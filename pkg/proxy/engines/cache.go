/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"context"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/cache/status"
	tc "github.com/tricksterproxy/trickster/pkg/proxy/context"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	"github.com/tricksterproxy/trickster/pkg/proxy/ranges/byterange"
	"github.com/tricksterproxy/trickster/pkg/proxy/request"
	tspan "github.com/tricksterproxy/trickster/pkg/tracing/span"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"

	"github.com/golang/snappy"
	"go.opentelemetry.io/otel/api/kv"
)

// QueryCache queries the cache for an HTTPDocument and returns it
func QueryCache(ctx context.Context, c cache.Cache, key string,
	ranges byterange.Ranges) (*HTTPDocument, status.LookupStatus, byterange.Ranges, error) {

	rsc := tc.Resources(ctx).(*request.Resources)

	ctx, span := tspan.NewChildSpan(ctx, rsc.Tracer, "QueryCache")
	if span != nil {
		defer span.End()
	}

	d := &HTTPDocument{}
	var lookupStatus status.LookupStatus
	var bytes []byte
	var err error

	if c.Configuration().CacheType == "memory" {
		mc := c.(cache.MemoryCache)
		var ifc interface{}
		ifc, lookupStatus, err = mc.RetrieveReference(key, true)
		// normalize any cache miss errors to cache.ErrKNF.
		if err != nil && err != cache.ErrKNF && strings.HasSuffix(err.Error(), "not in cache") {
			err = cache.ErrKNF
		}

		if err != nil || (lookupStatus != status.LookupStatusHit) {
			var nr byterange.Ranges
			if lookupStatus == status.LookupStatusKeyMiss && ranges != nil && len(ranges) > 0 {
				nr = ranges
			}

			tspan.SetAttributes(rsc.Tracer, span, kv.String("cache.status", lookupStatus.String()))

			return d, lookupStatus, nr, err
		}

		if ifc != nil {
			d, _ = ifc.(*HTTPDocument)
		} else {
			tspan.SetAttributes(rsc.Tracer, span, kv.String("cache.status", status.LookupStatusKeyMiss.String()))
			return d, status.LookupStatusKeyMiss, ranges, err
		}

	} else {

		bytes, lookupStatus, err = c.Retrieve(key, true)
		// normalize any cache miss errors to cache.ErrKNF.
		if err != nil && err != cache.ErrKNF && strings.HasSuffix(err.Error(), "not in cache") {
			err = cache.ErrKNF
		}

		if err != nil || (lookupStatus != status.LookupStatusHit) {
			var nr byterange.Ranges
			if lookupStatus == status.LookupStatusKeyMiss && ranges != nil && len(ranges) > 0 {
				nr = ranges

			}
			tspan.SetAttributes(rsc.Tracer, span, kv.String("cache.status", lookupStatus.String()))
			return d, lookupStatus, nr, err
		}

		var inflate bool
		// check and remove compression bit
		if len(bytes) > 0 {
			if bytes[0] == 1 {
				inflate = true
			}
			bytes = bytes[1:]
		}

		if inflate {
			rsc.Logger.Debug("decompressing cached data", tl.Pairs{"cacheKey": key})
			b, err := snappy.Decode(nil, bytes)
			if err == nil {
				bytes = b
			}
		}
		_, err = d.UnmarshalMsg(bytes)
		if err != nil {
			tspan.SetAttributes(rsc.Tracer, span, kv.String("cache.status", status.LookupStatusKeyMiss.String()))
			return d, status.LookupStatusKeyMiss, ranges, err
		}

	}

	var delta byterange.Ranges

	// Fulfillment is when we have a range stored, but a subsequent user wants the whole body, so
	// we must inflate the requested range to be the entire object in order to get the correct delta.
	d.isFulfillment = (d.Ranges != nil && len(d.Ranges) > 0) && (ranges == nil || len(ranges) == 0)

	if d.isFulfillment {
		if span != nil {
			span.AddEvent(
				ctx,
				"Cache Fulfillment",
			)
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
	tspan.SetAttributes(rsc.Tracer, span, kv.String("cache.status", lookupStatus.String()))
	return d, lookupStatus, delta, nil
}

func stripConditionalHeaders(h http.Header) {
	h.Del(headers.NameIfMatch)
	h.Del(headers.NameIfUnmodifiedSince)
	h.Del(headers.NameIfNoneMatch)
	h.Del(headers.NameIfModifiedSince)
}

// WriteCache writes an HTTPDocument to the cache
func WriteCache(ctx context.Context, c cache.Cache, key string, d *HTTPDocument,
	ttl time.Duration, compressTypes map[string]bool) error {

	rsc := tc.Resources(ctx).(*request.Resources)

	ctx, span := tspan.NewChildSpan(ctx, rsc.Tracer, "WriteCache")
	if span != nil {
		defer span.End()
	}

	h := http.Header(d.Headers)
	h.Del(headers.NameDate)
	h.Del(headers.NameTransferEncoding)
	h.Del(headers.NameContentRange)
	h.Del(headers.NameTricksterResult)

	var bytes []byte

	var compress bool

	if ce := http.Header(d.Headers).Get(headers.NameContentEncoding); (ce == "" || ce == "identity") &&
		(d.CachingPolicy == nil || !d.CachingPolicy.NoTransform) {
		if mt, _, err := mime.ParseMediaType(d.ContentType); err == nil {
			if _, ok := compressTypes[mt]; ok {
				compress = true
			}
		}

	}

	// for memory cache, don't serialize the document, since we can retrieve it by reference.
	if c.Configuration().CacheType == "memory" {
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

	// for non-memory, we have to seralize the document to a byte slice to store
	bytes, _ = d.MarshalMsg(nil)

	if compress {
		rsc.Logger.Debug("compressing cache data", tl.Pairs{"cacheKey": key})
		bytes = append([]byte{1}, snappy.Encode(nil, bytes)...)
	} else {
		bytes = append([]byte{0}, bytes...)
	}

	err := c.Store(key, bytes, ttl)
	if err != nil {
		if span != nil {
			span.AddEvent(
				ctx,
				"Cache Write Failure",
				kv.String("Error", err.Error()),
			)
		}
		return err
	}
	if span != nil {
		span.AddEvent(
			ctx,
			"Cache Write",
			kv.Int("bytesWritten", len(bytes)),
		)
	}
	return nil

}

// DocumentFromHTTPResponse returns an HTTPDocument from the provided HTTP Response and Body
func DocumentFromHTTPResponse(resp *http.Response, body []byte, cp *CachingPolicy, log *tl.Logger) *HTTPDocument {
	d := &HTTPDocument{}
	d.StatusCode = resp.StatusCode
	d.Status = resp.Status
	d.CachingPolicy = cp
	d.ContentLength = resp.ContentLength

	if resp.Header != nil {
		d.Headers = resp.Header.Clone()
	}

	ct := http.Header(d.Headers).Get(headers.NameContentType)
	if !strings.HasPrefix(ct, headers.ValueMultipartByteRanges) {
		d.ContentType = ct
	}

	if d.StatusCode == http.StatusPartialContent && body != nil && len(body) > 0 {
		d.ParsePartialContentBody(resp, body, log)
		d.FulfillContentBody()
	} else {
		d.SetBody(body)
	}

	return d
}
