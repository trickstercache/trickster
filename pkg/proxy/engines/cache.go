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
 * limitations// ByterangeChunkQueryProcessor implements ChunkQueryProcessor for byterange chunks (reading)
type ByterangeChunkQueryProcessor struct {
	d    *HTTPDocument
	size int64
	dbl  *int64 // atomic counter for document body length
}

func (bcp *ByterangeChunkQueryProcessor) ProcessChunk(index int, subkey string, qr *queryResult, c cache.Cache) error {he License.
*/

package engines

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
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

const (
	providerMemory = "memory"
)

type queryResult struct {
	queryKey     string
	d            *HTTPDocument
	lookupStatus status.LookupStatus
	err          error
}

func queryConcurrent(_ context.Context, c cache.Cache, key string) *queryResult {
	qr := &queryResult{queryKey: key, d: &HTTPDocument{}}
	if c.Configuration().Provider == providerMemory {
		mc := c.(cache.MemoryCache)
		var ifc any
		ifc, qr.lookupStatus, qr.err = mc.RetrieveReference(key)

		if qr.err != nil || (qr.lookupStatus != status.LookupStatusHit) {
			return qr
		}

		if ifc == nil {
			return qr
		}
		qr.d, _ = ifc.(*HTTPDocument)
	} else {
		var b []byte
		b, qr.lookupStatus, qr.err = c.Retrieve(key)

		if qr.err != nil || (qr.lookupStatus != status.LookupStatusHit) {
			return qr
		}

		var inflate bool
		// check and remove compression bit
		if len(b) > 0 {
			if b[0] == 1 {
				inflate = true
			}
			b = b[1:]
		}

		if inflate {
			// tl.Debug(rsc.Logger, "decompressing cached data", tl.Pairs{"cacheKey": key})
			decoder := brotli.NewReader(bytes.NewReader(b))
			b, qr.err = io.ReadAll(decoder)
			if qr.err != nil {
				return qr
			}
		}
		_, qr.err = qr.d.UnmarshalMsg(b)
		if qr.err != nil {
			return qr
		}
	}
	return qr
}

// QueryCache queries the cache for an HTTPDocument and returns it
func QueryCache(ctx context.Context, c cache.Cache, key string,
	ranges byterange.Ranges, unmarshal timeseries.UnmarshalerFunc,
) (*HTTPDocument, status.LookupStatus, byterange.Ranges, error) {
	rsc := tc.Resources(ctx).(*request.Resources)

	ctx, span := tspan.NewChildSpan(ctx, rsc.Tracer, "QueryCache")
	if span != nil {
		defer span.End()
	}

	var d *HTTPDocument
	var lookupStatus status.LookupStatus

	// Query document
	qr := queryConcurrent(ctx, c, key)
	if qr.err != nil {
		return qr.d, qr.lookupStatus, ranges, qr.err
	}
	if unmarshal != nil {
		qr.d.timeseries, _ = unmarshal(qr.d.Body, nil)
	}
	d = qr.d
	lookupStatus = qr.lookupStatus

	// If we got a meta document and want to use cache chunking, do so
	if c.Configuration().UseCacheChunking {
		if trq := rsc.TimeRangeQuery; trq != nil {
			// Use timeseries chunk querying
			err := executeTimeseriesChunkQuery(ctx, c, key, d, trq, unmarshal)
			if err != nil {
				return nil, status.LookupStatusKeyMiss, ranges, err
			}
		} else {
			// Use byterange chunk querying
			err := executeByterangeChunkQuery(ctx, c, key, d, ranges)
			if err != nil {
				return nil, status.LookupStatusKeyMiss, ranges, err
			}
		}
	}

	var delta byterange.Ranges

	// Fulfillment is when we have a range stored, but a subsequent user wants the whole body, so
	// we must inflate the requested range to be the entire object in order to get the correct delta.
	d.isFulfillment = (len(d.Ranges) > 0) && (len(ranges) == 0)

	if d.isFulfillment {
		if span != nil {
			span.AddEvent("Cache Fulfillment")
		}
		ranges = byterange.Ranges{byterange.Range{Start: 0, End: d.ContentLength - 1}}
	}

	if len(ranges) > 0 && len(d.Ranges) > 0 {
		delta = d.Ranges.CalculateDeltas(ranges, d.ContentLength)
		if len(delta) > 0 {
			if len(d.Body) > 0 {
				// If there's delta, we need to treat this as a partial hit; move all of d's content to RangeParts
				// Ignore ranges in d that are not bounded by the requested ranges
				// min, max := ranges[0].Start, ranges[len(ranges)-1].End
				d.RangeParts = make(byterange.MultipartByteRanges)
				for _, r := range d.Ranges {
					content := d.Body[r.Start : r.End+1]
					d.RangeParts[r] = &byterange.MultipartByteRange{
						Range:   r,
						Content: content,
					}
				}
				d.StoredRangeParts = d.RangeParts.PackableMultipartByteRanges()
				d.Body = nil
			}
			if delta.Equal(ranges) {
				lookupStatus = status.LookupStatusRangeMiss
			} else {
				lookupStatus = status.LookupStatusPartialHit
			}
		}
	}
	d.IsMeta = false
	d.IsChunk = false

	tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", lookupStatus.String()))
	return d, lookupStatus, delta, nil
}

func stripConditionalHeaders(h http.Header) {
	h.Del(headers.NameIfMatch)
	h.Del(headers.NameIfUnmodifiedSince)
	h.Del(headers.NameIfNoneMatch)
	h.Del(headers.NameIfModifiedSince)
}

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

func getSubKey(key string, chunkExtent timeseries.Extent) string {
	return fmt.Sprintf("%s.%s", key, chunkExtent)
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

// ChunkQueryIterator provides iteration over chunks for cache queries (reading)
type ChunkQueryIterator interface {
	// IterateChunks calls the provided function for each chunk
	// The function receives (index, subkey) and should return whether to continue
	IterateChunks(func(int, string) bool)
}

// ChunkQueryProcessor handles the result of querying a single chunk from cache (reading)
type ChunkQueryProcessor interface {
	// ProcessChunk processes a successful query result for a chunk
	ProcessChunk(index int, subkey string, qr *queryResult, c cache.Cache) error

	// Finalize performs any final processing after all chunks are processed
	Finalize() error
}

// executeChunkQuery performs generic chunk querying with early cancellation (reading from cache)
func executeChunkQuery(ctx context.Context, c cache.Cache, iterator ChunkQueryIterator, processor ChunkQueryProcessor) error {
	// Prepare waitgroup for concurrent processing
	eg := errgroup.Group{}
	eg.SetLimit(16) // FIXME: make configurable

	// Early cancellation context
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var firstErr atomic.Pointer[error]

	iterator.IterateChunks(func(index int, subkey string) bool {
		// Check if we should abort due to previous error
		select {
		case <-ctx.Done():
			return false
		default:
		}

		eg.Go(func() error {
			qr := queryConcurrent(ctx, c, subkey)
			if qr.lookupStatus != status.LookupStatusHit &&
				(qr.err == nil || errors.Is(qr.err, cache.ErrKNF)) {
				return nil
			}
			if qr.err != nil {
				// Set first error and cancel remaining operations
				if firstErr.CompareAndSwap(nil, &qr.err) {
					cancel()
				}
				logger.Error("chunk query failed",
					logging.Pairs{
						"error": qr.err, "chunkIdx": index,
						"key": subkey, "cacheQueryStatus": qr.lookupStatus,
					})
				return qr.err
			}

			// Process the successful result
			if err := processor.ProcessChunk(index, subkey, qr, c); err != nil {
				if firstErr.CompareAndSwap(nil, &err) {
					cancel()
				}
				return err
			}
			return nil
		})
		return true
	})

	// Wait on queries
	if err := eg.Wait(); err != nil {
		return err
	}

	// Check if we had any errors
	if err := firstErr.Load(); err != nil {
		return *err
	}

	// Finalize processing
	return processor.Finalize()
}

// TimeseriesChunkQueryIterator implements ChunkQueryIterator for timeseries chunks (reading)
type TimeseriesChunkQueryIterator struct {
	key   string
	cext  timeseries.Extent
	csize time.Duration
	trq   *timeseries.TimeRangeQuery
}

func (tci *TimeseriesChunkQueryIterator) IterateChunks(fn func(int, string) bool) {
	var resi int
	for chunkStart := tci.cext.Start; chunkStart.Before(tci.cext.End); chunkStart = chunkStart.Add(tci.csize) {
		chunkExtent := timeseries.Extent{
			Start: chunkStart,
			End:   chunkStart.Add(tci.csize - tci.trq.Step),
		}
		subkey := getSubKey(tci.key, chunkExtent)
		if !fn(resi, subkey) {
			break
		}
		resi++
	}
}

// TimeseriesChunkQueryProcessor implements ChunkQueryProcessor for timeseries chunks (reading)
type TimeseriesChunkQueryProcessor struct {
	d         *HTTPDocument
	trq       *timeseries.TimeRangeQuery
	unmarshal timeseries.UnmarshalerFunc
	ress      timeseries.List
}

func (tcp *TimeseriesChunkQueryProcessor) ProcessChunk(index int, subkey string, qr *queryResult, c cache.Cache) error {
	if c.Configuration().Provider != providerMemory {
		var err error
		qr.d.timeseries, err = tcp.unmarshal(qr.d.Body, nil)
		if err != nil {
			logger.Error("chunk unmarshal failed",
				logging.Pairs{
					"error": err, "chunkIdx": index,
					"key": subkey, "cacheQueryStatus": qr.lookupStatus,
				})
			return err
		}
	}
	if qr.d.timeseries != nil {
		tcp.ress[index] = qr.d.timeseries
	}
	return nil
}

func (tcp *TimeseriesChunkQueryProcessor) Finalize() error {
	tcp.d.timeseries = tcp.ress.Merge(true)
	if tcp.d.timeseries != nil {
		tcp.d.timeseries.SetExtents(tcp.d.timeseries.Extents().Compress(tcp.trq.Step))
	}
	return nil
}

// executeTimeseriesChunkQuery performs timeseries chunk querying with early cancellation
func executeTimeseriesChunkQuery(ctx context.Context, c cache.Cache, key string, d *HTTPDocument, trq *timeseries.TimeRangeQuery, unmarshal timeseries.UnmarshalerFunc) error {
	// Determine chunk extent and number of chunks
	var cext timeseries.Extent
	csize := trq.Step * time.Duration(c.Configuration().TimeseriesChunkFactor)
	cext.Start, cext.End = trq.Extent.Start.Truncate(csize), trq.Extent.End.Truncate(csize).Add(csize)
	cct := int(cext.End.Sub(cext.Start) / csize)

	iterator := &TimeseriesChunkQueryIterator{
		key:   key,
		cext:  cext,
		csize: csize,
		trq:   trq,
	}

	processor := &TimeseriesChunkQueryProcessor{
		d:         d,
		trq:       trq,
		unmarshal: unmarshal,
		ress:      make(timeseries.List, cct),
	}

	return executeChunkQuery(ctx, c, iterator, processor)
}

// ByterangeChunkQueryIterator implements ChunkQueryIterator for byterange chunks (reading)
type ByterangeChunkQueryIterator struct {
	key  string
	crs  int64
	cre  int64
	size int64
}

func (bci *ByterangeChunkQueryIterator) IterateChunks(fn func(int, string) bool) {
	var i int
	for chunkStart := bci.crs; chunkStart < bci.cre; chunkStart += bci.size {
		chunkRange := byterange.Range{
			Start: chunkStart,
			End:   chunkStart + bci.size - 1,
		}
		subkey := bci.key + chunkRange.String()
		if !fn(i, subkey) {
			break
		}
		i++
	}
}

// ByterangeChunkQueryProcessor implements ChunkQueryProcessor for byterange chunks (reading)
type ByterangeChunkQueryProcessor struct {
	d    *HTTPDocument
	size int64
	dbl  *int64 // atomic counter for document body length
}

func (bcp *ByterangeChunkQueryProcessor) ProcessChunk(index int, subkey string, qr *queryResult, c cache.Cache) error {
	if qr == nil {
		return nil
	}

	// Handle error - different from timeseries as we allow cache.ErrKNF
	if qr.err != nil && !errors.Is(qr.err, cache.ErrKNF) {
		return qr.err
	}

	// Process successful result immediately
	if !qr.d.IsMeta && qr.lookupStatus == status.LookupStatusHit {
		for _, r := range qr.d.Ranges {
			content := qr.d.Body[r.Start%bcp.size : r.End%bcp.size+1]
			r.Copy(bcp.d.Body, content)
			if v := atomic.LoadInt64(bcp.dbl); r.End+1 > v {
				atomic.CompareAndSwapInt64(bcp.dbl, v, r.End+1)
			}
		}
	}
	return nil
}

func (bcp *ByterangeChunkQueryProcessor) Finalize() error {
	if len(bcp.d.Ranges) > 1 {
		bcp.d.StoredRangeParts = make(map[string]*byterange.MultipartByteRange)
		for _, r := range bcp.d.Ranges {
			bcp.d.StoredRangeParts[r.String()] = &byterange.MultipartByteRange{
				Range:   r,
				Content: bcp.d.Body[r.Start : r.End+1],
			}
		}
		bcp.d.Body = nil
	} else {
		bcp.d.Body = bcp.d.Body[:*bcp.dbl]
	}
	return nil
}

// executeByterangeChunkQuery performs byterange chunk querying with early cancellation
func executeByterangeChunkQuery(ctx context.Context, c cache.Cache, key string, d *HTTPDocument, ranges byterange.Ranges) error {
	// Determine chunk start/end
	var crs, cre int64
	if len(ranges) == 0 {
		ranges = byterange.Ranges{byterange.Range{Start: 0, End: d.ContentLength - 1}}
	}
	size := c.Configuration().ByterangeChunkSize
	crs, cre = ranges[0].Start, ranges[len(ranges)-1].End
	crs = (crs / size) * size
	cre = (cre/size + 1) * size

	// Allocate body in meta document
	d.Body = make([]byte, d.ContentLength)

	var dbl int64 // Track document body length

	iterator := &ByterangeChunkQueryIterator{
		key:  key,
		crs:  crs,
		cre:  cre,
		size: size,
	}

	processor := &ByterangeChunkQueryProcessor{
		d:    d,
		size: size,
		dbl:  &dbl,
	}

	return executeChunkQuery(ctx, c, iterator, processor)
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
