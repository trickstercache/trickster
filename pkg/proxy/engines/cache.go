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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
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

func query(rsc *request.Resources, c cache.Cache, key string,
	ranges byterange.Ranges, trq *timeseries.TimeRangeQuery,
	span trace.Span) (*HTTPDocument, status.LookupStatus, byterange.Ranges, error) {

	d := &HTTPDocument{}
	b, lookupStatus, err := c.Retrieve(key, true)

	if err != nil || (lookupStatus != status.LookupStatusHit) {
		var nr byterange.Ranges
		if lookupStatus == status.LookupStatusKeyMiss && ranges != nil && len(ranges) > 0 {
			nr = ranges

		}
		tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", lookupStatus.String()))
		return d, lookupStatus, nr, err
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
		tl.Debug(rsc.Logger, "decompressing cached data", tl.Pairs{"cacheKey": key})
		decoder := brotli.NewReader(bytes.NewReader(b))
		b, err = io.ReadAll(decoder)
		if err != nil {
			tl.Error(rsc.Logger, "error decoding cache document", tl.Pairs{
				"cacheKey": key,
				"detail":   err.Error(),
			})
			tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", status.LookupStatusKeyMiss.String()))
			return d, status.LookupStatusKeyMiss, ranges, err
		}

	}
	_, err = d.UnmarshalMsg(b)
	if err != nil {
		tl.Error(rsc.Logger, "error unmarshaling cache document", tl.Pairs{
			"cacheKey": key,
			"detail":   err.Error(),
		})
		tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", status.LookupStatusKeyMiss.String()))
		return d, status.LookupStatusKeyMiss, ranges, err
	}
	if trq != nil {
		if rsc.CacheUnmarshaler == nil {
			tl.Error(rsc.Logger, "querycache asked for a timerange, but no unmarshaler was provided", tl.Pairs{
				"cacheKey": key,
				"detail":   err.Error(),
			})
			tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", status.LookupStatusError.String()))
			return d, status.LookupStatusError, ranges, err
		}
		cts, err := rsc.CacheUnmarshaler(d.Body, trq)
		if err != nil {
			tl.Error(rsc.Logger, "error unmarshaling cache timeseries", tl.Pairs{
				"cacheKey": key,
				"detail":   err.Error(),
			})
			tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", status.LookupStatusError.String()))
			return d, status.LookupStatusError, ranges, err
		}
		d.timeseries = cts
	}
	return d, lookupStatus, ranges, err
}

