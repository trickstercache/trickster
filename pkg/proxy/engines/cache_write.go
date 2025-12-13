/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations
 */

package engines

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
)

func writeConcurrent(_ context.Context, c cache.Cache, key string, d *HTTPDocument,
	compress bool, ttl time.Duration,
) error {
	var b []byte
	var err error

	// for memory cache, don't serialize the document, since we can retrieve it by reference.
	if c.Configuration().Provider == providerMemory {
		mc := c.(cache.MemoryCache)

		if d != nil {
			// during unmarshal, these would come back as false, so lets set them as such even for direct access
			d.rangePartsLoaded = false
			d.isFulfillment = false
			d.isLoaded = false
			d.RangeParts = nil

			if d.CachingPolicy != nil {
				d.CachingPolicy.ResetClientConditionals()
			}
		}
		return mc.StoreReference(key, d, ttl)
	}

	// for non-memory, we have to serialize the document to a byte slice to store
	b, err = d.MarshalMsg(nil)
	if err != nil {
		return err
	}

	if compress {
		buf := bytes.NewBuffer([]byte{1})
		encoder := brotli.NewWriter(buf)
		encoder.Write(b)
		encoder.Close()
		b = buf.Bytes()
	} else {
		buf := make([]byte, len(b)+1)
		copy(buf[1:], b)
		b = buf
	}

	return c.Store(key, b, ttl)
}

// WriteCache writes an HTTPDocument to the cache
func WriteCache(ctx context.Context, c cache.Cache, key string, d *HTTPDocument,
	ttl time.Duration, compressTypes sets.Set[string], marshal timeseries.MarshalerFunc,
) error {
	rsc := tc.Resources(ctx).(*request.Resources)

	ctx, span := tspan.NewChildSpan(ctx, rsc.Tracer, "WriteCache")
	if span != nil {
		defer span.End()
	}

	d.headerLock.Lock()
	h := http.Header(d.Headers)
	h.Del(headers.NameDate)
	h.Del(headers.NameTransferEncoding)
	h.Del(headers.NameContentRange)
	h.Del(headers.NameTricksterResult)
	ce := h.Get(headers.NameContentEncoding)
	d.headerLock.Unlock()

	var b []byte
	var err error
	var compress bool

	if (ce == "" || ce == "identity") &&
		(d.CachingPolicy == nil || !d.CachingPolicy.NoTransform) {
		if mt, _, err := mime.ParseMediaType(d.ContentType); err == nil {
			if _, ok := compressTypes[mt]; ok {
				compress = true
			}
		}
	}

	if c.Configuration().UseCacheChunking {
		rsc.Lock()
		trq := rsc.TimeRangeQuery
		rsc.Unlock()
		if trq != nil {
			// Use timeseries chunking
			chunker := NewTimeseriesChunkWriter(c, key, trq, marshal)
			err = executeChunking(ctx, c, key, d, compress, ttl, chunker)
		} else {
			// Use byterange chunking
			chunker := NewByterangeChunkWriter(c, key, d)
			err = executeChunking(ctx, c, key, d, compress, ttl, chunker)
		}
	} else {
		if marshal != nil {
			d.Body, _ = marshal(d.timeseries, nil, 0)
		}
		err = writeConcurrent(ctx, c, key, d, compress, ttl)
	}

	if err != nil {
		if span != nil {
			span.AddEvent(
				"Cache Write Failure",
				trace.EventOption(trace.WithAttributes(
					attribute.String("Error", err.Error()),
				)),
			)
		}
		return err
	}
	if span != nil {
		span.AddEvent(
			"Cache Write",
			trace.EventOption(trace.WithAttributes(
				attribute.Int("bytesWritten", len(b)),
			)),
		)
	}
	return nil
}

