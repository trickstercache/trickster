/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package engines

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/cache/status"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/request"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/tracing"
	"github.com/Comcast/trickster/pkg/locks"
	"go.opentelemetry.io/otel/api/core"
)

func handleCacheKeyHit(pr *proxyRequest) error {

	d := pr.cacheDocument

	if d != nil && d.StoredRangeParts != nil && len(d.StoredRangeParts) > 0 {
		d.LoadRangeParts()
	}

	ok, err := confirmTrueCacheHit(pr)
	if ok {
		return handleTrueCacheHit(pr)
	}

	// if not ok, then confirmTrueCacheHit already redirected the
	// request to the correct handle; we just return its result here.
	return err
}

func handleCachePartialHit(pr *proxyRequest) error {

	// if we already have a revalidation in progress, then we've already confirmed it's not
	// a true cache hit on the existing cached ranges. otherwise we need to verify first.
	if pr.revalidation == RevalStatusNone {
		if ok, err := confirmTrueCacheHit(pr); !ok {
			// if not ok, then confirmTrueCacheHit has already redirected the
			// request to the correct handle; we just return its result here.
			return err
		}
	}

	pr.prepareUpstreamRequests()

	handleUpstreamTransactions(pr)

	d := pr.cacheDocument
	resp := pr.upstreamResponse
	if pr.isPartialResponse {
		b, _ := ioutil.ReadAll(pr.upstreamReader)
		d2 := &HTTPDocument{}

		d2.ParsePartialContentBody(resp, b)
		d.LoadRangeParts()

		d2.Ranges = d2.RangeParts.Ranges()

		d.RangeParts.Merge(d2.RangeParts)
		d.Ranges = d.RangeParts.Ranges()
		d.StoredRangeParts = d.RangeParts.PackableMultipartByteRanges()
		err := d.FulfillContentBody()

		if err == nil {
			pr.upstreamResponse.Body = ioutil.NopCloser(bytes.NewBuffer(d.Body))
			pr.upstreamResponse.Header.Set(headers.NameContentType, d.ContentType)
			pr.upstreamReader = pr.upstreamResponse.Body
		} else {
			h, b := d.RangeParts.ExtractResponseRange(pr.wantedRanges, d.ContentLength, d.ContentType, nil)

			headers.Merge(pr.upstreamResponse.Header, h)
			pr.upstreamReader = ioutil.NopCloser(bytes.NewBuffer(b))
		}

	} else {
		if d != nil {
			d.RangeParts = nil
			d.Ranges = nil
			d.StoredRangeParts = nil
			d.StatusCode = resp.StatusCode
			http.Header(d.Headers).Del(headers.NameContentRange)
		}
	}

	pr.store()

	return handleResponse(pr)

}

func confirmTrueCacheHit(pr *proxyRequest) (bool, error) {

	pr.cachingPolicy.Merge(pr.cacheDocument.CachingPolicy)

	if (!pr.checkCacheFreshness()) && (pr.cachingPolicy.CanRevalidate) {
		return false, handleCacheRevalidation(pr)
	}
	if !pr.cachingPolicy.IsFresh {
		pr.cacheStatus = status.LookupStatusKeyMiss
		return false, handleCacheKeyMiss(pr)
	}

	return true, nil
}

func handleCacheRangeMiss(pr *proxyRequest) error {
	// ultimately we can optimize range miss functionality compared to partial hit
	// (e.g., if the object has expired, no need to revalidate on a range miss,
	//  but must dump old parts if the new range has a different etag or last-modified)
	// for now we'll just treat it like partial hit, but it's still observed as a range miss
	return handleCachePartialHit(pr)
}

