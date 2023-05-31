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
 * limitations under the License.
 */

package engines

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/andybalholm/brotli"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type queryResult struct {
	queryKey     string
	d            *HTTPDocument
	lookupStatus status.LookupStatus
	err          error
}

func queryConcurrent(ctx context.Context, c cache.Cache, key string, cr chan<- *queryResult, done func()) *queryResult {
	if done != nil {
		defer done()
	}
	qr := &queryResult{queryKey: key, d: &HTTPDocument{}}
	if c.Configuration().Provider == "memory" {
		mc := c.(cache.MemoryCache)
		var ifc interface{}
		ifc, qr.lookupStatus, qr.err = mc.RetrieveReference(key, true)

		if qr.err != nil || (qr.lookupStatus != status.LookupStatusHit) {
			if cr != nil {
				cr <- qr
			}
			return qr
		}

		if ifc != nil {
			qr.d, _ = ifc.(*HTTPDocument)
		} else {
			if cr != nil {
				cr <- qr
			}
			return qr
		}

	} else {
		var b []byte
		b, qr.lookupStatus, qr.err = c.Retrieve(key, true)

		if qr.err != nil || (qr.lookupStatus != status.LookupStatusHit) {
			if cr != nil {
				cr <- qr
			}
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
				if cr != nil {
					cr <- qr
				}
				return qr
			}

		}
		_, qr.err = qr.d.UnmarshalMsg(b)
		if qr.err != nil {
			if cr != nil {
				cr <- qr
			}
			return qr
		}
	}
	if cr != nil {
		cr <- qr
	}
	return qr
}

