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
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/cache/status"
	"github.com/tricksterproxy/trickster/pkg/proxy/errors"
	"github.com/tricksterproxy/trickster/pkg/proxy/forwarding"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	"github.com/tricksterproxy/trickster/pkg/proxy/methods"
	"github.com/tricksterproxy/trickster/pkg/proxy/request"
	tspan "github.com/tricksterproxy/trickster/pkg/tracing/span"
	"github.com/tricksterproxy/trickster/pkg/util/log"

	"go.opentelemetry.io/otel/api/trace"
	"go.opentelemetry.io/otel/label"
)

func handleCacheKeyHit(pr *proxyRequest) error {

	d := pr.cacheDocument

	if d != nil && d.StoredRangeParts != nil && len(d.StoredRangeParts) > 0 {
		d.LoadRangeParts()
	}

	ok, err := confirmTrueCacheHit(pr)
	if ok {
		if pr.hasReadLock {
			pr.cacheLock.RRelease()
			pr.hasReadLock = false
		}
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

	b1, b2 := upgradeLock(pr)
	if b1 && !b2 && pr.rerunCount < 3 {
		rerunRequest(pr)
		return nil
	}

	pr.prepareUpstreamRequests()

	handleUpstreamTransactions(pr)

	d := pr.cacheDocument
	resp := pr.upstreamResponse
	if pr.isPartialResponse {
		b, _ := ioutil.ReadAll(pr.upstreamReader)
		d2 := &HTTPDocument{}

		d2.ParsePartialContentBody(resp, b, pr.Logger)
		d.LoadRangeParts()

		d2.Ranges = d2.RangeParts.Ranges()

		d.RangeParts.Merge(d2.RangeParts)
		d.Ranges = d.RangeParts.Ranges()
		d.StoredRangeParts = d.RangeParts.PackableMultipartByteRanges()
		err := d.FulfillContentBody()

		if err == nil {
			pr.upstreamResponse.Body = ioutil.NopCloser(bytes.NewReader(d.Body))
			pr.upstreamResponse.Header.Set(headers.NameContentType, d.ContentType)
			pr.upstreamReader = pr.upstreamResponse.Body
		} else {
			h, b := d.RangeParts.ExtractResponseRange(pr.wantedRanges, d.ContentLength, d.ContentType, nil)

			headers.Merge(pr.upstreamResponse.Header, h)
			pr.upstreamReader = ioutil.NopCloser(bytes.NewReader(b))
		}

	} else if d != nil {
		d.RangeParts = nil
		d.Ranges = nil
		d.StoredRangeParts = nil
		d.StatusCode = resp.StatusCode
		d.headerLock.Lock()
		http.Header(d.Headers).Del(headers.NameContentRange)
		d.headerLock.Unlock()
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

	b1, b2 := upgradeLock(pr)
	if b1 && !b2 {
		rerunRequest(pr)
		return nil
	}

	rsc := request.GetResources(pr.Request)

	ctx, span := tspan.NewChildSpan(pr.Request.Context(), rsc.Tracer, "CacheRevalidation")
	if span != nil {
		defer func() {
			reval := revalidationStatusValues[pr.revalidation]
			span.AddEvent(
				ctx,
				"Complete",
				label.String("result", reval),
			)
			span.End()
		}()
	}

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
		pr.upstreamReader = bytes.NewReader(pr.cacheDocument.Body)
		return handleTrueCacheHit(pr)
	}

	pr.revalidation = RevalStatusFailed
	pr.cacheStatus = status.LookupStatusKeyMiss
	return handleAllWrites(pr)
}

func handleTrueCacheHit(pr *proxyRequest) error {

	d := pr.cacheDocument
	if d == nil {
		return errors.ErrNilCacheDocument
	}

	if pr.cachingPolicy.IsNegativeCache {
		pr.cacheStatus = status.LookupStatusNegativeCacheHit
	}

	pr.upstreamResponse = &http.Response{StatusCode: d.StatusCode, Request: pr.Request,
		Header: d.SafeHeaderClone()}
	if pr.wantsRanges {
		h, b := d.RangeParts.ExtractResponseRange(pr.wantedRanges, d.ContentLength, d.ContentType, d.Body)
		headers.Merge(pr.upstreamResponse.Header, h)
		pr.upstreamReader = bytes.NewReader(b)
	} else {
		pr.upstreamReader = bytes.NewReader(d.Body)
	}

	return handleResponse(pr)

}

func handleCacheKeyMiss(pr *proxyRequest) error {

	b1, b2 := upgradeLock(pr)
	if b1 && !b2 {
		rerunRequest(pr)
		return nil
	}

	rsc := request.GetResources(pr.Request)
	pc := rsc.PathConfig

	// if a we're using PCF, handle that separately
	if !methods.HasBody(pr.Method) && !pr.wantsRanges && pc != nil &&
		pc.CollapsedForwardingType == forwarding.CFTypeProgressive {
		if err := handlePCF(pr); err != errors.ErrPCFContentLength {
			// if err is nil, or something else, we'll proceed.
			return err
		}
	}

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

func handlePCF(pr *proxyRequest) error {

	rsc := request.GetResources(pr.Request)
	oc := rsc.OriginConfig

	pr.isPCF = true
	pcfResult, pcfExists := reqs.Load(pr.key)
	// a PCF session is in progress for this URL, join this client to it.
	if pcfExists {
		pr.cacheLock.Release()
		pr.hasWriteLock = false
		pcf := pcfResult.(ProgressiveCollapseForwarder)
		pr.upstreamResponse = pcf.GetResp()
		pr.responseWriter = PrepareResponseWriter(pr.responseWriter, pr.upstreamResponse.StatusCode,
			pr.upstreamResponse.Header)
		pcf.AddClient(pr.responseWriter)
		return nil
	}

	ctx, span := tspan.NewChildSpan(pr.upstreamRequest.Context(), rsc.Tracer, "FetchObject")
	if span != nil {
		span.SetAttributes(label.Bool("isPCF", true))
		defer span.End()
	}
	pr.upstreamRequest = pr.upstreamRequest.WithContext(ctx)

	reader, resp, contentLength := PrepareFetchReader(pr.upstreamRequest)
	pr.upstreamResponse = resp

	pr.writeResponseHeader()
	pr.responseWriter = PrepareResponseWriter(pr.responseWriter, resp.StatusCode, resp.Header)
	// Check if we know the content length and if it is less than our max object size.
	if contentLength > 0 && contentLength < int64(oc.MaxObjectSizeBytes) {
		pcf := NewPCF(resp, contentLength)
		reqs.Store(pr.key, pcf)
		// Blocks until server completes

		pr.cachingPolicy.Merge(GetResponseCachingPolicy(pr.upstreamResponse.StatusCode,
			rsc.OriginConfig.NegativeCache, pr.upstreamResponse.Header))
		pr.determineCacheability()

		go func() {
			var dest io.Writer = pcf
			if pr.writeToCache {
				pr.cacheBuffer = &bytes.Buffer{}
				dest = io.MultiWriter(pcf, pr.cacheBuffer)
			}
			io.Copy(dest, reader)
			pcf.Close()
			reqs.Delete(pr.key)
		}()

		pcf.AddClient(pr.responseWriter)

		return handleAllWrites(pr)
	}
	return errors.ErrPCFContentLength
}

func handleAllWrites(pr *proxyRequest) error {
	handleResponse(pr)
	if pr.writeToCache {
		if pr.cacheDocument == nil || !pr.cacheDocument.isLoaded {
			d := DocumentFromHTTPResponse(pr.upstreamResponse, nil, pr.cachingPolicy, pr.Logger)
			pr.cacheDocument = d
			if pr.isPartialResponse {
				d.ParsePartialContentBody(pr.upstreamResponse, pr.cacheBuffer.Bytes(), pr.Logger)
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
	if !pr.isPCF {
		pr.writeResponseHeader()
	}
	pr.setBodyWriter() // what about partial hit? it does not set this
	pr.writeResponseBody()
	return nil
}

var cacheResponseHandlers map[status.LookupStatus]func(*proxyRequest) error

func init() {
	// Cache Status Response Handler Mappings
	cacheResponseHandlers = map[status.LookupStatus]func(*proxyRequest) error{
		status.LookupStatusHit:        handleCacheKeyHit,
		status.LookupStatusPartialHit: handleCachePartialHit,
		status.LookupStatusKeyMiss:    handleCacheKeyMiss,
		status.LookupStatusRangeMiss:  handleCacheRangeMiss,
	}
}

func fetchViaObjectProxyCache(w io.Writer, r *http.Request) (*http.Response, status.LookupStatus) {

	rsc := request.GetResources(r)
	oc := rsc.OriginConfig
	cc := rsc.CacheClient

	pr := newProxyRequest(r, w)

	_, span := tspan.NewChildSpan(r.Context(), rsc.Tracer, "ObjectProxyCacheRequest")
	if span != nil {
		pr.upstreamRequest = pr.upstreamRequest.WithContext(trace.ContextWithSpan(pr.upstreamRequest.Context(), span))
		defer span.End()
	}

	pr.parseRequestRanges()

	pr.cachingPolicy = GetRequestCachingPolicy(pr.Header)

	pr.key = oc.CacheKeyPrefix + ".opc." + pr.DeriveCacheKey(nil, "")

	// if a PCF entry exists, or the client requested no-cache for this object, proxy out to it
	pcfResult, pcfExists := reqs.Load(pr.key)
	pr.isPCF = !methods.HasBody(pr.Method) && pcfExists && !pr.wantsRanges

	if pr.isPCF || pr.cachingPolicy.NoCache {
		if pr.cachingPolicy.NoCache {
			cc.Remove(pr.key)
			return nil, status.LookupStatusProxyOnly
		}
		pcf := pcfResult.(ProgressiveCollapseForwarder)
		pr.upstreamResponse = pcf.GetResp()
		writer := PrepareResponseWriter(w, pr.upstreamResponse.StatusCode, pr.upstreamResponse.Header)
		pcf.AddClient(writer)
		return pr.upstreamResponse, status.LookupStatusProxyHit
	}

	pr.cachingPolicy.ParseClientConditionals()

	if !rsc.NoLock {
		pr.cacheLock, _ = cc.Locker().RAcquire(pr.key)
		pr.hasReadLock = true
	}

	var err error
	pr.cacheDocument, pr.cacheStatus, pr.neededRanges, err =
		QueryCache(pr.upstreamRequest.Context(), cc, pr.key, pr.wantedRanges)
	if err == nil || err == cache.ErrKNF {
		if f, ok := cacheResponseHandlers[pr.cacheStatus]; ok {
			f(pr)
		} else {
			pr.Logger.Warn("unhandled cache lookup response", log.Pairs{"lookupStatus": pr.cacheStatus})
			return nil, status.LookupStatusProxyOnly
		}
	} else {
		pr.Logger.Error("cache lookup error", log.Pairs{"detail": err.Error()})
		pr.cacheDocument = nil
		pr.cacheStatus = status.LookupStatusKeyMiss
		handleCacheKeyMiss(pr)
	}

	if pr.hasWriteLock {
		pr.cacheLock.Release()
	} else if pr.hasReadLock {
		pr.cacheLock.RRelease()
	}

	if pr.wasReran {
		return nil, status.LookupStatusRevalidated
	}

	// newProxyRequest sets pr.started to time.Now()
	pr.elapsed = time.Since(pr.started)
	el := float64(pr.elapsed.Milliseconds()) / 1000.0
	recordOPCResult(pr, pr.cacheStatus, pr.upstreamResponse.StatusCode, r.URL.Path, el, pr.upstreamResponse.Header)

	return pr.upstreamResponse, pr.cacheStatus
}

// ObjectProxyCacheRequest provides a Basic HTTP Reverse Proxy/Cache
func ObjectProxyCacheRequest(w http.ResponseWriter, r *http.Request) {
	_, cacheStatus := fetchViaObjectProxyCache(w, r)
	if cacheStatus == status.LookupStatusProxyOnly {
		DoProxy(w, r, true)
	}
}

// FetchViaObjectProxyCache Fetches an object from Cache or Origin (on miss),
// writes the object to the cache, and returns the object to the caller
func FetchViaObjectProxyCache(r *http.Request) ([]byte, *http.Response, bool) {
	w := bytes.NewBuffer(nil)
	resp, cacheStatus := fetchViaObjectProxyCache(w, r)
	if cacheStatus == status.LookupStatusProxyOnly {
		resp = DoProxy(w, r, false)
	}

	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}

	return w.Bytes(), resp, cacheStatus == status.LookupStatusHit
}

func recordOPCResult(pr *proxyRequest, cacheStatus status.LookupStatus, httpStatus int,
	path string, elapsed float64, header http.Header) {
	pr.mapLock.Lock()
	recordResults(pr.Request, "ObjectProxyCache", cacheStatus, httpStatus, path, "", elapsed, nil, header)
	pr.mapLock.Unlock()
}

func upgradeLock(pr *proxyRequest) (bool, bool) {
	if pr.hasReadLock && !pr.hasWriteLock {
		wasFirst := pr.cacheLock.Upgrade()
		pr.hasReadLock = false
		pr.hasWriteLock = true
		if wasFirst {
			return true, true
		}
		return true, false
	}
	return false, false
}

func rerunRequest(pr *proxyRequest) {
	pr.wasReran = true
	if w, ok := pr.responseWriter.(http.ResponseWriter); ok {
		if pr.hasWriteLock {
			pr.cacheLock.Release()
			pr.hasWriteLock = false
		}
		ObjectProxyCacheRequest(w, pr.Request)
	}
}