// DocumentFromHTTPResponse returns an HTTPDocument from the provided
// HTTP Response and Body
func DocumentFromHTTPResponse(resp *http.Response, body []byte,
	cp *CachingPolicy,
) *HTTPDocument {
	d := &HTTPDocument{}
	d.StatusCode = resp.StatusCode
	d.Status = resp.Status
	d.CachingPolicy = cp
	d.ContentLength = resp.ContentLength

	if resp.Header != nil {
		d.headerLock.Lock()
		d.Headers = resp.Header.Clone()
		d.headerLock.Unlock()
	}

	d.headerLock.Lock()
	ct := http.Header(d.Headers).Get(headers.NameContentType)
	d.headerLock.Unlock()
	if !strings.HasPrefix(ct, headers.ValueMultipartByteRanges) {
		d.ContentType = ct
	}

	if d.StatusCode == http.StatusPartialContent && body != nil && len(body) > 0 {
		d.ParsePartialContentBody(resp, body)
		if err := d.FulfillContentBody(); err != nil {
			return d
		}
	} else {
		d.SetBody(body)
	}

	return d
}

// CacheableDocument abstracts the source data for chunking operations when writing to cache.
type CacheableDocument interface {
	// GetMeta returns the metadata document that acts as a manifest for all chunks.
	GetMeta() *HTTPDocument

	// --- Timeseries Chunking Methods ---

	// GetTimeseriesChunk retrieves the specific chunk of data for a given time extent.
	GetTimeseriesChunk(extent timeseries.Extent) *HTTPDocument

	// --- Byterange Chunking Methods ---

	// getByteRanges returns the total extent of the data in byte ranges.
	getByteRanges() byterange.Ranges

	// GetByterangeChunk retrieves the specific chunk of data for a given byte range.
	GetByterangeChunk(dataRange byterange.Range, size int64) *HTTPDocument
}

// ChunkWriter abstracts the logic for different types of chunking when writing to cache.
type ChunkWriter interface {
	// Determine the number of chunks plus one for the metadata document.
	ChunkCount() int

	// Iterate over chunks and execute the provided write function for each.
	// The write function takes (index, subkey, chunkData).
	IterateChunks(
		d CacheableDocument, // The source data document
		writeFunc func(int, string, any) error,
	) error

	// GetMeta returns the metadata document to be stored separately.
	GetMeta(d CacheableDocument) any
}

// TimeseriesChunkWriter handles timeseries chunking operations when writing to cache
type TimeseriesChunkWriter struct {
	trq     *timeseries.TimeRangeQuery
	c       cache.Cache
	key     string
	cext    timeseries.Extent
	csize   time.Duration
	cct     int
	marshal timeseries.MarshalerFunc
}

// NewTimeseriesChunkWriter creates a new TimeseriesChunkWriter
func NewTimeseriesChunkWriter(c cache.Cache, key string, trq *timeseries.TimeRangeQuery, marshal timeseries.MarshalerFunc) *TimeseriesChunkWriter {
	csize := trq.Step * time.Duration(c.Configuration().TimeseriesChunkFactor)
	var cext timeseries.Extent
	cext.Start, cext.End = trq.Extent.Start.Truncate(csize), trq.Extent.End.Truncate(csize).Add(csize)
	cct := int(cext.End.Sub(cext.Start) / csize)

	return &TimeseriesChunkWriter{trq: trq, c: c, key: key, cext: cext, csize: csize, cct: cct, marshal: marshal}
}

func (tc *TimeseriesChunkWriter) ChunkCount() int {
	return tc.cct + 1 // chunks + meta
}

func (tc *TimeseriesChunkWriter) GetMeta(d CacheableDocument) any {
	return d.GetMeta()
}