// QueryCache queries the cache for an HTTPDocument and returns it
func QueryCache(ctx context.Context, c cache.Cache, key string,
	ranges byterange.Ranges, unmarshal timeseries.UnmarshalerFunc) (*HTTPDocument, status.LookupStatus, byterange.Ranges, error) {

	rsc := tc.Resources(ctx).(*request.Resources)

	ctx, span := tspan.NewChildSpan(ctx, rsc.Tracer, "QueryCache")
	if span != nil {
		defer span.End()
	}

	var d *HTTPDocument
	var lookupStatus status.LookupStatus

	// Query document
	qr := queryConcurrent(ctx, c, key, nil, nil)
	if qr.err != nil {
		return qr.d, qr.lookupStatus, ranges, qr.err
	} else {
		if unmarshal != nil {
			qr.d.timeseries, _ = unmarshal(qr.d.Body, nil)
		}
		d = qr.d
		lookupStatus = qr.lookupStatus
	}

	// If we got a meta document and want to use cache chunking, do so
	if c.Configuration().UseCacheChunking {
		if trq := rsc.TimeRangeQuery; trq != nil {
			// Do timeseries chunk retrieval
			// Determine chunk extent and number of chunks
			var cext timeseries.Extent
			var csize = trq.Step * time.Duration(c.Configuration().TimeseriesChunkFactor)
			var cct int
			cext.Start, cext.End = trq.Extent.Start.Truncate(csize), trq.Extent.End.Truncate(csize).Add(csize)
			cct = int(cext.End.Sub(cext.Start) / csize)
			// Prepare buffered results and waitgroup
			wg := &sync.WaitGroup{}
			// Result slice of timeseries
			ress := make([]timeseries.Timeseries, cct)
			resi := 0
			for chunkStart := cext.Start; chunkStart.Before(cext.End); chunkStart = chunkStart.Add(csize) {
				// Chunk range (inclusive, on-step)
				chunkExtent := timeseries.Extent{
					Start: chunkStart,
					End:   chunkStart.Add(csize - trq.Step),
				}
				// Derive subkey
				subkey := key + chunkExtent.String()
				// Query
				wg.Add(1)
				go func(outIdx int) {
					defer wg.Done()
					qr := queryConcurrent(ctx, c, subkey, nil, nil)
					if c.Configuration().Provider != "memory" {
						qr.d.timeseries, qr.err = unmarshal(qr.d.Body, nil)
					}
					if qr.err == nil {
						ress[outIdx] = qr.d.timeseries
					}
				}(resi)
				resi++
			}
			// Wait on queries
			wg.Wait()
			d.timeseries = ress[0]
			d.timeseries.Merge(true, ress[1:]...)
			if d.timeseries != nil {
				d.timeseries.SetExtents(d.timeseries.Extents().Compress(trq.Step))
			}
		} else {
			// Do byterange chunking
			// Determine chunk start/end and number of chunks
			var crs, cre, cct int64
			if len(ranges) == 0 {
				ranges = byterange.Ranges{byterange.Range{Start: 0, End: d.ContentLength - 1}}
			}
			size := c.Configuration().ByterangeChunkSize
			crs, cre = ranges[0].Start, ranges[len(ranges)-1].End
			crs = (crs / size) * size
			cre = (cre/size + 1) * size
			cct = (cre - crs) / size
			// Allocate body in meta document
			d.Body = make([]byte, d.ContentLength)
			// Prepare buffered results and waitgroup
			cr := make(chan *queryResult, cct)
			wg := &sync.WaitGroup{}
			// Iterate chunks
			for chunkStart := crs; chunkStart < cre; chunkStart += size {
				// Determine chunk range (inclusive)
				chunkRange := byterange.Range{
					Start: chunkStart,
					End:   chunkStart + size - 1,
				}
				// Determine subkey
				subkey := key + chunkRange.String()
				// Query subdocument
				wg.Add(1)
				go queryConcurrent(ctx, c, subkey, cr, wg.Done)
			}
			// Wait on queries to finish (result channel is buffered and doesn't hold for receive)
			wg.Wait()
			close(cr)
			// Handle results
			dbl_lock := &sync.Mutex{}
			var dbl int64
			for qr := range cr {
				// Return on error
				if qr.err != nil && !errors.Is(qr.err, cache.ErrKNF) {
					return qr.d, qr.lookupStatus, ranges, qr.err
				}
				// Merge with meta document on success
				// We can do this concurrently since chunk ranges don't overlap

				wg.Add(1)
				go func(qrc *queryResult) {
					defer wg.Done()
					if qrc.d.IsMeta {
						return
					}
					if qrc.lookupStatus == status.LookupStatusHit {
						for _, r := range qrc.d.Ranges {
							content := qrc.d.Body[r.Start%size : r.End%size+1]
							r.Copy(d.Body, content)
							dbl_lock.Lock()
							if r.End+1 > dbl {
								dbl = r.End + 1
							}
							dbl_lock.Unlock()
						}
					}
				}(qr)
			}
			wg.Wait()
			if len(d.Ranges) > 1 {
				d.StoredRangeParts = make(map[string]*byterange.MultipartByteRange)
				for _, r := range d.Ranges {
					d.StoredRangeParts[r.String()] = &byterange.MultipartByteRange{
						Range:   r,
						Content: d.Body[r.Start : r.End+1],
					}
				}
				d.Body = nil
			} else {
				d.Body = d.Body[:dbl]
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
		delta = ranges.CalculateDelta(d.Ranges, d.ContentLength)
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

func writeConcurrent(ctx context.Context, c cache.Cache, key string, d *HTTPDocument,
	compress bool, ttl time.Duration, cr chan<- error, done func()) {

	if done != nil {
		defer done()
	}
	var b []byte
	var err error

	// for memory cache, don't serialize the document, since we can retrieve it by reference.
	if c.Configuration().Provider == "memory" {
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
		cr <- mc.StoreReference(key, d, ttl)
		return
	}

	// for non-memory, we have to serialize the document to a byte slice to store
	b, err = d.MarshalMsg(nil)
	if err != nil {
		cr <- err
		return
	}

	if compress {
		// tl.Debug(rsc.Logger, "compressing cache data", tl.Pairs{"cacheKey": key})
		buf := bytes.NewBuffer([]byte{1})
		encoder := brotli.NewWriter(buf)
		encoder.Write(b)
		encoder.Close()
		b = buf.Bytes()
	} else {
		b = append([]byte{0}, b...)
	}

	cr <- c.Store(key, b, ttl)
}

// WriteCache writes an HTTPDocument to the cache
func WriteCache(ctx context.Context, c cache.Cache, key string, d *HTTPDocument,
	ttl time.Duration, compressTypes map[string]interface{}, marshal timeseries.MarshalerFunc) error {

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
		if trq := rsc.TimeRangeQuery; trq != nil {
			// Do timeseries chunking
			meta := d.GetMeta()
			// Determine chunk extent and number of chunks
			var cext timeseries.Extent
			var csize = trq.Step * time.Duration(c.Configuration().TimeseriesChunkFactor)
			var cct int
			cext.Start, cext.End = trq.Extent.Start.Truncate(csize), trq.Extent.End.Truncate(csize).Add(csize)
			cct = int(cext.End.Sub(cext.Start) / csize)
			// Prepare buffered results and waitgroup
			cr := make(chan error, cct+1)
			wg := &sync.WaitGroup{}
			for chunkStart := cext.Start; chunkStart.Before(cext.End); chunkStart = chunkStart.Add(csize) {
				// Chunk range (inclusive, on-step)
				chunkExtent := timeseries.Extent{
					Start: chunkStart,
					End:   chunkStart.Add(csize - trq.Step),
				}
				// Derive subkey
				subkey := key + chunkExtent.String()
				// Query
				wg.Add(1)
				go func() {
					cd := d.GetTimeseriesChunk(chunkExtent)
					if c.Configuration().Provider != "memory" {
						cd.Body, _ = marshal(cd.timeseries, nil, 0)
					}
					writeConcurrent(ctx, c, subkey, cd, compress, ttl, cr, wg.Done)
				}()
			}
			// Store metadocument
			wg.Add(1)
			go writeConcurrent(ctx, c, key, meta, compress, ttl, cr, wg.Done)
			// Wait on writes to finish (result channel is buffered and doesn't hold for receive)
			wg.Wait()
			close(cr)
			// Handle results
			for res := range cr {
				if res != nil && err != nil {
					err = res
					break
				}
			}
		} else {
			// Do byterange chunking
			// Determine chunk start/end and number of chunks
			drs := d.getByteRanges()
			size := c.Configuration().ByterangeChunkSize
			crs, cre := drs[0].Start, drs[len(drs)-1].End
			crs = (crs / size) * size
			cre = (cre/size + 1) * size
			cct := (cre - crs) / size
			// Create meta document
			meta := d.GetMeta()
			// Prepare buffered results and waitgroup
			cr := make(chan error, cct+1)
			wg := &sync.WaitGroup{}
			// Iterate chunks
			for chunkStart := crs; chunkStart < cre; chunkStart += size {
				// Determine chunk range (inclusive)
				chunkRange := byterange.Range{
					Start: chunkStart,
					End:   chunkStart + size - 1,
				}
				// Determine subkey
				subkey := key + chunkRange.String()
				// Get chunk
				cd := d.GetByterangeChunk(chunkRange, size)
				// Store subdocument
				wg.Add(1)
				go writeConcurrent(ctx, c, subkey, cd, compress, ttl, cr, wg.Done)
			}
			// Store metadocument
			wg.Add(1)
			go writeConcurrent(ctx, c, key, meta, compress, ttl, cr, wg.Done)
			// Wait on writes to finish (result channel is buffered and doesn't hold for receive)
			wg.Wait()
			close(cr)
			// Handle results
			for res := range cr {
				if res != nil && err != nil {
					err = res
					break
				}
			}
		}
	} else {
		// Write concurrently
		cr := make(chan error)
		go func() {
			if marshal != nil {
				d.Body, _ = marshal(d.timeseries, nil, 0)
			}
			writeConcurrent(ctx, c, key, d, compress, ttl, cr, nil)
		}()
		err = <-cr
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
	cp *CachingPolicy, logger interface{}) *HTTPDocument {
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
		d.ParsePartialContentBody(resp, body, logger)
		d.FulfillContentBody()
	} else {
		d.SetBody(body)
	}

	return d
}
