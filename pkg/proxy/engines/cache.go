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
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	providerMemory = "memory"

	flagLen = 1
	// minCompressLen is the size under which compressing costs more than it saves
	minCompressLen = 512
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
		if d, ok := ifc.(*HTTPDocument); ok {
			// memory cache returns a shared reference; copy so QueryCache
			// can safely mutate fields like timeseries, isFulfillment, etc.
			qr.d = d.ShallowCopy()
		}
		return qr
	}
	if sc, ok := splitCache(c); ok {
		var meta, body []byte
		meta, body, qr.lookupStatus, qr.err = sc.RetrieveSplit(key)
		if qr.err != nil || qr.lookupStatus != status.LookupStatusHit {
			return qr
		}
		if len(meta) > 0 {
			qr.err = decodeSections(qr.d, meta, body)
			return qr
		}
		// an object that was stored whole
		qr.err = decodeDocument(qr.d, body)
		return qr
	}
	var b []byte
	b, qr.lookupStatus, qr.err = c.Retrieve(key)
	if qr.err != nil ||
		(qr.lookupStatus != status.LookupStatusHit && qr.lookupStatus != status.LookupStatusProxyHit) {
		return qr
	}
	qr.err = decodeDocument(qr.d, b)
	return qr
}

// a document stored whole: a byte that tells whether what follows is compressed, and then the
// document
func decodeDocument(d *HTTPDocument, b []byte) error {
	// check and remove the compression flag
	if len(b) > 0 {
		enc, ok := flagEncoding(b[0])
		if !ok {
			return errSectionsCorrupt
		}
		b = b[1:]
		if enc != providers.Identity {
			var err error
			if b, err = decodeAll(enc, b, 0); err != nil {
				return err
			}
		}
	}
	_, err := d.UnmarshalMsg(b)
	emptyBodyIsNil(d)
	return err
}

// the body is decoded in place, from a buffer the read owns, which leaves an empty body non-nil where
// a copy of it would be nil
func emptyBodyIsNil(d *HTTPDocument) {
	if len(d.Body) == 0 {
		d.Body = nil
	}
}

// QueryCache queries the cache for an HTTPDocument and returns it
func QueryCache(ctx context.Context, c cache.Cache, key string,
	ranges byterange.Ranges, unmarshal timeseries.UnmarshalerFunc,
) (*HTTPDocument, status.LookupStatus, byterange.Ranges, error) {
	return queryCache(ctx, c, key, ranges, unmarshal, false, providers.Identity)
}

// with deferBody, the body of a document that is its whole object is left in the cache for the
// caller to read or release; accept is the encodings a compressed body may be served in
func queryCache(ctx context.Context, c cache.Cache, key string,
	ranges byterange.Ranges, unmarshal timeseries.UnmarshalerFunc, deferBody bool, accept providers.Provider,
) (*HTTPDocument, status.LookupStatus, byterange.Ranges, error) {
	rsc := tc.Resources(ctx).(*request.Resources)

	ctx, span := tspan.NewChildSpan(ctx, rsc.Tracer, "QueryCache")
	if span != nil {
		defer span.End()
	}
	setResourceSpanAttributes(rsc, span)

	var d *HTTPDocument
	var lookupStatus status.LookupStatus

	// Query document
	var qr *queryResult
	if sc, ok := streamCache(c); ok && deferBody && unmarshal == nil && !c.Configuration().UseCacheChunking {
		qr = queryDeferred(sc, key, accept)
	} else {
		qr = queryConcurrent(ctx, c, key)
	}
	if qr.err != nil {
		tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", qr.lookupStatus.String()))
		return qr.d, qr.lookupStatus, ranges, qr.err
	}
	// a memory cache hands back the dataset it holds, which its reader clones before changing it
	if unmarshal != nil && qr.d.timeseries == nil {
		qr.d.timeseries, _ = unmarshal(qr.d.Body, nil)
	}
	d = qr.d
	lookupStatus = qr.lookupStatus

	// If we got a meta document and want to use cache chunking, do so
	opts := rsc.BackendOptions
	if c.Configuration().UseCacheChunking {
		// an object proxy cache document can carry a query for its key but holds no series
		if trq := rsc.TimeRangeQuery; trq != nil && unmarshal != nil {
			// Use timeseries chunk querying
			err := executeTimeseriesChunkQuery(ctx, c, key, d, trq, unmarshal, opts)
			if err != nil {
				return nil, status.LookupStatusKeyMiss, ranges, err
			}
		} else {
			// Use byterange chunk querying
			err := executeByterangeChunkQuery(ctx, c, key, d, ranges, opts)
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

	if sc, ok := splitCache(c); ok {
		return writeSections(sc, key, d, compress, ttl)
	}

	// the document is stored whole, after a byte that tells whether it is compressed
	b = make([]byte, flagLen, flagLen+d.Msgsize())
	b, err = d.MarshalMsg(b)
	if err != nil {
		return err
	}

	// skip compression for small payloads where overhead exceeds benefit
	if compress && len(b)-flagLen >= minCompressLen {
		buf := compressPool.Get().(*bytes.Buffer)
		defer putCompressBuffer(buf)
		buf.WriteByte(encodingFlag(cacheCodec))
		if err = encodeTo(buf, b[flagLen:]); err != nil {
			return err
		}
		// every cache has done with the bytes it is given by the time it returns
		return c.Store(key, buf.Bytes(), ttl)
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
	setResourceSpanAttributes(rsc, span)

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

	opts := rsc.BackendOptions
	if c.Configuration().UseCacheChunking {
		rsc.Lock()
		trq := rsc.TimeRangeQuery
		rsc.Unlock()
		if trq != nil && marshal != nil {
			// Use timeseries chunking
			chunker := NewTimeseriesChunkWriter(c, key, trq, marshal)
			err = executeChunking(ctx, c, key, d, compress, ttl, chunker, opts)
		} else {
			// Use byterange chunking
			chunker := NewByterangeChunkWriter(c, key, d)
			err = executeChunking(ctx, c, key, d, compress, ttl, chunker, opts)
		}
	} else {
		switch {
		case marshal == nil:
		case c.Configuration().Provider == providerMemory:
			// a memory cache holds the dataset itself, which is all a hit reads, so an encoding of it
			// would only take up the cache's memory
			d.Body = nil
		default:
			if d.Body, err = marshal(d.timeseries, nil, 0); err != nil {
				return err
			}
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