// QueryCache queries the cache for an HTTPDocument and returns it
func QueryCache(ctx context.Context, c cache.Cache, key string,
	ranges byterange.Ranges, trq *timeseries.TimeRangeQuery) (*HTTPDocument, status.LookupStatus, byterange.Ranges, error) {

	rsc := tc.Resources(ctx).(*request.Resources)
	doCacheChunk := c.Configuration().UseCacheChunking
	chunkFactor := time.Duration(c.Configuration().TimeseriesChunkFactor)

	ctx, span := tspan.NewChildSpan(ctx, rsc.Tracer, "QueryCache")
	if span != nil {
		defer span.End()
	}

	d := &HTTPDocument{}
	var lookupStatus status.LookupStatus
	var err error

	if c.Configuration().Provider == "memory" {
		mc := c.(cache.MemoryCache)
		var ifc interface{}
		ifc, lookupStatus, err = mc.RetrieveReference(key, true)

		if err != nil || (lookupStatus != status.LookupStatusHit) {
			var nr byterange.Ranges
			if lookupStatus == status.LookupStatusKeyMiss && ranges != nil && len(ranges) > 0 {
				nr = ranges
			}

			tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", lookupStatus.String()))

			return d, lookupStatus, nr, err
		}

		if ifc != nil {
			d, _ = ifc.(*HTTPDocument)
		} else {
			tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", status.LookupStatusKeyMiss.String()))
			return d, status.LookupStatusKeyMiss, ranges, err
		}

	} else {
		if doCacheChunk {
			if trq != nil {
				if trq.Step == 0 {
					return d, status.LookupStatusError, ranges, errors.New("cache timeseries query must have a range when using chunking")
				}
				// Determine step and chunk size
				step := trq.Step
				size := step * chunkFactor
				// Establish a duration start->end such that it is aligned to the epoch along size and contains all of trq
				rootExt := trq.Extent
				start, end := rootExt.Start.Truncate(size), rootExt.End.Truncate(size).Add(size)
				// Iterate through that duration in chunks of size
				var dd *HTTPDocument
				lookupCt, lookupFound := 0, 0
				for chunkStart := start; chunkStart.Before(end); chunkStart = chunkStart.Add(size) {
					// End chunk one step before next; steps are inclusive
					chunkEnd := chunkStart.Add(size - step)
					chunkExt := timeseries.Extent{
						Start: chunkStart,
						End:   chunkEnd,
					}
					chunkKey := key + chunkExt.String()
					dd, lookupStatus, ranges, err = query(rsc, c, chunkKey, ranges, trq, span)
					lookupCt++
					if err != nil || lookupStatus == status.LookupStatusKeyMiss {
						continue
					}
					lookupFound++
					dd.timeseries, err = rsc.CacheUnmarshaler(dd.Body, trq)
					if err != nil {
						tl.Error(rsc.Logger, "error unmarshaling cache document chunk", tl.Pairs{
							"cacheKey": chunkKey,
							"detail":   err.Error(),
						})
					}
					if d.timeseries == nil {
						d.timeseries = dd.timeseries
					} else {
						d.timeseries.Merge(true, dd.timeseries)
					}
				}
				if lookupFound == 0 {
					return d, status.LookupStatusKeyMiss, ranges, cache.ErrKNF
				} else if lookupFound == lookupCt {
					lookupStatus = status.LookupStatusHit
				} else {
					lookupStatus = status.LookupStatusPartialHit
				}
			} else {
				size := int64(c.Configuration().ByterangeChunkSize)
				start, end := ranges[0].Start, ranges[len(ranges)-1].End
				start = start - (start % size)
				end = end + size - (end % size)
				lookupCt, lookupFound := 0, 0
				for chunkStart := start; chunkStart < end; chunkStart += size {
					chunkEnd := chunkStart + size - 1
					chunkRange := byterange.Range{
						Start: chunkStart,
						End:   chunkEnd,
					}
					chunkKey := key + ":" + chunkRange.String()
					dd, lstat, got, err := query(rsc, c, chunkKey, byterange.Ranges{chunkRange}, trq, span)
					lookupCt++
					if err != nil || lstat == status.LookupStatusError {
						return dd, lstat, got, err
					}
					if lstat == status.LookupStatusKeyMiss {
						continue
					}
					lookupFound++
					if d.Body == nil {
						d.Body = dd.Body
						d.Ranges = byterange.Ranges{chunkRange}
					} else {
						d.Body = append(d.Body, dd.Body...)
						d.Ranges = append(d.Ranges, chunkRange)
					}
				}
				if lookupFound == 0 {
					return d, status.LookupStatusKeyMiss, ranges, cache.ErrKNF
				} else if lookupFound == lookupCt {
					lookupStatus = status.LookupStatusHit
				} else {
					lookupStatus = status.LookupStatusPartialHit
				}
			}
		} else {
			d, lookupStatus, ranges, err = query(rsc, c, key, ranges, trq, span)
			if err != nil {
				return d, lookupStatus, ranges, err
			}
		}
	}

	var delta byterange.Ranges

	// Fulfillment is when we have a range stored, but a subsequent user wants the whole body, so
	// we must inflate the requested range to be the entire object in order to get the correct delta.
	d.isFulfillment = (d.Ranges != nil && len(d.Ranges) > 0) && (ranges == nil || len(ranges) == 0)

	if d.isFulfillment {
		if span != nil {
			span.AddEvent("Cache Fulfillment")
		}
		ranges = byterange.Ranges{byterange.Range{Start: 0, End: d.ContentLength - 1}}
	}

	if ranges != nil && len(ranges) > 0 && d.Ranges != nil && len(d.Ranges) > 0 {
		delta = ranges.CalculateDelta(d.Ranges, d.ContentLength)
		if delta != nil && len(delta) > 0 {
			if delta.Equal(ranges) {
				lookupStatus = status.LookupStatusRangeMiss
			} else {
				lookupStatus = status.LookupStatusPartialHit
			}
		}

	}
	tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", lookupStatus.String()))
	return d, lookupStatus, delta, nil
}

func stripConditionalHeaders(h http.Header) {
	h.Del(headers.NameIfMatch)
	h.Del(headers.NameIfUnmodifiedSince)
	h.Del(headers.NameIfNoneMatch)
	h.Del(headers.NameIfModifiedSince)
}

