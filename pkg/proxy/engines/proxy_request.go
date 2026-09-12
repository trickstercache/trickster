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
	"net/http/httptrace"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	tpe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// setupSpanForRequest configures a request span with range detection and returns the modified request
func setupSpanForRequest(req *http.Request, span trace.Span) *http.Request {
	if span != nil {
		setResourceSpanAttributes(request.GetResources(req), span)
		if req.Header != nil {
			if _, ok := req.Header[headers.NameRange]; ok {
				span.SetAttributes(attribute.Bool("isRange", true))
			}
		}
		req = req.WithContext(trace.ContextWithSpan(req.Context(), span))
	}
	return req
}

type proxyRequest struct {
	// client request/response
	*http.Request
	rsc            *request.Resources
	responseWriter io.Writer
	responseBody   []byte
	clientWriter   io.Writer

	// upstream
	upstreamRequest  *http.Request
	upstreamResponse *http.Response
	upstreamReader   io.Reader

	// parallel origin requests
	originRequests  []*http.Request
	originResponses []*http.Response
	originReaders   []io.ReadCloser

	// revalidation
	revalidationRequest  *http.Request
	revalidationResponse *http.Response
	revalidationReader   io.ReadCloser
	revalidation         RevalidationStatus

	// cache state
	cacheDocument *HTTPDocument
	cacheBuffer   *bytes.Buffer
	cacheStatus   status.LookupStatus
	cachingPolicy *CachingPolicy
	key           string
	// primaryKey is the key for the target URI alone. key equals it unless the
	// stored response nominated request fields, in which case key is the
	// secondary key derived from them.
	primaryKey string
	varyNames  []string
	// varyGeneration scopes this request's variant key. It is read from the
	// index on lookup and minted on store when none was found.
	varyGeneration string
	// varyUnmatchable records a Vary the cache cannot compare on, so the
	// response answers this request alone
	varyUnmatchable bool
	// sharedKey is the key this request would have without its Authorization
	// credential. RFC 9111 3.5 lets a shared cache reuse a response to an
	// authorized request only where the origin marked it shareable, and that
	// is the key such a response is stored under.
	sharedKey string
	// omitAuthFromKey makes DeriveCacheKey produce sharedKey rather than the
	// credential-specific key
	omitAuthFromKey bool
	writeToCache    bool

	// range handling
	wantedRanges      byterange.Ranges
	neededRanges      byterange.Ranges
	rangeParts        byterange.MultipartByteRanges
	wantsRanges       bool
	isPartialResponse bool

	// progressive collapse forwarding
	collapsedForwarder ProgressiveCollapseForwarder
	isPCF              bool

	// misc
	mapLock         *sync.Mutex
	started         time.Time
	contentLength   int64
	trueContentType string
	// set when the upstream body ended early or failed mid-copy; an incomplete
	// object must never be written to cache. Atomic because the PCF copy runs
	// on its own goroutine while the caller waits to store.
	bodyTruncated atomic.Bool
}

func cloneRequestWithSpan(r *http.Request) *http.Request {
	if r == nil {
		return nil
	}
	rsc := request.GetResources(r)
	out, err := request.Clone(r)
	if err != nil {
		return nil
	}
	baseCtx := request.RebindUpstreamShortReadCapture(context.Background(), r.Context())
	out = out.WithContext(tctx.WithResources(
		trace.ContextWithSpan(baseCtx,
			trace.SpanFromContext(r.Context())),
		rsc))
	return out
}

// newProxyRequest accepts the original inbound HTTP Request and Response
// and returns a proxyRequest object
func newProxyRequest(r *http.Request, w io.Writer) *proxyRequest {
	pr := &proxyRequest{
		Request:         r,
		rsc:             request.GetResources(r),
		upstreamRequest: cloneRequestWithSpan(r),
		contentLength:   -1,
		responseWriter:  w,
		clientWriter:    w,
		started:         time.Now(),
		mapLock:         &sync.Mutex{},
	}
	return pr
}

