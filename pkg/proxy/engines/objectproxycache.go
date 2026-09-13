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
	stderrors "errors"
	"io"
	"net/http"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/forwarding"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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

	if err := handleUpstreamTransactions(pr); err != nil {
		return err
	}

	d := pr.cacheDocument
	resp := pr.upstreamResponse
	if pr.isPartialResponse {
		b, err := io.ReadAll(pr.upstreamReader)
		if err != nil {
			return err
		}
		d2 := &HTTPDocument{}

		d2.ParsePartialContentBody(resp, b)
		d.LoadRangeParts()

		d2.Ranges = d2.RangeParts.Ranges()

		d.RangeParts.Merge(d2.RangeParts)
		d.Ranges = d.RangeParts.Ranges()
		d.StoredRangeParts = d.RangeParts.PackableMultipartByteRanges()
		err = d.FulfillContentBody()

		if err == nil {
			pr.upstreamResponse.Body = io.NopCloser(bytes.NewReader(d.Body))
			pr.mapLock.Lock()
			pr.upstreamResponse.Header.Set(headers.NameContentType, d.ContentType)
			pr.mapLock.Unlock()
			pr.upstreamReader = pr.upstreamResponse.Body
		} else {
			h, b := d.RangeParts.ExtractResponseRange(pr.wantedRanges, d.ContentLength, d.ContentType, nil)
			pr.mapLock.Lock()
			headers.Merge(pr.upstreamResponse.Header, h)
			pr.mapLock.Unlock()
			pr.upstreamReader = io.NopCloser(bytes.NewReader(b))
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

	if err := pr.store(); err != nil {
		return err
	}

	return handleResponse(pr)
}

func confirmTrueCacheHit(pr *proxyRequest) (bool, error) {
	pr.cachingPolicy.Merge(pr.cacheDocument.CachingPolicy)

	fresh := pr.checkCacheFreshness()
	// RFC 9111 5.2.1: the client's own directives can rule out a response the
	// cache would otherwise have reused
	if fresh && !pr.cachingPolicy.clientAcceptsStored(time.Now()) {
		fresh = false
		pr.cachingPolicy.IsFresh = false
	}
	if fresh {
		return true, nil
	}
	// RFC 5861 3: inside the stale-while-revalidate window the client gets the
	// stored response now and the refresh happens behind it
	if pr.cachingPolicy.CanServeStaleWhileRevalidate(time.Now()) {
		return false, handleStaleWhileRevalidate(pr)
	}
	// only-if-cached forbids going to the origin, even to revalidate
	if pr.cachingPolicy.OnlyIfCached() {
		return false, handleOnlyIfCachedMiss(pr)
	}
	if pr.cachingPolicy.CanRevalidate {
		return false, handleCacheRevalidation(pr)
	}
	pr.cacheStatus = status.LookupStatusKeyMiss
	return false, handleCacheKeyMiss(pr)
}

// handleOnlyIfCachedMiss answers a request that refused a forwarded response
// but could not be satisfied from storage. RFC 9111 5.2.1.7 specifies 504.
func handleOnlyIfCachedMiss(pr *proxyRequest) error {
	pr.cacheStatus = status.LookupStatusKeyMiss
	pr.writeToCache = false
	pr.upstreamResponse = &http.Response{
		StatusCode: http.StatusGatewayTimeout,
		Request:    pr.Request,
		Header:     make(http.Header),
	}
	pr.upstreamReader = bytes.NewReader(nil)
	return handleResponse(pr)
}

func handleCacheRangeMiss(pr *proxyRequest) error {
	// ultimately we can optimize range miss functionality compared to partial hit
	// (e.g., if the object has expired, no need to revalidate on a range miss,
	//  but must dump old parts if the new range has a different etag or last-modified)
	// for now we'll just treat it like partial hit, but it's still observed as a range miss
	return handleCachePartialHit(pr)
}

func handleCacheRevalidation(pr *proxyRequest) error {
	_, span := tspan.NewChildSpan(pr.Request.Context(), pr.rsc.Tracer, "CacheRevalidation")
	if span != nil {
		defer func() {
			reval := revalidationStatusValues[pr.revalidation]
			span.AddEvent(
				"Complete",
				trace.EventOption(trace.WithAttributes(attribute.String("result", reval))),
			)
			span.End()
		}()
	}
	setResourceSpanAttributes(pr.rsc, span)

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
	if err := handleUpstreamTransactions(pr); err != nil {
		return err
	}

	return handleCacheRevalidationResponse(pr)
}

func handleCacheRevalidationResponse(pr *proxyRequest) error {
	if pr.upstreamResponse.StatusCode == http.StatusNotModified {
		pr.revalidation = RevalStatusOK
		pr.cachingPolicy.IsFresh = true
		pr.cachingPolicy.LocalDate = time.Now()
		// the origin just confirmed this representation, so it is new again
		pr.cachingPolicy.InitialAge = 0
		pr.cacheStatus = status.LookupStatusRevalidated
		pr.upstreamResponse.StatusCode = pr.cacheDocument.StatusCode
		pr.writeToCache = true
		if err := pr.store(); err != nil {
			return err
		}
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

	pr.upstreamResponse = &http.Response{
		StatusCode: d.StatusCode, Request: pr.Request,
		Header: d.SafeHeaderClone(),
	}
	if pr.wantsRanges {
		h, b := d.RangeParts.ExtractResponseRange(pr.wantedRanges, d.ContentLength, d.ContentType, d.Body)
		headers.Merge(pr.upstreamResponse.Header, h)
		pr.upstreamReader = bytes.NewReader(b)
	} else {
		pr.upstreamReader = bytes.NewReader(d.Body)
	}

	ce := pr.upstreamResponse.Header.Get(headers.NameContentEncoding)
	if ep := profile.FromContext(pr.Request.Context()); ep != nil {
		ep.ContentEncoding = ce
	}

	return handleResponse(pr)
}

func handleCacheKeyMiss(pr *proxyRequest) error {
	pc := pr.rsc.PathConfig

	// if we're using PCF, handle that separately
	if methods.IsCacheable(pr.Method) && !pr.wantsRanges && pc != nil &&
		pc.CollapsedForwardingType == forwarding.CFTypeProgressive {
		if err := handlePCF(pr); !stderrors.Is(err, errors.ErrPCFContentLength) {
			return err
		}
		// PCF not applicable (content too large), fall through to normal fetch
	}

	pr.prepareUpstreamRequests()
	if err := handleUpstreamTransactions(pr); err != nil {
		return err
	}
	return handleAllWrites(pr)
}

// serveOPCResult writes a singleflight-shared result to a waiting request's client.
// the waiter has no cacheDocument or upstream reader state, only the pre-built opcResult.
func serveOPCResult(pr *proxyRequest, result *opcResult) error {
	pr.upstreamResponse = &http.Response{
		StatusCode: result.statusCode,
		Request:    pr.Request,
		Header:     result.headers.Clone(),
	}
	if status.IsSuccessful(result.cacheStatus) {
		pr.cacheStatus = status.LookupStatusProxyHit
	} else {
		pr.cacheStatus = status.LookupStatusProxyError
	}
	pr.writeResponseHeader()
	pr.mapLock.Lock()
	PrepareResponseWriter(pr.responseWriter, result.statusCode, pr.upstreamResponse.Header, nil)
	pr.mapLock.Unlock()
	_, err := io.Copy(pr.responseWriter, bytes.NewReader(result.body))
	return err
}

// maxVariantRetries bounds how many times a waiter will re-run to find a
// result it may use before fetching on its own. A shifting Vary should settle
// in one hop; the bound only keeps a pathological origin from spinning.
const maxVariantRetries = 3

// runOPC performs this request's cache lookup and handling under singleflight,
// returning the shared result and whether this request is the one that ran it.
func (pr *proxyRequest) runOPC(sfKey string, cc cache.Cache) (*opcResult, bool, error) {
	var isExecutor bool
	val, err, _ := opcGroup.Do(sfKey, func() (any, error) {
		isExecutor = true
		return pr.executeOPC(cc), nil
	})
	if err != nil {
		return nil, isExecutor, err
	}
	return val.(*opcResult), isExecutor, nil
}

// executeOPC reads the cache and runs the handler for the resulting status,
// writing this request's own response and returning what a waiter would need.
func (pr *proxyRequest) executeOPC(cc cache.Cache) *opcResult {
	// wrap the response writer to capture body writes for the opcResult
	capture := &sfResponseCapture{inner: pr.responseWriter}
	pr.responseWriter = capture

	// buildErrorResult constructs an opcResult for error responses.
	buildErrorResult := func() *opcResult {
		sc := http.StatusBadGateway
		var h http.Header
		if pr.upstreamResponse != nil {
			sc = pr.upstreamResponse.StatusCode
			h = pr.upstreamResponse.Header.Clone()
		}
		if h == nil {
			h = http.Header{}
		}
		return &opcResult{
			statusCode:  sc,
			headers:     h,
			body:        append([]byte(nil), capture.buf.Bytes()...),
			elapsed:     float64(time.Since(pr.started).Milliseconds()) / 1000.0,
			cacheStatus: status.LookupStatusProxyError,
		}
	}

	err := pr.queryCache(pr.upstreamRequest.Context(), cc)
	// nothing stored and the client will not accept a forwarded response
	if pr.cachingPolicy.OnlyIfCached() && pr.cacheStatus != status.LookupStatusHit {
		if fErr := handleOnlyIfCachedMiss(pr); fErr != nil {
			return buildErrorResult()
		}
	} else if err == nil || stderrors.Is(err, cache.ErrKNF) {
		f := cacheResponseHandler(pr.cacheStatus)
		if f == nil {
			logger.Warn("unhandled cache lookup response",
				logging.Pairs{"lookupStatus": pr.cacheStatus})
			return &opcResult{cacheStatus: status.LookupStatusProxyOnly}
		}
		if fErr := f(pr); fErr != nil {
			return buildErrorResult()
		}
	} else {
		logger.Error("cache lookup error",
			logging.Pairs{keys.Detail: err.Error()})
		pr.cacheDocument = nil
		pr.cacheStatus = status.LookupStatusKeyMiss
		if fErr := handleCacheKeyMiss(pr); fErr != nil {
			return buildErrorResult()
		}
	}

	// the shared result must be what went on the wire, not what went into
	// storage: those differ whenever a range was extracted from a complete
	// stored object, or a conditional hit sent a bodyless 304
	body := capture.buf.Bytes()
	// deep-copy body to avoid aliasing with memory cache (stores by reference)
	return &opcResult{
		statusCode:      pr.upstreamResponse.StatusCode,
		headers:         pr.upstreamResponse.Header.Clone(),
		body:            append([]byte(nil), body...),
		elapsed:         float64(time.Since(pr.started).Milliseconds()) / 1000.0,
		cacheStatus:     pr.cacheStatus,
		varyNames:       pr.varyNames,
		varyGeneration:  pr.varyGeneration,
		varyKey:         pr.key,
		varyUnmatchable: pr.varyUnmatchable,
	}
}

func handleUpstreamTransactions(pr *proxyRequest) error {
	if err := pr.makeUpstreamRequests(); err != nil {
		return err
	}
	pr.reconstituteResponses()
	pr.determineCacheability()
	return nil
}

func handlePCF(pr *proxyRequest) error {
	o := pr.rsc.BackendOptions

	if o.MaxObjectSizeBytes <= 0 {
		return errors.ErrPCFContentLength
	}

	pr.isPCF = true
	pcfResult, pcfExists := reqs.Load(pr.key)
	// a PCF session is in progress for this URL, join this client to it.
	if pcfExists {
		pcf := pcfResult.(ProgressiveCollapseForwarder)
		pr.upstreamResponse = pcf.GetResp()
		pr.mapLock.Lock()
		pr.responseWriter = PrepareResponseWriter(pr.responseWriter, pr.upstreamResponse.StatusCode,
			pr.upstreamResponse.Header, nil)
		pr.mapLock.Unlock()
		if err := pcf.AddClient(pr.responseWriter); err != nil {
			abortOnCopyError(pr.responseWriter, pr.Request, err)
			return err
		}
		return nil
	}

	ctx, span := tspan.NewChildSpan(pr.upstreamRequest.Context(), pr.rsc.Tracer, "FetchObject")
	if span != nil {
		span.SetAttributes(attribute.Bool("isPCF", true))
		defer span.End()
	}
	setResourceSpanAttributes(pr.rsc, span)
	pr.upstreamRequest = pr.upstreamRequest.WithContext(ctx)

	reader, resp, contentLength := PrepareFetchReader(pr.upstreamRequest)
	pr.upstreamResponse = resp

	// decide before committing anything downstream, so a response that cannot be
	// collapsed is served from this fetch rather than re-requested
	var pcf ProgressiveCollapseForwarder
	if (contentLength < 0 || (contentLength > 0 && contentLength < int64(o.MaxObjectSizeBytes))) &&
		collapseEligible(pr.Request, resp.StatusCode, resp.Header, pr.rsc.PathConfig) {
		pcf = NewPCF(resp, contentLength, int64(o.MaxObjectSizeBytes))
	}
	if pcf == nil {
		return serveUncollapsed(pr, resp, reader)
	}

	pr.writeResponseHeader()
	pr.responseWriter = PrepareResponseWriter(pr.responseWriter, resp.StatusCode, resp.Header, nil)
	{
		actual, loaded := reqs.LoadOrStore(pr.key, pcf)
		if loaded {
			// Another goroutine created a PCF session first; join it instead.
			resp.Body.Close()
			existingPCF := actual.(ProgressiveCollapseForwarder)
			pr.upstreamResponse = existingPCF.GetResp()
			pr.mapLock.Lock()
			pr.responseWriter = PrepareResponseWriter(pr.responseWriter, pr.upstreamResponse.StatusCode,
				pr.upstreamResponse.Header, nil)
			pr.mapLock.Unlock()
			if err := existingPCF.AddClient(pr.responseWriter); err != nil {
				abortOnCopyError(pr.responseWriter, pr.Request, err)
				return err
			}
			return nil
		}

		pr.cachingPolicy.Merge(GetResponseCachingPolicy(pr.upstreamResponse.StatusCode,
			pr.rsc.BackendOptions.NegativeCache, pr.upstreamResponse.Header))
		pr.determineCacheability()

		goWithRecover("opc.pcf.copy", func() {
			defer func() {
				if reader != nil {
					reader.Close()
				}
			}()
			defer reqs.Delete(pr.key)
			var dest io.Writer = pcf
			if pr.writeToCache {
				pr.cacheBuffer = &bytes.Buffer{}
				dest = io.MultiWriter(pcf, pr.cacheBuffer)
			}
			n, err := io.Copy(dest, reader)
			switch {
			case err != nil:
				logger.Error("pcf upstream copy failed",
					logging.Pairs{keys.Key: pr.key, keys.Detail: err.Error()})
				pr.bodyTruncated.Store(true)
				pcf.CloseWithError(err)
			case pr.Method != http.MethodHead && contentLength > 0 && n < contentLength:
				logger.Error("pcf upstream returned short body",
					logging.Pairs{keys.Key: pr.key})
				pr.bodyTruncated.Store(true)
				pcf.CloseWithError(io.ErrUnexpectedEOF)
			default:
				pcf.Close()
			}
		})

		if err := pcf.AddClient(pr.responseWriter); err != nil {
			abortOnCopyError(pr.responseWriter, pr.Request, err)
			return err
		}

		return handleAllWrites(pr)
	}
}

// serveUncollapsed serves a response that reached the collapse path but cannot
// be shared, reusing the fetch already in hand. Falling back to an independent
// fetch would double the origin request and write the second body under this
// response's already-committed status.
func serveUncollapsed(pr *proxyRequest, resp *http.Response, reader io.ReadCloser) error {
	pr.isPCF = false
	pr.upstreamResponse = resp
	pr.upstreamReader = reader
	if reader != nil {
		defer reader.Close()
	}
	pr.cachingPolicy.Merge(GetResponseCachingPolicy(resp.StatusCode,
		pr.rsc.BackendOptions.NegativeCache, resp.Header))
	pr.determineCacheability()
	return handleAllWrites(pr)
}

func handleAllWrites(pr *proxyRequest) error {
	// RFC 5861 4: an origin that failed does not invalidate what is stored, so
	// a response still inside its stale-if-error window stands in for it
	if serveStaleOnError(pr) {
		pr.writeToCache = false
		pr.cacheStatus = status.LookupStatusHit
		return handleTrueCacheHit(pr)
	}
	if err := handleResponse(pr); err != nil {
		return err
	}
	if pr.writeToCache {
		// an object that ended early is not the object the origin advertised;
		// storing it would serve a truncated body to every later requester
		if pr.bodyTruncated.Load() {
			logger.Warn("skipping cache write for truncated upstream response",
				logging.Pairs{keys.Key: pr.key, keys.URL: pr.URL.String()})
			return nil
		}
		if pr.cacheDocument == nil || !pr.cacheDocument.isLoaded {
			d := DocumentFromHTTPResponse(pr.upstreamResponse, nil, pr.cachingPolicy)
			pr.cacheDocument = d
			if pr.isPartialResponse {
				d.ParsePartialContentBody(pr.upstreamResponse, pr.cacheBuffer.Bytes())
			} else {
				d.Body = pr.cacheBuffer.Bytes()
			}
		}
		if err := pr.store(); err != nil {
			return err
		}
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
	pr.writeResponseTrailers()
	return nil
}

func cacheResponseHandler(s status.LookupStatus) func(*proxyRequest) error {
	switch s {
	case status.LookupStatusHit, status.LookupStatusProxyHit:
		return handleCacheKeyHit
	case status.LookupStatusPartialHit:
		return handleCachePartialHit
	case status.LookupStatusKeyMiss:
		return handleCacheKeyMiss
	case status.LookupStatusRangeMiss:
		return handleCacheRangeMiss
	}
	return nil
}

func fetchViaObjectProxyCache(w io.Writer, r *http.Request) (*http.Response, status.LookupStatus) {
	rsc := request.GetResources(r)
	o := rsc.BackendOptions
	if o == nil || o.ProxyOnly {
		return nil, status.LookupStatusProxyOnly
	}

	cc := rsc.CacheClient

	pr := newProxyRequest(r, w)
	pr.relayInterimResponses(w)

	_, span := tspan.NewChildSpan(r.Context(), rsc.Tracer, "ObjectProxyCacheRequest")
	if span != nil {
		pr.upstreamRequest = pr.upstreamRequest.WithContext(trace.ContextWithSpan(pr.upstreamRequest.Context(), span))
		defer span.End()
	}
	setResourceSpanAttributes(rsc, span)

	pr.parseRequestRanges()

	pr.cachingPolicy = GetRequestCachingPolicy(pr.Header)

	pr.key = ComposeCacheKey(o.Name, o.CacheKeyPrefix, "opc", pr.DeriveCacheKey(""))
	pr.primaryKey = pr.key
	// an authorized request also has a credential-free key, which is where a
	// response the origin marked shareable is stored and found
	if methods.IsCacheable(pr.Method) && pr.Header.Get(headers.NameAuthorization) != "" {
		pr.omitAuthFromKey = true
		pr.sharedKey = ComposeCacheKey(o.Name, o.CacheKeyPrefix, "opc", pr.DeriveCacheKey(""))
		pr.omitAuthFromKey = false
	}

	// if a PCF entry exists, or the client requested no-cache for this object, proxy out to it
	pcfResult, pcfExists := reqs.Load(pr.key)
	pr.isPCF = !methods.HasBody(pr.Method) && pcfExists && !pr.wantsRanges

	if pr.isPCF || pr.cachingPolicy.NoCache {
		if pr.cachingPolicy.NoCache {
			cc.Remove(pr.key)
			tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", status.LookupStatusProxyOnly.String()))
			return nil, status.LookupStatusProxyOnly
		}
		pcf := pcfResult.(ProgressiveCollapseForwarder)
		pr.upstreamResponse = pcf.GetResp()
		pr.mapLock.Lock()
		writer := PrepareResponseWriter(w, pr.upstreamResponse.StatusCode, pr.upstreamResponse.Header, nil)
		pr.mapLock.Unlock()
		if err := pcf.AddClient(writer); err != nil {
			tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", status.LookupStatusError.String()))
			abortOnCopyError(writer, r, err)
			return nil, status.LookupStatusError
		}
		tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", status.LookupStatusProxyHit.String()))
		setHTTPStatusSpanAttributes(rsc.Tracer, pr.upstreamResponse.StatusCode, span)
		return pr.upstreamResponse, status.LookupStatusProxyHit
	}

	pr.cachingPolicy.ParseClientConditionals()

	// deduplicate cache lookup + handler work per cache key via singleflight.
	// the executor writes its own response and returns an opcResult for waiters.
	sfKey := pr.key
	if pr.wantsRanges {
		sfKey += "|" + pr.wantedRanges.String()
	}
	result, isExecutor, sfErr := pr.runOPC(sfKey, cc)
	// RFC 9111 4.1: a leader's response answers only requests whose own fields
	// select the same variant. A waiter that selects differently -- which a
	// cold miss cannot know in advance -- redoes the work under its own
	// variant key rather than being handed a body chosen for someone else.
	// The check repeats, because the execution it then joins may nominate a
	// different set of fields again.
	for attempt := range maxVariantRetries {
		if sfErr != nil || isExecutor || result.suitableFor(pr) {
			break
		}
		vKey := result.variantKeyFor(pr)
		// a response that can match no other request, or a selection that
		// keeps shifting, has to be fetched for this request alone
		if result.varyUnmatchable || attempt+1 >= maxVariantRetries {
			vKey = sfKey + "|solo|" + newVaryGeneration()
		}
		if pr.wantsRanges {
			vKey += "|" + pr.wantedRanges.String()
		}
		result, isExecutor, sfErr = pr.runOPC(vKey, cc)
	}

	if sfErr != nil {
		tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", status.LookupStatusError.String()))
		return nil, status.LookupStatusError
	}
	if result.cacheStatus == status.LookupStatusProxyOnly {
		tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", status.LookupStatusProxyOnly.String()))
		return nil, status.LookupStatusProxyOnly
	}

	// only serve the shared result for waiters; the executor already wrote its response
	if !isExecutor {
		if err := serveOPCResult(pr, result); err != nil {
			tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", status.LookupStatusError.String()))
			return nil, status.LookupStatusError
		}
	}

	// ensure pr.upstreamResponse is set for metrics recording;
	// may be nil if the handler errored before contacting upstream
	if pr.upstreamResponse == nil {
		pr.upstreamResponse = &http.Response{
			StatusCode: result.statusCode,
			Request:    pr.Request,
			Header:     result.headers.Clone(),
		}
		pr.cacheStatus = result.cacheStatus
	}

	recordOPCResult(pr, pr.cacheStatus, pr.upstreamResponse.StatusCode, r.URL.Path, result.elapsed, pr.upstreamResponse.Header)
	tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", pr.cacheStatus.String()))
	setHTTPStatusSpanAttributes(rsc.Tracer, pr.upstreamResponse.StatusCode, span)

	return pr.upstreamResponse, pr.cacheStatus
}

// ObjectProxyCacheRequest provides a Basic HTTP Reverse Proxy/Cache
func ObjectProxyCacheRequest(w http.ResponseWriter, r *http.Request) {
	resp, cacheStatus := fetchViaObjectProxyCache(w, r)
	if cacheStatus == status.LookupStatusProxyOnly {
		DoProxy(w, r, true)
	} else if rsc := request.GetResources(r); resp != nil && rsc != nil &&
		(rsc.MergeFunc != nil || rsc.TSTransformer != nil) {
		rsc.Response = resp
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

	if resp != nil {
		if resp.Body != nil {
			resp.Body.Close()
		}
		resp.Body = io.NopCloser(w)
	}

	return w.Bytes(), resp, cacheStatus == status.LookupStatusHit
}

func recordOPCResult(pr *proxyRequest, cacheStatus status.LookupStatus, httpStatus int,
	path string, elapsed float64, header http.Header,
) {
	pr.mapLock.Lock()
	recordResults(pr.Request, "ObjectProxyCache", cacheStatus, httpStatus, path, "", elapsed, nil, nil, header)
	pr.mapLock.Unlock()
}