func (tc *TimeseriesChunkWriter) IterateChunks(
	d CacheableDocument,
	writeFunc func(int, string, any) error,
) error {
	i := 0
	for chunkStart := tc.cext.Start; chunkStart.Before(tc.cext.End); chunkStart = chunkStart.Add(tc.csize) {
		chunkExtent := timeseries.Extent{
			Start: chunkStart,
			End:   chunkStart.Add(tc.csize - tc.trq.Step),
		}
		subkey := getSubKey(tc.key, chunkExtent)
		chunkData := d.GetTimeseriesChunk(chunkExtent)

		// Handle serialization for non-memory providers
		if tc.c.Configuration().Provider != providerMemory && tc.marshal != nil {
			chunkData.Body, _ = tc.marshal(chunkData.timeseries, nil, 0)
		}

		if err := writeFunc(i, subkey, chunkData); err != nil {
			return err
		}
		i++
	}
	return nil
}

// ByterangeChunkWriter handles byterange chunking operations when writing to cache
type ByterangeChunkWriter struct {
	c    cache.Cache
	key  string
	size int64
	crs  int64 // chunk range start
	cre  int64 // chunk range end
	cct  int64 // chunk count
}

// NewByterangeChunkWriter creates a new byterange chunk writer
func NewByterangeChunkWriter(c cache.Cache, key string, d CacheableDocument) *ByterangeChunkWriter {
	drs := d.getByteRanges()
	size := c.Configuration().ByterangeChunkSize
	crs, cre := drs[0].Start, drs[len(drs)-1].End
	crs = (crs / size) * size
	cre = (cre/size + 1) * size
	cct := (cre - crs) / size

	return &ByterangeChunkWriter{
		c:    c,
		key:  key,
		size: size,
		crs:  crs,
		cre:  cre,
		cct:  cct,
	}
}

func (bc *ByterangeChunkWriter) ChunkCount() int {
	return int(bc.cct) + 1 // +1 for meta
}

func (bc *ByterangeChunkWriter) IterateChunks(
	d CacheableDocument,
	writeFunc func(int, string, any) error,
) error {
	i := 0
	for chunkStart := bc.crs; chunkStart < bc.cre; chunkStart += bc.size {
		chunkRange := byterange.Range{
			Start: chunkStart,
			End:   chunkStart + bc.size - 1,
		}
		subkey := bc.key + chunkRange.String()
		chunkData := d.GetByterangeChunk(chunkRange, bc.size)

		if err := writeFunc(i, subkey, chunkData); err != nil {
			return err
		}
		i++
	}
	return nil
}

func (bc *ByterangeChunkWriter) GetMeta(d CacheableDocument) any {
	return d.GetMeta()
}

// executeChunking performs the generic chunking and concurrent write logic.
func executeChunking(ctx context.Context, c cache.Cache, key string, d CacheableDocument, compress bool, ttl time.Duration, chunker ChunkWriter) error {
	cct := chunker.ChunkCount()
	cr := make([]error, cct) // Error slice size is chunks + 1 (for meta)

	eg := errgroup.Group{}
	eg.SetLimit(16) // FIXME: make configurable

	// 1. Iterate over chunks and start concurrent writes
	err := chunker.IterateChunks(d, func(index int, subkey string, chunkData any) error {
		// This is the core concurrent write logic
		eg.Go(func() error {
			httpDoc, ok := chunkData.(*HTTPDocument)
			if !ok {
				return errors.New("invalid chunk data type")
			}
			cr[index] = writeConcurrent(ctx, c, subkey, httpDoc, compress, ttl)
			return nil
		})
		return nil // The return value here is unused, we use the errgroup for final errors
	})
	if err != nil {
		return err
	}

	// The last index is reserved for the metadata document.
	metaIndex := cct - 1
	meta := chunker.GetMeta(d)

	// 2. Store metadocument concurrently
	eg.Go(func() error {
		httpDoc, ok := meta.(*HTTPDocument)
		if !ok {
			return errors.New("invalid meta data type")
		}
		cr[metaIndex] = writeConcurrent(ctx, c, key, httpDoc, compress, ttl)
		return nil
	})

	// 3. Wait on writes to finish and handle results
	if err := eg.Wait(); err != nil {
		return err
	}
	return errors.Join(cr...)
}