func write(rsc *request.Resources, c cache.Cache, d *HTTPDocument, key string, ttl time.Duration,
	compress bool, span trace.Span) error {
	b, err := d.MarshalMsg(nil)
	if err != nil {
		tl.Error(rsc.Logger, "error marshaling cache document", tl.Pairs{
			"cacheKey": key,
			"detail":   err.Error(),
		})
		return err
	}

	if compress {
		tl.Debug(rsc.Logger, "compressing cache data", tl.Pairs{"cacheKey": key})
		buf := bytes.NewBuffer([]byte{1})
		encoder := brotli.NewWriter(buf)
		encoder.Write(b)
		encoder.Close()
		b = buf.Bytes()
	} else {
		b = append([]byte{0}, b...)
	}

	err = c.Store(key, b, ttl)
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
	return nil
}

// WriteCache writes an HTTPDocument to the cache
func WriteCache(ctx context.Context, c cache.Cache, key string, d *HTTPDocument,
	ttl time.Duration, compressTypes map[string]interface{}) error {

	doCacheChunk := c.Configuration().UseCacheChunking
	chunkFactor := time.Duration(c.Configuration().TimeseriesChunkFactor)
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

		return mc.StoreReference(key, d, ttl)
	}

	if d.timeseries != nil {
		if rsc.CacheMarshaler == nil {
			tl.Error(rsc.Logger, "writecache provided for a timerange, but no marshaler was provided", tl.Pairs{
				"cacheKey": key,
				"detail":   err.Error(),
			})
		} else {
			d.Body, err = rsc.CacheMarshaler(d.timeseries, nil, 0)
			if err != nil {
				tl.Error(rsc.Logger, "error marshaling cache document timeseries", tl.Pairs{
					"cacheKey": key,
					"detail":   err.Error(),
				})
			}
		}
	}

	// Currently this won't call without a timeseries; byte-only chunking TODO
	if doCacheChunk {
		if d.timeseries != nil {
			if d.timeseries.Step() == 0 {
				return errors.New("")
			}
			// Chunk based on document timeseries
			// Determine step and chunk size
			root := d.timeseries
			step := root.Step()
			size := step * chunkFactor
			// Establish a duration start->end such that it is aligned to the epoch along size and contains all of root
			rootExts := root.Extents().Compress(step)
			start, end := rootExts[0].Start.Truncate(size), rootExts[0].End.Truncate(size).Add(size)
			// Iterate through that duration in chunks of size
			for chunkStart := start; chunkStart.Before(end); chunkStart = chunkStart.Add(size) {
				// End chunk one step before next; steps are inclusive
				chunkEnd := chunkStart.Add(size - step)
				chunkExt := timeseries.Extent{
					Start: chunkStart,
					End:   chunkEnd,
				}
				chunkKey := key + chunkExt.String()
				dd := d.CloneEmptyContent()
				dd.timeseries = root.CroppedClone(chunkExt)
				dd.Body, err = rsc.CacheMarshaler(dd.timeseries, nil, 0)
				if err != nil {
					tl.Error(rsc.Logger, "error marshaling cache document chunk", tl.Pairs{
						"cacheKey": chunkKey,
						"detail":   err.Error(),
					})
				}
				err = write(rsc, c, d, chunkKey, ttl, compress, span)
				if err != nil {
					return err
				}
			}
		} else {
			root := d.Body
			size := int64(c.Configuration().ByterangeChunkSize)
			// Get start/end of the document byterange
			rootRanges := d.Ranges
			start, end := rootRanges[0].Start, rootRanges[len(rootRanges)-1].End
			start = start - (start % size)
			end = end + size - (end % size)
			for chunkStart := start; chunkStart < end; chunkStart += size {
				chunkEnd := chunkStart + size - 1
				chunkRange := byterange.Range{
					Start: chunkStart,
					End:   chunkEnd,
				}
				chunkKey := key + ":" + chunkRange.String()
				dd := d.CloneEmptyContent()
				dd.Ranges = byterange.Ranges{chunkRange}
				dd.Body = root[chunkStart-start : chunkEnd-start]
				err = write(rsc, c, dd, chunkKey, ttl, compress, span)
				if err != nil {
					return err
				}
			}
		}
	} else {
		// for non-memory, we have to seralize the document to a byte slice to store
		err = write(rsc, c, d, key, ttl, compress, span)
		if err != nil {
			return err
		}
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
