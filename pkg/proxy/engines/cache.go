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

	"github.com/andybalholm/brotli"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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
	var b []byte
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

		b, lookupStatus, err = c.Retrieve(key, true)

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

// WriteCache writes an HTTPDocument to the cache
func WriteCache(ctx context.Context, c cache.Cache, key string, d *HTTPDocument,
	ttl time.Duration, compressTypes map[string]interface{}) error {

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

	// for non-memory, we have to seralize the document to a byte slice to store
	b, err = d.MarshalMsg(nil)
	if err != nil {
		tl.Error(rsc.Logger, "error marshaling cache document", tl.Pairs{
			"cacheKey": key,
			"detail":   err.Error(),
		})
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
