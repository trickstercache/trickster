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
	"net/http"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type proxyRequest struct {
	*http.Request
	responseWriter io.Writer
	responseBody   []byte

	upstreamRequest  *http.Request
	upstreamResponse *http.Response
	upstreamReader   io.Reader

	// for parallel requests
	originRequests  []*http.Request
	originResponses []*http.Response
	originReaders   []io.ReadCloser

	revalidationRequest  *http.Request
	revalidationResponse *http.Response
	revalidationReader   io.ReadCloser

	rerunCount int

	cacheDocument *HTTPDocument
	cacheBuffer   *bytes.Buffer
	cacheLock     locks.NamedLock
	mapLock       *sync.Mutex

	key         string
	started     time.Time
	elapsed     time.Duration
	cacheStatus status.LookupStatus

	wantedRanges byterange.Ranges
	neededRanges byterange.Ranges
	rangeParts   byterange.MultipartByteRanges

	contentLength int64
	revalidation  RevalidationStatus

	trueContentType string

	collapsedForwarder ProgressiveCollapseForwarder
	cachingPolicy      *CachingPolicy

	Logger            interface{}
	isPCF             bool
	writeToCache      bool
	hasWriteLock      bool
	hasReadLock       bool
	wasReran          bool
	wantsRanges       bool
	isPartialResponse bool
	wasReconstituted  bool
}

// newProxyRequest accepts the original inbound HTTP Request and Response
// and returns a proxyRequest object
func newProxyRequest(r *http.Request, w io.Writer) *proxyRequest {
	rsc := request.GetResources(r)
	pr := &proxyRequest{
		Request: r,
		upstreamRequest: r.Clone(
			tctx.WithResources(
				trace.ContextWithSpan(context.Background(),
					trace.SpanFromContext(r.Context())),
				rsc)),
		contentLength:  -1,
		responseWriter: w,
		started:        time.Now(),
		mapLock:        &sync.Mutex{},
	}
	if rsc != nil {
		pr.Logger = rsc.Logger
	}
	return pr
}

func (pr *proxyRequest) Clone() *proxyRequest {
	rsc := request.GetResources(pr.Request)
	return &proxyRequest{
		Request: pr.Request.Clone(
			tctx.WithResources(
				trace.ContextWithSpan(context.Background(),
					trace.SpanFromContext(pr.Request.Context())),
				rsc)),
		upstreamRequest: pr.upstreamRequest.Clone(
			tctx.WithResources(
				trace.ContextWithSpan(context.Background(),
					trace.SpanFromContext(pr.upstreamRequest.Context())),
				rsc)),
		Logger:             pr.Logger,
		cacheDocument:      pr.cacheDocument,
		key:                pr.key,
		cacheStatus:        pr.cacheStatus,
		writeToCache:       pr.writeToCache,
		wantsRanges:        pr.wantsRanges,
		wantedRanges:       pr.wantedRanges,
		neededRanges:       pr.neededRanges,
		rangeParts:         pr.rangeParts,
		collapsedForwarder: pr.collapsedForwarder,
		cachingPolicy:      pr.cachingPolicy,
		revalidation:       pr.revalidation,
		isPartialResponse:  pr.isPartialResponse,
		started:            time.Now(),
		mapLock:            &sync.Mutex{},
	}
}