func handleCacheRevalidation(pr *proxyRequest) error {

	rsc := request.GetResources(pr.Request)
	oc := rsc.OriginConfig

	ctx, span := tracing.NewChildSpan(pr.Request.Context(), oc.TracingConfig.Tracer, "CacheRevalidation")
	defer func() {
		reval := revalidationStatusValues[pr.revalidation]
		span.AddEvent(
			ctx,
			"Complete",
			core.Key("Result").String(reval),
		)
		defer span.End()
	}()

	pr.revalidation = RevalStatusInProgress

	// if it's a range miss, we don't need to remote revalidate.
	// range miss means we have a cache key for this object, but
	// not any of the byte ranges that the user has requested.
	// since the needed range is 100% uncached, we can use
	// the last-modified/etag of the new response to perform
	// an internal revalidation of the pre-existing partial content.
	if pr.cacheStatus == status.LookupStatusRangeMiss {
		pr.revalidation = RevalStatusLocal
		return handleCacheRangeMiss(pr)
	}

	// all other cache statuses that got us to this point means
	// we have to perform a remote revalidation; queue it up
	pr.prepareRevalidationRequest()

	if pr.cacheStatus == status.LookupStatusPartialHit {
		// this will handle all upstream calls including prepared re-evaluation
		return handleCachePartialHit(pr)
	}

	// all remaining cache statuses indicate there are no other upstream
	// requests than this revalidation. so lets make the call
	handleUpstreamTransactions(pr)

	return handleCacheRevalidationResponse(pr)

}

func handleCacheRevalidationResponse(pr *proxyRequest) error {

	if pr.upstreamResponse.StatusCode == http.StatusNotModified {
		pr.revalidation = RevalStatusOK
		pr.cachingPolicy.IsFresh = true
		pr.cachingPolicy.LocalDate = time.Now()
		pr.cacheStatus = status.LookupStatusRevalidated
		pr.upstreamResponse.StatusCode = pr.cacheDocument.StatusCode
		pr.writeToCache = true
		pr.store()
		pr.upstreamReader = bytes.NewBuffer(pr.cacheDocument.Body)
		return handleTrueCacheHit(pr)
	}

	pr.revalidation = RevalStatusFailed
	pr.cacheStatus = status.LookupStatusKeyMiss
	return handleAllWrites(pr)
}

func handleTrueCacheHit(pr *proxyRequest) error {

	d := pr.cacheDocument
	if d == nil {
		return errors.New("nil cacheDocument")
	}

	if pr.cachingPolicy.IsNegativeCache {
		pr.cacheStatus = status.LookupStatusNegativeCacheHit
	}

	pr.upstreamResponse = &http.Response{StatusCode: d.StatusCode, Request: pr.Request, Header: d.Headers}
	if pr.wantsRanges {
		h, b := d.RangeParts.ExtractResponseRange(pr.wantedRanges, d.ContentLength, d.ContentType, d.Body)
		headers.Merge(pr.upstreamResponse.Header, h)
		pr.upstreamReader = bytes.NewBuffer(b)
	} else {
		pr.upstreamReader = bytes.NewBuffer(d.Body)
	}

	return handleResponse(pr)

}

func handleCacheKeyMiss(pr *proxyRequest) error {
	pr.prepareUpstreamRequests()
	handleUpstreamTransactions(pr)
	return handleAllWrites(pr)
}

func handleUpstreamTransactions(pr *proxyRequest) error {
	pr.makeUpstreamRequests()
	pr.reconstituteResponses()
	pr.determineCacheability()
	return nil
}

func handleAllWrites(pr *proxyRequest) error {
	handleResponse(pr)
	if pr.writeToCache {
		if pr.cacheDocument == nil || !pr.cacheDocument.isLoaded {
			d := DocumentFromHTTPResponse(pr.upstreamResponse, nil, pr.cachingPolicy)
			pr.cacheDocument = d
			if pr.isPartialResponse {
				d.ParsePartialContentBody(pr.upstreamResponse, pr.cacheBuffer.Bytes())
			} else {
				d.Body = pr.cacheBuffer.Bytes()
			}
		}
		pr.store()
	}
	return nil
}

func handleResponse(pr *proxyRequest) error {
	pr.prepareResponse()
	pr.writeResponseHeader()
	pr.setBodyWriter() // what about partial hit? it does not set this
	pr.writeResponseBody()
	return nil
}