func (pr *proxyRequest) Clone() *proxyRequest {
	return &proxyRequest{
		Request:            cloneRequestWithSpan(pr.Request),
		rsc:                pr.rsc,
		upstreamRequest:    cloneRequestWithSpan(pr.upstreamRequest),
		cacheDocument:      pr.cacheDocument,
		key:                pr.key,
		primaryKey:         pr.primaryKey,
		sharedKey:          pr.sharedKey,
		varyNames:          pr.varyNames,
		varyGeneration:     pr.varyGeneration,
		cacheStatus:        pr.cacheStatus,
		writeToCache:       pr.writeToCache,
		wantsRanges:        pr.wantsRanges,
		wantedRanges:       pr.wantedRanges,
		neededRanges:       pr.neededRanges,
		rangeParts:         pr.rangeParts,
		collapsedForwarder: pr.collapsedForwarder,
		// a clone can outlive this request on its own goroutine, and the
		// policy is mutated by response handling under a different lock
		cachingPolicy:     pr.cachingPolicy.Clone(),
		revalidation:      pr.revalidation,
		isPartialResponse: pr.isPartialResponse,
		started:           time.Now(),
		mapLock:           &sync.Mutex{},
	}
}

// Fetch makes an HTTP request to the Origin URL, bypassing the Cache.
// A non-nil error indicates a mid-stream read failure; resp.StatusCode
// still reflects the upstream status, so callers must check both.
func (pr *proxyRequest) Fetch() ([]byte, *http.Response, time.Duration, error) {
	o := pr.rsc.BackendOptions
	pc := pr.rsc.PathConfig

	var handlerName string
	if pc != nil {
		handlerName = pc.HandlerName
	}

	start := time.Now()
	reader, resp, _ := PrepareFetchReader(pr.upstreamRequest)

	var body []byte
	var err error
	if reader != nil {
		if o != nil && o.MaxObjectSizeBytes > 0 {
			// +1 so reaching limit means overflow, not exactly-at-limit.
			limit := int64(o.MaxObjectSizeBytes) + 1
			body, err = io.ReadAll(io.LimitReader(reader, limit))
			if err == nil && int64(len(body)) >= limit {
				err = tpe.ErrUnexpectedUpstreamResponse
				logger.Error("upstream response exceeded MaxObjectSizeBytes",
					logging.Pairs{keys.URL: pr.URL.String(), "max": o.MaxObjectSizeBytes})
			}
		} else {
			body, err = io.ReadAll(reader)
		}
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	if err != nil {
		logger.Error("error reading body from http response",
			logging.Pairs{keys.URL: pr.URL.String(), keys.Detail: err.Error()})
		return body, resp, 0, err
	}

	elapsed := time.Since(start) // includes any time required to decompress the document for deserialization
	if resp != nil {
		pr.rsc.SetUpstream(pr.upstreamRequest.URL.Host, resp.StatusCode, elapsed)
	}

	goWithRecover("proxyRequest.Fetch.logUpstreamRequest", func() {
		logUpstreamRequest(o.Name, o.Provider, handlerName, pr.upstreamRequest.Method,
			pr.upstreamRequest.URL.String(), pr.UserAgent(), resp.StatusCode, len(body), elapsed.Seconds())
	})

	return body, resp, elapsed, nil
}

// relayInterimResponses forwards 1xx responses from the origin to the client as
// they arrive. RFC 9110 15.2 requires a proxy to relay an interim response it
// does not consume itself, which is what makes 103 Early Hints useful: the
// client starts preloading while the origin is still producing the final
// response. The cache has nothing to store for an interim response.
func (pr *proxyRequest) relayInterimResponses(w io.Writer) {
	rw, ok := w.(http.ResponseWriter)
	if !ok || pr.upstreamRequest == nil {
		return
	}
	trace := &httptrace.ClientTrace{
		Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
			h := rw.Header()
			for k, vv := range header {
				h[http.CanonicalHeaderKey(k)] = slices.Clone(vv)
			}
			rw.WriteHeader(code)
			// an interim response's fields belong to it alone, and this header
			// map is reused to build the final response
			clear(h)
			return nil
		},
	}
	pr.upstreamRequest = pr.upstreamRequest.WithContext(
		httptrace.WithClientTrace(pr.upstreamRequest.Context(), trace))
}