// Fetch makes an HTTP request to the provided Origin URL, bypassing the Cache, and returns the
// response and elapsed time to the caller.
func (pr *proxyRequest) Fetch() ([]byte, *http.Response, time.Duration) {

	rsc := request.GetResources(pr.upstreamRequest)
	o := rsc.BackendOptions
	pc := rsc.PathConfig

	var handlerName string
	if pc != nil {
		handlerName = pc.HandlerName
	}

	start := time.Now()
	reader, resp, _ := PrepareFetchReader(pr.upstreamRequest)

	var body []byte
	var err error
	if reader != nil {
		body, err = io.ReadAll(reader)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	if err != nil {
		tl.Error(pr.Logger, "error reading body from http response",
			tl.Pairs{"url": pr.URL.String(), "detail": err.Error()})
		return []byte{}, resp, 0
	}

	elapsed := time.Since(start) // includes any time required to decompress the document for deserialization

	go logUpstreamRequest(pr.Logger, o.Name, o.Provider, handlerName, pr.upstreamRequest.Method,
		pr.upstreamRequest.URL.String(), pr.UserAgent(), resp.StatusCode, len(body), elapsed.Seconds())

	return body, resp, elapsed
}

func (pr *proxyRequest) prepareRevalidationRequest() {

	rsc := request.GetResources(pr.upstreamRequest)
	pr.revalidation = RevalStatusInProgress
	pr.revalidationRequest = request.SetResources(pr.upstreamRequest.Clone(context.Background()),
		request.GetResources(pr.Request))

	_, span := tspan.NewChildSpan(pr.revalidationRequest.Context(), rsc.Tracer, "FetchRevlidation")
	if span != nil {
		pr.revalidationRequest =
			pr.revalidationRequest.WithContext(trace.ContextWithSpan(pr.revalidationRequest.Context(), span))
		defer span.End()
	}

	if pr.cacheStatus == status.LookupStatusPartialHit {
		var rh string
		d := pr.cacheDocument
		cl := d.ContentLength

		rsc := request.GetResources(pr.Request)
		// revalRanges are the ranges we have in cache that have expired, but the user needs
		// so we revalidate these ranges in parallel with fetching of the uncached ranges

		var wr byterange.Ranges

		if pr.wantedRanges != nil && len(pr.wantedRanges) > 0 {
			wr = pr.wantedRanges
		} else {
			wr = byterange.Ranges{{Start: 0, End: cl}}
		}

		revalRanges := wr.CalculateDelta(pr.neededRanges, cl)
		l := len(revalRanges)
		if (l > 1 && rsc.BackendOptions.DearticulateUpstreamRanges) && len(pr.cacheDocument.Ranges) == 1 {
			rh = pr.cacheDocument.Ranges.String()
		} else if l == 1 {
			rh = revalRanges.String()
		}

		if rh != "" {
			pr.revalidationRequest.Header.Set(headers.NameRange, rh)
		} else {
			pr.revalidationRequest.Header.Del(headers.NameRange)
		}

	}

	if pr.cachingPolicy.ETag != "" {
		pr.revalidationRequest.Header.Set(headers.NameIfNoneMatch, pr.cachingPolicy.ETag)
	}
	if !pr.cachingPolicy.LastModified.IsZero() {
		pr.revalidationRequest.Header.Set(headers.NameIfModifiedSince,
			pr.cachingPolicy.LastModified.UTC().Format(time.RFC1123))
	}

}

func (pr *proxyRequest) setRangeHeader(h http.Header) {
	if pr.neededRanges != nil && len(pr.neededRanges) > 0 {
		pr.cachingPolicy.IsFresh = false
		h.Set(headers.NameRange, pr.neededRanges.String())
	}
}

func (pr *proxyRequest) prepareUpstreamRequests() {

	pr.setRangeHeader(pr.upstreamRequest.Header)

	pr.stripConditionalHeaders()
	rsc := request.GetResources(pr.Request)
	if pr.originRequests == nil {
		var l int
		if pr.neededRanges == nil {
			l = 1
		} else {
			l = len(pr.neededRanges)
		}
		pr.originRequests = make([]*http.Request, 0, l)
	}

	// if we are articulating the origin range requests, break those out here
	if pr.neededRanges != nil && len(pr.neededRanges) > 0 && rsc.BackendOptions.DearticulateUpstreamRanges {
		for _, r := range pr.neededRanges {
			req := request.SetResources(pr.upstreamRequest.Clone(context.Background()), rsc)
			req.Header.Set(headers.NameRange, "bytes="+r.String())
			pr.originRequests = append(pr.originRequests, req)
		}
	} else { // otherwise it will just be a list of one request.
		pr.originRequests = []*http.Request{pr.upstreamRequest}
	}
}

func (pr *proxyRequest) makeUpstreamRequests() error {

	wg := sync.WaitGroup{}

	rsc := request.GetResources(pr.Request)

	if pr.revalidationRequest != nil {
		wg.Add(1)
		go func() {
			req := pr.revalidationRequest
			_, span := tspan.NewChildSpan(req.Context(), rsc.Tracer, "FetchRevalidation")
			if span != nil {
				if req.Header != nil {
					if _, ok := req.Header[headers.NameRange]; ok {
						span.SetAttributes(attribute.Bool("isRange", true))
					}
				}
				pr.revalidationRequest = req.WithContext(trace.ContextWithSpan(req.Context(), span))
				defer span.End()
			}
			pr.revalidationReader, pr.revalidationResponse, _ = PrepareFetchReader(pr.revalidationRequest)
			wg.Done()
		}()
	}

	if pr.originRequests != nil && len(pr.originRequests) > 0 {
		pr.originResponses = make([]*http.Response, len(pr.originRequests))
		pr.originReaders = make([]io.ReadCloser, len(pr.originRequests))
		for i := range pr.originRequests {
			wg.Add(1)
			go func(j int) {
				req := pr.originRequests[j]
				_, span := tspan.NewChildSpan(req.Context(), rsc.Tracer, "Fetch")
				if span != nil {
					if req.Header != nil {
						if _, ok := req.Header[headers.NameRange]; ok {
							span.SetAttributes(attribute.Bool("isRange", true))
						}
					}
					req = req.WithContext(trace.ContextWithSpan(req.Context(), span))
					defer span.End()
				}
				pr.originReaders[j], pr.originResponses[j], _ = PrepareFetchReader(req)
				wg.Done()
			}(i)
		}
	}

	wg.Wait()

	return nil
}

func (pr *proxyRequest) checkCacheFreshness() bool {
	cp := pr.cachingPolicy
	if pr.cachingPolicy == nil {
		return false
	}
	cp.IsFresh = !cp.LocalDate.Add(time.Duration(cp.FreshnessLifetime) * time.Second).Before(time.Now())
	return cp.IsFresh
}

func (pr *proxyRequest) parseRequestRanges() bool {
	// handle byte range requests
	var out byterange.Ranges
	if _, ok := pr.Header[headers.NameRange]; ok {
		out = byterange.ParseRangeHeader(pr.Header.Get(headers.NameRange))
	}
	pr.wantsRanges = len(out) > 0
	pr.wantedRanges = out

	// if the client shouldn't support multipart ranges, force a full range
	rsc := request.GetResources(pr.Request)
	if rsc.BackendOptions.MultipartRangesDisabled && len(pr.wantedRanges) > 1 {
		pr.upstreamRequest.Header.Del(headers.NameRange)
		pr.wantsRanges = false
		pr.wantedRanges = nil
	}

	return pr.wantsRanges
}

func (pr *proxyRequest) stripConditionalHeaders() {
	// don't proxy these up, their scope is only between Trickster and client
	if pr.cachingPolicy != nil && pr.cachingPolicy.IsClientConditional {
		stripConditionalHeaders(pr.upstreamRequest.Header)
	}
}

func (pr *proxyRequest) writeResponseHeader() {
	pr.mapLock.Lock()
	headers.SetResultsHeader(pr.upstreamResponse.Header, "ObjectProxyCache", pr.cacheStatus.String(), "", nil)
	pr.mapLock.Unlock()
}

func (pr *proxyRequest) setBodyWriter() {

	if !pr.isPCF {
		pr.mapLock.Lock()
		PrepareResponseWriter(pr.responseWriter, pr.upstreamResponse.StatusCode, pr.upstreamResponse.Header)
		pr.mapLock.Unlock()
	}

	if pr.writeToCache && pr.cacheBuffer == nil {
		pr.cacheBuffer = &bytes.Buffer{}

		if pr.cachingPolicy.IsClientFresh {
			// don't write response body to the client on a 304 Not Modified
			pr.responseWriter = pr.cacheBuffer
			if pr.upstreamResponse.StatusCode == http.StatusNotModified {
				pr.upstreamResponse.StatusCode = http.StatusOK
			}
		} else {
			// we need to write to both the client over the wire, and the cache buffer
			if pr.responseWriter != nil {
				pr.responseWriter = io.MultiWriter(pr.responseWriter, pr.cacheBuffer)
			} else {
				pr.responseWriter = pr.cacheBuffer
			}
		}
	} else if pr.upstreamResponse.StatusCode == http.StatusNotModified {
		pr.responseWriter = nil
	}
}

func (pr *proxyRequest) writeResponseBody() {
	if pr.upstreamReader == nil || pr.responseWriter == nil {
		return
	}
	io.Copy(pr.responseWriter, pr.upstreamReader)
}

func (pr *proxyRequest) determineCacheability() {

	rsc := request.GetResources(pr.Request)
	resp := pr.upstreamResponse

	if resp != nil && resp.StatusCode >= 400 {
		pr.writeToCache = pr.cachingPolicy.IsNegativeCache
		resp.Header.Del(headers.NameCacheControl)
		resp.Header.Del(headers.NameExpires)
		resp.Header.Del(headers.NameLastModified)
		resp.Header.Del(headers.NameETag)
		resp.Header.Del(headers.NameContentLength)
		return
	}

	if pr.revalidation == RevalStatusLocal {

		tpc := pr.cachingPolicy.Clone()
		tpc.IfModifiedSinceTime = pr.cacheDocument.CachingPolicy.LastModified
		tpc.IfNoneMatchValue = pr.cacheDocument.CachingPolicy.ETag
		tpc.IsClientConditional = true
		tpc.ResolveClientConditionals(pr.cacheStatus)
		if !tpc.IsClientFresh {
			// this this case the range miss becomes a key miss since the old range failed revalidation
			pr.cacheStatus = status.LookupStatusKeyMiss
			pr.cacheDocument = nil
		}
	}

	if rsc.AlternateCacheTTL > 0 {
		pr.writeToCache = true
		pr.cachingPolicy = &CachingPolicy{LocalDate: time.Now(),
			FreshnessLifetime: int(rsc.AlternateCacheTTL.Seconds())}
		return
	}

	if pr.cachingPolicy.NoCache || (!pr.cachingPolicy.CanRevalidate && pr.cachingPolicy.FreshnessLifetime <= 0) {
		pr.writeToCache = false
		rsc.CacheClient.Remove(pr.key)
		// is fresh, and we can cache, can revalidate and the freshness is greater than 0
	} else if !pr.cachingPolicy.IsFresh {
		pr.writeToCache = true
	}
}

func (pr *proxyRequest) store() error {

	if !pr.writeToCache || pr.cacheDocument == nil {
		return nil
	}

	d := pr.cacheDocument

	pr.writeToCache = false // in case store is called again before the object has changed

	d.StoredRangeParts = d.RangeParts.PackableMultipartByteRanges()

	if pr.trueContentType != "" {
		pr.Header.Del(headers.NameContentType)
		d.headerLock.Lock()
		http.Header(d.Headers).Del(headers.NameContentType)
		d.headerLock.Unlock()
		d.ContentType = pr.trueContentType
	}

	rsc := request.GetResources(pr.Request)
	o := rsc.BackendOptions

	rf := o.RevalidationFactor
	if rsc.AlternateCacheTTL > 0 {
		rf = 1
	}

	d.CachingPolicy = pr.cachingPolicy
	err := WriteCache(pr.upstreamRequest.Context(), rsc.CacheClient, pr.key, d,
		pr.cachingPolicy.TTL(rf, o.MaxTTL), o.CompressibleTypes, nil)
	if err != nil {
		return err
	}
	return nil
}

func (pr *proxyRequest) updateContentLength() {

	resp := pr.upstreamResponse
	if resp == nil || pr.responseBody == nil || pr.upstreamResponse.StatusCode > 299 {
		return
	}

	resp.Header.Del(headers.NameContentLength)
	pr.contentLength = int64(len(pr.responseBody))
	resp.ContentLength = pr.contentLength

	pr.upstreamReader = bytes.NewReader(pr.responseBody)
}

func (pr *proxyRequest) prepareResponse() {

	pr.cachingPolicy.ResolveClientConditionals(pr.cacheStatus)

	d := pr.cacheDocument
	resp := pr.upstreamResponse

	// if all of the client conditional headers were satisfied,
	// return 304
	if pr.cachingPolicy.IsClientFresh {
		// 304 on an If-None-Match only applies to GET/HEAD requests
		// this bit will convert an INM-based 304 to a 412 on non-GET/HEAD
		if !methods.IsCacheable(pr.Method) &&
			pr.cachingPolicy.HasIfNoneMatch && !pr.cachingPolicy.IfNoneMatchResult {
			pr.upstreamResponse.StatusCode = http.StatusPreconditionFailed
		} else {
			resp.StatusCode = http.StatusNotModified
		}
		pr.responseBody = []byte{}
		pr.updateContentLength()

		return
	}

	if pr.wantsRanges && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) {

		// since the user wants ranges, we have to extract them from what we have already
		if (d == nil || !d.isLoaded) &&
			(pr.cacheStatus == status.LookupStatusPartialHit || pr.cacheStatus == status.LookupStatusKeyMiss ||
				pr.cacheStatus == status.LookupStatusRangeMiss) {
			var b []byte
			if pr.upstreamReader != nil {
				b, _ = io.ReadAll(pr.upstreamReader)
			}
			d = DocumentFromHTTPResponse(pr.upstreamResponse, b, pr.cachingPolicy, pr.Logger)
			pr.cacheBuffer = bytes.NewBuffer(b)
			if pr.writeToCache {
				d.isLoaded = true
				pr.cacheDocument = d
			}
		}

		// we will need to stitch in a temporary content type header if it is a multipart response,
		// but need the original content type and length if we are also writing to the cache
		pr.trueContentType = resp.Header.Get(headers.NameContentType)
		pr.contentLength = d.ContentLength

		resp.StatusCode = http.StatusPartialContent

		if d.Ranges != nil && len(d.Ranges) > 0 {
			d.LoadRangeParts()
		}
		var h http.Header
		pr.trueContentType = d.ContentType
		h, pr.responseBody = d.RangeParts.ExtractResponseRange(pr.wantedRanges, d.ContentLength, d.ContentType, d.Body)
		headers.Merge(resp.Header, h)
		pr.upstreamReader = bytes.NewReader(pr.responseBody)
	} else if !pr.wantsRanges {
		if resp.StatusCode == http.StatusPartialContent {
			resp.StatusCode = http.StatusOK
		}
		resp.Header.Del(headers.NameContentRange)
		if pr.cacheStatus == status.LookupStatusHit || pr.cacheStatus == status.LookupStatusRevalidated ||
			pr.cacheStatus == status.LookupStatusPartialHit {
			pr.responseBody = d.Body
		}
	}

	pr.updateContentLength()

}