// Cache Status Response Handler Mappings
var cacheResponseHandlers = map[status.LookupStatus]func(*proxyRequest) error{
	status.LookupStatusHit:        handleCacheKeyHit,
	status.LookupStatusPartialHit: handleCachePartialHit,
	status.LookupStatusKeyMiss:    handleCacheKeyMiss,
	status.LookupStatusRangeMiss:  handleCacheRangeMiss,
}

func fetchViaObjectProxyCache(w io.Writer, r *http.Request) (*http.Response, status.LookupStatus) {

	rsc := request.GetResources(r)
	oc := rsc.OriginConfig
	cc := rsc.CacheClient

	pr := newProxyRequest(r, w)
	pr.parseRequestRanges()

	pr.cachingPolicy = GetRequestCachingPolicy(pr.Header)

	pr.key = oc.CacheKeyPrefix + "." + pr.DeriveCacheKey(nil, "")
	pcfResult, pcfExists := Reqs.Load(pr.key)
	if (!pr.wantsRanges && pcfExists) || pr.cachingPolicy.NoCache {
		if pr.cachingPolicy.NoCache {
			locks.Acquire(pr.key)
			cc.Remove(pr.key)
			locks.Release(pr.key)
		}
		return nil, status.LookupStatusProxyOnly
	}

	if pcfExists {
		pr.collapsedForwarder = pcfResult.(ProgressiveCollapseForwarder)
	}

	pr.cachingPolicy.ParseClientConditionals()

	if !rsc.NoLock {
		locks.Acquire(pr.key)
	}

	var err error
	pr.cacheDocument, pr.cacheStatus, pr.neededRanges, err = QueryCache(pr.Context(), cc, pr.key, pr.wantedRanges)
	if err == nil || err == cache.ErrKNF {
		if f, ok := cacheResponseHandlers[pr.cacheStatus]; ok {
			f(pr)
		} else {
			log.Warn("unhandled cache lookup response", log.Pairs{"lookupStatus": pr.cacheStatus})
			return nil, status.LookupStatusProxyOnly
		}
	} else {
		log.Error("cache lookup error", log.Pairs{"detail": err.Error()})
		pr.cacheDocument = nil
		pr.cacheStatus = status.LookupStatusKeyMiss
		handleCacheKeyMiss(pr)
	}

	if !rsc.NoLock {
		locks.Release(pr.key)
	}

	// newProxyRequest sets pr.started to time.Now()
	pr.elapsed = time.Since(pr.started)
	el := float64(pr.elapsed.Milliseconds()) / 1000.0
	recordOPCResult(r, pr.cacheStatus, pr.upstreamResponse.StatusCode, r.URL.Path, el, pr.upstreamResponse.Header)

	return pr.upstreamResponse, pr.cacheStatus
}

// ObjectProxyCacheRequest provides a Basic HTTP Reverse Proxy/Cache
func ObjectProxyCacheRequest(w http.ResponseWriter, r *http.Request) {
	_, cacheStatus := fetchViaObjectProxyCache(w, r)
	if cacheStatus == status.LookupStatusProxyOnly {
		DoProxy(w, r)
	}
}

// FetchViaObjectProxyCache Fetches an object from Cache or Origin (on miss), writes the object to the cache, and returns the object to the caller
func FetchViaObjectProxyCache(r *http.Request) ([]byte, *http.Response, bool) {
	w := bytes.NewBuffer(nil)
	resp, cacheStatus := fetchViaObjectProxyCache(w, r)
	if cacheStatus == status.LookupStatusProxyOnly {
		resp = DoProxy(w, r)
	}
	return w.Bytes(), resp, cacheStatus == status.LookupStatusHit
}

func recordOPCResult(r *http.Request, cacheStatus status.LookupStatus, httpStatus int, path string, elapsed float64, header http.Header) {
	recordResults(r, "ObjectProxyCache", cacheStatus, httpStatus, path, "", elapsed, nil, header)
}