func (pr *proxyRequest) prepareRevalidationRequest() {
	pr.revalidation = RevalStatusInProgress
	var err error
	pr.revalidationRequest, err = request.Clone(pr.upstreamRequest)
	if err != nil {
		pr.revalidation = RevalStatusNone
		return
	}
	pr.revalidationRequest = request.SetResources(pr.revalidationRequest, pr.rsc)
	_, span := tspan.NewChildSpan(pr.revalidationRequest.Context(), pr.rsc.Tracer, "FetchRevlidation")
	if span != nil {
		setResourceSpanAttributes(pr.rsc, span)
		pr.revalidationRequest = pr.revalidationRequest.WithContext(trace.ContextWithSpan(pr.revalidationRequest.Context(), span))
		defer span.End()
	}

	if pr.cacheStatus == status.LookupStatusPartialHit {
		var rh string
		d := pr.cacheDocument
		cl := d.ContentLength

		var wr byterange.Ranges
		if len(pr.wantedRanges) > 0 {
			wr = pr.wantedRanges
		} else {
			wr = byterange.Ranges{{Start: 0, End: cl - 1}}
		}

		// revalRanges are the ranges we have in cache that have expired, but the user needs
		// so we revalidate these ranges in parallel with fetching of the uncached ranges
		revalRanges := pr.neededRanges.CalculateDeltas(wr, cl)
		l := len(revalRanges)
		if (l > 1 && pr.rsc.BackendOptions.DearticulateUpstreamRanges) && len(pr.cacheDocument.Ranges) == 1 {
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
	if len(pr.neededRanges) > 0 {
		pr.cachingPolicy.IsFresh = false
		h.Set(headers.NameRange, pr.neededRanges.String())
	}
}

func (pr *proxyRequest) prepareUpstreamRequests() {
	pr.setRangeHeader(pr.upstreamRequest.Header)

	pr.stripConditionalHeaders()
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
	if len(pr.neededRanges) > 0 && pr.rsc.BackendOptions.DearticulateUpstreamRanges {
		for _, r := range pr.neededRanges {
			req, err := request.Clone(pr.upstreamRequest)
			if err != nil {
				continue
			}
			req = request.SetResources(req, pr.rsc.Clone())
			req.Header.Set(headers.NameRange, "bytes="+r.String())
			pr.originRequests = append(pr.originRequests, req)
		}
	} else { // otherwise it will just be a list of one request.
		pr.originRequests = []*http.Request{pr.upstreamRequest}
	}
}

func (pr *proxyRequest) makeSimpleUpstreamRequests(req *http.Request,
	tracer *tracing.Tracer,
) (io.ReadCloser, *http.Response) {
	_, span := tspan.NewChildSpan(req.Context(), tracer, "Fetch")
	req = setupSpanForRequest(req, span)
	if span != nil {
		defer span.End()
	}
	reader, resp, _ := PrepareFetchReader(req)
	if resp != nil {
		setHTTPStatusSpanAttributes(tracer, resp.StatusCode, span)
	}

	return reader, resp
}

func (pr *proxyRequest) makeUpstreamRequests() error {
	// short circuit for when there is only 1 upstream request
	if pr.revalidationRequest == nil && len(pr.originRequests) == 1 {
		pr.originReaders = make([]io.ReadCloser, 1)
		pr.originResponses = make([]*http.Response, 1)
		pr.originReaders[0], pr.originResponses[0] = pr.makeSimpleUpstreamRequests(pr.originRequests[0], pr.rsc.Tracer)
		return nil
	}

	wg := sync.WaitGroup{}

	if pr.revalidationRequest != nil {
		wg.Go(func() {
			req := pr.revalidationRequest
			_, span := tspan.NewChildSpan(req.Context(), pr.rsc.Tracer, "FetchRevalidation")
			pr.revalidationRequest = setupSpanForRequest(req, span)
			if span != nil {
				defer span.End()
			}
			var contentLength int64
			pr.revalidationReader, pr.revalidationResponse, contentLength = PrepareFetchReader(pr.revalidationRequest)
			if pr.revalidationResponse != nil {
				setHTTPStatusSpanAttributes(pr.rsc.Tracer, pr.revalidationResponse.StatusCode, span)
			}
			if pr.revalidationReader == nil {
				logger.Error("revalidation upstream returned no reader",
					logging.Pairs{
						keys.URL:           pr.revalidationRequest.URL.String(),
						keys.ContentLength: contentLength,
					})
			}
		})
	}

	if len(pr.originRequests) > 0 {
		pr.originResponses = make([]*http.Response, len(pr.originRequests))
		pr.originReaders = make([]io.ReadCloser, len(pr.originRequests))
		for i := range pr.originRequests {
			wg.Go(func() {
				req := pr.originRequests[i]
				_, span := tspan.NewChildSpan(req.Context(), pr.rsc.Tracer, "Fetch")
				req = setupSpanForRequest(req, span)
				if span != nil {
					defer span.End()
				}
				var contentLength int64
				pr.originReaders[i], pr.originResponses[i], contentLength = PrepareFetchReader(req)
				if pr.originResponses[i] != nil {
					setHTTPStatusSpanAttributes(pr.rsc.Tracer, pr.originResponses[i].StatusCode, span)
				}
				if pr.originReaders[i] == nil {
					logger.Error("origin upstream returned no reader",
						logging.Pairs{
							keys.URL:           req.URL.String(),
							keys.ContentLength: contentLength,
						})
				}
			})
		}
	}

	wg.Wait()

	return nil
}

// queryCache reads the object for this request. When the stored response
// nominated request fields, the primary key holds only an index naming them
// and the object itself lives under the secondary key those fields derive,
// which costs a second read for varying objects alone.
func (pr *proxyRequest) queryCache(ctx context.Context, c cache.Cache) error {
	d, ls, nr, err := pr.queryKey(ctx, c)
	// nothing stored for this credential; a copy the origin marked shareable
	// may still be held for the target URI itself. This only probes: a miss
	// must leave the request keyed to its own credential, or whatever it goes
	// on to fetch would be stored where anyone could read it.
	if ls != status.LookupStatusHit && pr.sharedKey != "" {
		key, primary, vary, gen := pr.key, pr.primaryKey, pr.varyNames, pr.varyGeneration
		pr.key, pr.primaryKey, pr.varyNames, pr.varyGeneration = pr.sharedKey, pr.sharedKey, nil, ""
		sd, sls, snr, sErr := pr.queryKey(ctx, c)
		if sls == status.LookupStatusHit {
			d, ls, nr, err = sd, sls, snr, sErr
		} else {
			pr.key, pr.primaryKey, pr.varyNames, pr.varyGeneration = key, primary, vary, gen
		}
	}
	pr.cacheDocument, pr.cacheStatus, pr.neededRanges = d, ls, nr

	// RFC 9110 13.1.5: a Range whose If-Range validator no longer matches the
	// stored representation is ignored and the client gets the whole thing.
	// The cache may hold only the requested part, so the lookup is redone
	// without the range to work out what is actually missing -- deciding this
	// after the fetch would serve a fragment as if it were the whole.
	//
	// With nothing stored there is no validator here to compare against, and
	// the request keeps its range state so the origin evaluates If-Range and
	// answers 206 or 200 as it sees fit.
	if pr.wantsRanges && pr.cachingPolicy.HasIfRange &&
		pr.hasStoredRepresentation() && !pr.ifRangeMatchesDocument() {
		pr.wantsRanges = false
		pr.wantedRanges = nil
		d, ls, nr, err = pr.queryKey(ctx, c)
		pr.cacheDocument, pr.cacheStatus, pr.neededRanges = d, ls, nr
	}
	return err
}

// hasConfiguredCredential reports whether a path configuration puts an
// Authorization credential on the request going upstream. Such a request is
// authenticated at the origin even though the client sent nothing, so RFC 9111
// 3.5 governs whether the response may be kept.
func (pr *proxyRequest) hasConfiguredCredential() bool {
	if pr.rsc == nil || pr.rsc.PathConfig == nil {
		return false
	}
	for k, v := range pr.rsc.PathConfig.RequestHeaders {
		name := strings.TrimPrefix(strings.TrimPrefix(k, "+"), "-")
		if http.CanonicalHeaderKey(name) != headers.NameAuthorization {
			continue
		}
		// a deletion, or a value that clears the field, leaves it anonymous
		if strings.HasPrefix(k, "-") || v == "" {
			continue
		}
		return true
	}
	return false
}

// hasStoredRepresentation reports whether the lookup found an object whose
// validator can be compared against. A miss still yields a document -- an
// empty one -- so a nil check alone would treat every cold miss as a
// validator mismatch.
func (pr *proxyRequest) hasStoredRepresentation() bool {
	switch pr.cacheStatus {
	case status.LookupStatusHit, status.LookupStatusPartialHit,
		status.LookupStatusRangeMiss:
		return pr.cacheDocument != nil && pr.cacheDocument.CachingPolicy != nil
	}
	return false
}

// ifRangeMatchesDocument evaluates the client's If-Range against the validator
// of the representation actually in storage, rather than against a policy that
// later merging may have moved on from.
func (pr *proxyRequest) ifRangeMatchesDocument() bool {
	d := pr.cacheDocument
	if d == nil || d.CachingPolicy == nil || pr.cachingPolicy == nil {
		return false
	}
	probe := &CachingPolicy{
		ETag:         d.CachingPolicy.ETag,
		LastModified: d.CachingPolicy.LastModified,
		IfRangeValue: pr.cachingPolicy.IfRangeValue,
	}
	return probe.IfRangeMatches()
}

// queryKey reads pr.key, following the variant index when the stored response
// nominated request fields.
func (pr *proxyRequest) queryKey(ctx context.Context,
	c cache.Cache,
) (*HTTPDocument, status.LookupStatus, byterange.Ranges, error) {
	d, ls, nr, err := QueryCache(ctx, c, pr.key, pr.wantedRanges, nil)
	if err == nil && d != nil && len(d.VaryNames) > 0 {
		pr.varyGeneration = d.VaryGeneration
		pr.setVaryNames(d.VaryNames)
		d, ls, nr, err = QueryCache(ctx, c, pr.key, pr.wantedRanges, nil)
	}
	return d, ls, nr, err
}

// setVaryNames points this request's key at the variant the nominated fields
// select. With no names the object is stored under the primary key itself.
func (pr *proxyRequest) setVaryNames(names []string) {
	pr.varyNames = names
	if len(names) == 0 {
		pr.key = pr.primaryKey
		return
	}
	if pr.varyGeneration == "" {
		pr.varyGeneration = newVaryGeneration()
	}
	pr.key = varySecondaryKey(pr.primaryKey, pr.varyGeneration, names, pr.Header)
}

func (pr *proxyRequest) checkCacheFreshness() bool {
	cp := pr.cachingPolicy
	if pr.cachingPolicy == nil {
		return false
	}
	cp.IsFresh = cp.CurrentAge(time.Now()) < cp.FreshnessLifetime
	return cp.IsFresh
}

func (pr *proxyRequest) parseRequestRanges() bool {
	// handle byte range requests. RFC 9110 14.2 defines range handling for GET
	// alone, so a Range on any other method -- HEAD included -- is ignored
	var out byterange.Ranges
	if _, ok := pr.Header[headers.NameRange]; ok && pr.Method == http.MethodGet {
		out = byterange.ParseRangeHeader(pr.Header.Get(headers.NameRange))
	}
	pr.wantsRanges = len(out) > 0
	pr.wantedRanges = out

	// if the client shouldn't support multipart ranges, force a full range
	if pr.rsc.BackendOptions.MultipartRangesDisabled && len(pr.wantedRanges) > 1 {
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
	headers.SetResultsHeader(pr.upstreamResponse.Header, "ObjectProxyCache", pr.cacheStatus.String(), "", nil, nil)
	pr.setAgeHeader()
	pr.setCacheStatusHeader()
	pr.mapLock.Unlock()
}

func (pr *proxyRequest) setBodyWriter() {
	if !pr.isPCF {
		pr.mapLock.Lock()
		PrepareResponseWriter(pr.responseWriter, pr.upstreamResponse.StatusCode,
			pr.upstreamResponse.Header, pr.trailerNames())
		pr.mapLock.Unlock()
	}

	if pr.writeToCache && pr.cacheBuffer == nil {
		pr.cacheBuffer = &bytes.Buffer{}

		if pr.cachingPolicy.IsClientFresh {
			// don't write response body to the client on a 304 Not Modified
			pr.responseWriter = pr.cacheBuffer
			pr.clientWriter = nil
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
	n, err := io.Copy(pr.responseWriter, pr.upstreamReader)
	if err != nil {
		logger.Error("error copying upstream response body", logging.Pairs{keys.Error: err})
		pr.bodyTruncated.Store(true)
	}
	// Chunked / transparent-gzip transports can return err==nil with n<CL;
	// trigger short-read regardless of err. Responses that carry no content
	// are exempt: their Content-Length describes the body a GET would have
	// returned, so an empty copy is the correct outcome, not a truncation.
	if pr.upstreamResponse != nil && pr.upstreamResponse.ContentLength > 0 &&
		n < pr.upstreamResponse.ContentLength &&
		methods.HasResponseContent(pr.Method, pr.upstreamResponse.StatusCode) {
		pr.bodyTruncated.Store(true)
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		if c := request.GetUpstreamShortReadCapture(pr.upstreamRequest.Context()); c != nil {
			c.Mark()
		}
	}
	// the upstream Content-Length was dropped, so a normal return would end the
	// chunked response cleanly and the client would accept a partial body as
	// whole; break the connection instead. err is local to this copy, so a
	// truncation flagged by the PCF goroutine does not abort this client.
	abortOnCopyError(pr.clientWriter, pr.Request, err)
}

// trailerNames returns the trailer fields the origin declared. They are known
// before the body is read, which is what lets them be announced downstream in
// time to matter.
func (pr *proxyRequest) trailerNames() []string {
	return responseTrailerNames(pr.upstreamResponse)
}

func responseTrailerNames(resp *http.Response) []string {
	if resp == nil || len(resp.Trailer) == 0 {
		return nil
	}
	out := make([]string, 0, len(resp.Trailer))
	for k := range resp.Trailer {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// writeResponseTrailers copies any trailer fields the origin sent after its
// body to the client. RFC 9110 6.5 lets a proxy forward them. Nothing is stored
// for them, so a response served from cache has none to add.
func (pr *proxyRequest) writeResponseTrailers() {
	if pr.upstreamResponse == nil || len(pr.upstreamResponse.Trailer) == 0 {
		return
	}
	rw, ok := pr.clientWriter.(http.ResponseWriter)
	if !ok {
		return
	}
	h := rw.Header()
	for k, vv := range pr.upstreamResponse.Trailer {
		for _, v := range vv {
			// the prefix marks a field the server emits after the body, which
			// is the only way to send one that was not announced up front
			h.Add(http.TrailerPrefix+k, v)
		}
	}
}

func (pr *proxyRequest) determineCacheability() {
	resp := pr.upstreamResponse

	if resp != nil {
		// RFC 9111 4.1: a response nominating request fields is stored under a
		// key derived from them, so it is reused only where they match.
		names, matchable := varyFieldNames(resp.Header)
		pr.varyUnmatchable = !matchable
		if !matchable {
			pr.writeToCache = false
			pr.rsc.CacheClient.Remove(pr.key)
			return
		}
		pr.setVaryNames(names)
	}

	// RFC 9111 3.5: a shared cache may store a response to a request that
	// carried Authorization only where the origin marked it reusable by one.
	// This has to settle before any branch below can authorize storage --
	// a negative-cached error is still an authenticated response.
	switch {
	case pr.sharedKey != "":
		if !pr.cachingPolicy.IsShareable {
			pr.writeToCache = false
			return
		}
		// the origin said this is shareable, so store it under the target URI
		// rather than under the credential that happened to fetch it
		pr.primaryKey = pr.sharedKey
		pr.setVaryNames(pr.varyNames)
	case pr.hasConfiguredCredential() && !pr.cachingPolicy.IsShareable:
		// the request reaches the origin authenticated even though the client
		// sent no credential, and there is no per-credential key isolating the
		// result, so only the origin can authorize keeping it
		pr.writeToCache = false
		return
	}

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

	if pr.rsc.AlternateCacheTTL > 0 {
		pr.writeToCache = true
		pr.cachingPolicy = &CachingPolicy{
			LocalDate:         time.Now(),
			FreshnessLifetime: int(pr.rsc.AlternateCacheTTL.Seconds()),
		}
		return
	}

	if pr.cachingPolicy.NoCache || (!pr.cachingPolicy.CanRevalidate && pr.cachingPolicy.FreshnessLifetime <= 0) {
		pr.writeToCache = false
		pr.rsc.CacheClient.Remove(pr.key)
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

	o := pr.rsc.BackendOptions

	rf := o.RevalidationFactor
	if pr.rsc.AlternateCacheTTL > 0 {
		rf = 1
	}

	d.CachingPolicy = pr.cachingPolicy
	ttl := pr.cachingPolicy.TTL(rf, time.Duration(o.MaxTTL))

	// the primary key holds the index naming the selecting fields, so a later
	// request can learn which secondary key to read before it has the object
	if len(pr.varyNames) > 0 && pr.primaryKey != "" && pr.primaryKey != pr.key {
		idx := &HTTPDocument{VaryNames: pr.varyNames, VaryGeneration: pr.varyGeneration}
		if err := WriteCache(pr.upstreamRequest.Context(), pr.rsc.CacheClient,
			pr.primaryKey, idx, ttl, nil, nil); err != nil {
			logger.Error("error writing vary index to cache",
				logging.Pairs{keys.Key: pr.primaryKey, keys.Detail: err.Error()})
		}
	}

	err := WriteCache(pr.upstreamRequest.Context(), pr.rsc.CacheClient, pr.key, d,
		ttl, o.CompressibleTypes, nil)
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

	// RFC 9110 13.1.5: a Range is honored only while the client's If-Range
	// validator still identifies this representation; otherwise the range is
	// ignored and the whole response is served
	if pr.wantsRanges && pr.cachingPolicy.HasIfRange && !pr.cachingPolicy.IfRangeMatches() {
		pr.wantsRanges = false
		pr.wantedRanges = nil
	}

	if pr.wantsRanges && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) {
		// since the user wants ranges, we have to extract them from what we have already
		if (d == nil || !d.isLoaded) &&
			(pr.cacheStatus == status.LookupStatusPartialHit || pr.cacheStatus == status.LookupStatusKeyMiss ||
				pr.cacheStatus == status.LookupStatusRangeMiss) {
			var b []byte
			if pr.upstreamReader != nil {
				var err error
				b, err = io.ReadAll(pr.upstreamReader)
				if err != nil {
					// Upstream cut off mid-stream — b holds only a truncated
					// prefix. Neither cache it nor present ranges carved from
					// it as a successful response; fail the client instead.
					logger.Error("upstream read error during range extraction",
						logging.Pairs{keys.Error: err})
					pr.writeToCache = false
					pr.bodyTruncated.Store(true)
					abortOnCopyError(pr.clientWriter, pr.Request, err)
				}
			}
			d = DocumentFromHTTPResponse(pr.upstreamResponse, b, pr.cachingPolicy)
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

		// RFC 9110 14.2: when none of the requested ranges overlap the content,
		// answer 416 naming the full length rather than an empty 206
		if _, ok := pr.wantedRanges.Resolve(d.ContentLength); !ok {
			resp.StatusCode = http.StatusRequestedRangeNotSatisfiable
			resp.Header.Set(headers.NameContentRange,
				"bytes */"+strconv.FormatInt(d.ContentLength, 10))
			resp.Header.Del(headers.NameContentLength)
			resp.ContentLength = 0
			pr.responseBody = nil
			pr.upstreamReader = bytes.NewReader(nil)
			return
		}

		resp.StatusCode = http.StatusPartialContent

		if len(d.Ranges) > 0 {
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
		wasRevalidated = pr.revalidationResponse.StatusCode == http.StatusNotModified
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
	wasReconstituted := requestCount > 1

	if wasReconstituted {
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
				wg.Go(func() {
					// oh snap. so we have some partial content to merge in, but the original cache document
					// is now invalid. lets go ahead and reset it.
					b, err := io.ReadAll(resp.Body)
					if err != nil {
						logger.Error("error reading revalidation response body",
							logging.Pairs{keys.Detail: err.Error()})
						return
					}
					appendLock.Lock()
					parts.ParsePartialContentBody(resp, b)
					appendLock.Unlock()
				})
			}
		}

		for i := range pr.originRequests {
			wg.Go(func() {
				r := pr.originRequests[i]
				resp := pr.originResponses[i]

				// only set the upstream response
				appendLock.Lock()
				if pr.upstreamResponse == nil {
					pr.upstreamRequest = r
					pr.upstreamResponse = resp
				}
				appendLock.Unlock()

				if resp.StatusCode == http.StatusPartialContent {
					b, err := io.ReadAll(resp.Body)
					if err != nil {
						logger.Error("error reading origin response body",
							logging.Pairs{keys.Detail: err.Error()})
						return
					}
					appendLock.Lock()
					parts.ParsePartialContentBody(resp, b)
					appendLock.Unlock()
				}
			})
		}

		// all the response bodies are loading in parallel. Wait until they are done.
		wg.Wait()

		resp := pr.upstreamResponse

		parts.Ranges = parts.RangeParts.Ranges()

		var bodyFromParts bool
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
		pr.mapLock.Lock()
		pr.cachingPolicy.Merge(GetResponseCachingPolicy(pr.upstreamResponse.StatusCode,
			pr.rsc.BackendOptions.NegativeCache, pr.upstreamResponse.Header))
		pr.mapLock.Unlock()
	}
}