// reconstitute will arrange and process multiple responses so that
// we have just one response for the initial request
func (pr *proxyRequest) reconstituteResponses() {

	hasRevalidationRequest := pr.revalidationRequest != nil

	var wasRevalidated bool
	if hasRevalidationRequest {
		pr.upstreamRequest = pr.revalidationRequest
		pr.upstreamResponse = pr.revalidationResponse
		pr.upstreamReader = pr.upstreamResponse.Body
		wasRevalidated = hasRevalidationRequest && pr.revalidationResponse.StatusCode == http.StatusNotModified
	}

	var originCount int
	if pr.originRequests != nil {
		originCount = len(pr.originRequests)
	}

	var requestCount int
	if hasRevalidationRequest && !wasRevalidated {
		requestCount = originCount + 1
	} else {
		requestCount = originCount
	}

	if requestCount == 0 && !hasRevalidationRequest {
		return
	}
	// if we have a revalidation request, and its response is a 200 OK, or is the only upstream request
	// we will set the primary source response to the revalidation response
	if hasRevalidationRequest &&
		(originCount == 0 || pr.revalidationResponse.StatusCode == http.StatusOK) {
		requestCount = 1
	} else if (!hasRevalidationRequest || wasRevalidated) && originCount == 1 {
		// if we only have a single request, and it's a normal originRequest, set that to the response
		// or if we had a revalidation request that was revalidated, and only one other origin request
		pr.upstreamRequest = pr.originRequests[0]
		pr.upstreamResponse = pr.originResponses[0]
		pr.upstreamReader = pr.originResponses[0].Body
		requestCount = 1
	}

	// if the revalidation request 304'd, we actually don't have to do anything else with it here.
	hasRevalidationRequest = hasRevalidationRequest && !wasRevalidated

	// first pass to handle any potential 200 OKs that should trump all other part-based responses
	if requestCount > 1 {
		for i := range pr.originRequests {
			if pr.originResponses[i].StatusCode == http.StatusOK {
				pr.upstreamRequest = pr.originRequests[i]
				pr.upstreamResponse = pr.originResponses[i]
				pr.upstreamReader = pr.originResponses[i].Body
				pr.mapLock.Lock()
				pr.upstreamResponse.Header.Del(headers.NameContentRange)
				pr.mapLock.Unlock()
				requestCount = 1
				break
			}
		}
	}

	// if all requests were 206, we have to reconstitute to a single multipart body
	pr.wasReconstituted = requestCount > 1

	if pr.wasReconstituted {

		// in this case, we should _not_ use the revalidation request as the base upstreamResponse,
		// since it could have a 304 not modified as the response, instead of a 200 or 206, and this
		// point assumes fresh

		pr.upstreamReader = nil
		pr.upstreamResponse = nil

		appendLock := sync.Mutex{}
		wg := sync.WaitGroup{}
		parts := &HTTPDocument{}

		if hasRevalidationRequest {
			// if one of the parallel requests was a revalidation, it means the part we have in cache has expired.
			// StatusCode will be: 1) 304 Not Modified (the entire cache is still fresh), 2) 206 Partial Content
			// (cache is stale, returned range is the user-requested range that was stale cached, ready to serve
			// fresh from the origin (we already handled the case of a 200 further up)
			resp := pr.revalidationResponse

			// if it's a 304 Not Modified, just don't do anything, since the cached document is good as-is, and
			// the new responses below will returned to be merged with the existing cache. so just check for 206 here.
			if resp.StatusCode == http.StatusPartialContent {
				wg.Add(1)
				go func() {
					// oh snap. so we have some partial content to merge in, but the original cache document
					// is now invalid. lets go ahead and reset it.
					b, _ := io.ReadAll(resp.Body)
					appendLock.Lock()
					parts.ParsePartialContentBody(resp, b, pr.Logger)
					appendLock.Unlock()
					wg.Done()
				}()
			}
		}

		for i := range pr.originRequests {
			wg.Add(1)
			go func(j int) {
				r := pr.originRequests[j]
				resp := pr.originResponses[j]

				if pr.upstreamResponse == nil {
					// only set the upstream response
					appendLock.Lock()
					if pr.upstreamResponse == nil {
						pr.upstreamRequest = r
						pr.upstreamResponse = resp
					}
					appendLock.Unlock()
				}

				if resp.StatusCode == http.StatusPartialContent {
					b, _ := io.ReadAll(resp.Body)
					appendLock.Lock()
					parts.ParsePartialContentBody(resp, b, pr.Logger)
					appendLock.Unlock()
				}
				wg.Done()
			}(i)
		}

		// all the response bodies are loading in parallel. Wait until they are done.
		wg.Wait()

		resp := pr.upstreamResponse

		parts.Ranges = parts.RangeParts.Ranges()

		bodyFromParts := false
		if len(parts.Ranges) > 0 {
			resp.Header.Del(headers.NameContentRange)
			pr.trueContentType = parts.ContentType
			if bodyFromParts = len(parts.Ranges) > 1; !bodyFromParts {
				err := parts.FulfillContentBody()
				if bodyFromParts = err != nil; !bodyFromParts {
					pr.upstreamReader = bytes.NewReader(parts.Body)
					resp.StatusCode = http.StatusOK
					pr.cacheBuffer = bytes.NewBuffer(parts.Body)
				}
			}
		} else {
			pr.upstreamReader = bytes.NewReader(parts.Body)
		}

		if bodyFromParts {
			h, b := parts.RangeParts.Body(parts.ContentLength, parts.ContentType)
			headers.Merge(resp.Header, h)
			pr.upstreamReader = bytes.NewReader(b)
		}
	}

	pr.isPartialResponse = pr.upstreamResponse.StatusCode == http.StatusPartialContent

	// now we merge the caching policy of the new upstreams
	if pr.upstreamResponse.StatusCode != http.StatusNotModified {
		rsc := request.GetResources(pr.Request)
		pr.mapLock.Lock()
		pr.cachingPolicy.Merge(GetResponseCachingPolicy(pr.upstreamResponse.StatusCode,
			rsc.BackendOptions.NegativeCache, pr.upstreamResponse.Header))
		pr.mapLock.Unlock()

	}

}
