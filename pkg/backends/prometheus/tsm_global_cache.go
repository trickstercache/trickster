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

package prometheus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tt "github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	pathopts "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

const (
	// globalOriginHost names the in-process origin that evaluates a global-mode ALB's cache misses
	globalOriginHost   = "tsm-global.trickstercache.org"
	globalOriginScheme = "http"
	// globalCacheVersion is part of every cached result's key, so a change to what entries hold is a miss
	globalCacheVersion = "2"
	// maxJoinAttempts bounds how often a request retries after joining an evaluation another request's
	// client canceled
	maxJoinAttempts = 3
	// cacheKeyAllParams is the cache_key_params value keying every parameter
	cacheKeyAllParams = "*"
)

// globalTenantHeaders are the tenant headers of the products that serve raw series, which partition
// cached global results so no tenant reads another's
var globalTenantHeaders = []string{"X-Scope-OrgID", "THANOS-TENANT"}

// a cached global result is a whole evaluation, keyed by all that decides one: its range or time and
// limit, and how it ends, so requests sharing one agree on it
var (
	globalInstantKeyParams = []string{upQuery, upTime, upLimit, partialResponseParam, timeoutParam}
	globalRangeKeyParams   = []string{upQuery, upStart, upEnd, upStep, upLimit, partialResponseParam, timeoutParam}
)

// cachedGlobal keeps each complete evaluation whole: a result's samples depend on its whole range, as
// deduplication follows replicas from its first sample
type cachedGlobal struct {
	e      *globalEvaluator
	client *Client
	// base and prefix are the internal client's options and key prefix, before a pool's namespace
	base   *bo.Options
	prefix string
	// instant and rng are the query routes, keyed as configured on the ALB's own paths too
	instant, rng *pathopts.Options
	routes       atomic.Pointer[globalRoutes]
	// dispatch answers a prepared request from the cache, or the evaluation the cache runs for it
	dispatch func(w http.ResponseWriter, r *http.Request, isRange bool)
	// owners maps the resources of each request being answered to its owner, as the cache fetches from
	// the evaluator without the client's cancellation and pool snapshot
	owners sync.Map
}

// globalOwner is the client request an evaluation runs for: its context, which cancels the evaluation,
// and the pool snapshot whose namespace keys the result
type globalOwner struct {
	ctx context.Context
	req backends.TSMGlobalRequest
}

// globalRoutes are the options and query routes for one pool namespace
type globalRoutes struct {
	namespace    string
	options      *bo.Options
	instant, rng *pathopts.Options
	// variants holds each route merged with the identity of an ALB path a request arrived on
	variants sync.Map
}

// globalVariant names a query route merged with the identity of the ALB path that matched
type globalVariant struct {
	matched *pathopts.Options
	isRange bool
}

// newCachedGlobal builds a Prometheus client over the ALB's cache whose origin is e itself, reached in
// process, so what the cache lacks is what e evaluates
func (e *globalEvaluator) newCachedGlobal(results backends.TSMGlobalCache) (*cachedGlobal, error) {
	o := results.Backend.Clone()
	configured := o.Paths
	o.Provider = providers.Prometheus
	o.ALBOptions = nil
	o.HealthCheck = nil
	o.OriginURL = globalOriginScheme + "://" + globalOriginHost
	o.Scheme, o.Host, o.PathPrefix = globalOriginScheme, globalOriginHost, ""
	o.Prometheus = &po.Options{InstantRound: tt.Duration(po.DefaultInstantRound)}
	// a failed evaluation is mostly a member's outage or a deadline, which no later request should get,
	// so no failure is kept, whatever negative cache the ALB names
	o.NegativeCacheName, o.NegativeCache = "", nil
	o.HTTPClient = &http.Client{Transport: inProcessTransport{handler: http.HandlerFunc(e.serveOrigin)}}
	client, err := NewClientWithHooks(o.Name, o, nil, results.Cache, Hooks{CacheKeyHeaders: globalTenantHeaders})
	if err != nil {
		return nil, err
	}
	o.Paths = client.DefaultPathConfigs(o)
	cg := &cachedGlobal{
		e: e, client: client, base: o, prefix: e.cacheKeyPrefix(),
		instant: globalQueryRoute(o.Paths, APIPath+mnQuery, globalInstantKeyParams, configured),
		rng:     globalQueryRoute(o.Paths, APIPath+mnQueryRange, globalRangeKeyParams, configured),
	}
	cg.dispatch = cg.dispatchCached
	return cg, nil
}

// dispatchCached answers r through the object cache, which never assembles a range from cached parts;
// an instant's time is rounded as any Prometheus backend rounds it
func (cg *cachedGlobal) dispatchCached(w http.ResponseWriter, r *http.Request, isRange bool) {
	if isRange {
		r.URL = urls.BuildUpstreamURL(r, cg.client.BaseUpstreamURL())
		engines.ObjectProxyCacheRequest(w, r)
		return
	}
	cg.client.QueryHandler(w, r)
}

// globalQueryRoute returns path's route, keyed by keyParams and the ALB's configured paths' identity; it
// sets no Cache-Control, as only the evaluator knows when a result is partial
func globalQueryRoute(defaults pathopts.List, path string, keyParams []string,
	configured pathopts.List,
) *pathopts.Options {
	pc := defaults.Match(http.MethodGet, path).Clone()
	delete(pc.ResponseHeaders, headers.NameCacheControl)
	pc.CacheKeyParams = slices.Clone(keyParams)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if c := configured.Match(method, path); c != nil {
			mergeIdentity(pc, c)
		}
	}
	return pc
}

// mergeIdentity adds to pc the cache key headers, parameters and form fields c sets
func mergeIdentity(pc, c *pathopts.Options) {
	pc.CacheKeyHeaders = appendHeaders(pc.CacheKeyHeaders, c.CacheKeyHeaders)
	if slices.Contains(c.CacheKeyParams, cacheKeyAllParams) {
		pc.CacheKeyParams = []string{cacheKeyAllParams}
	} else if !slices.Contains(pc.CacheKeyParams, cacheKeyAllParams) {
		pc.CacheKeyParams = appendNew(pc.CacheKeyParams, c.CacheKeyParams)
	}
	pc.CacheKeyFormFields = appendNew(pc.CacheKeyFormFields, c.CacheKeyFormFields)
}

// keysIdentity reports whether a path sets any cache identity a query route would merge
func keysIdentity(pc *pathopts.Options) bool {
	return pc != nil && (len(pc.CacheKeyHeaders) > 0 || len(pc.CacheKeyParams) > 0 || len(pc.CacheKeyFormFields) > 0)
}

// appendNew appends each of add that list doesn't hold
func appendNew(list, add []string) []string {
	for _, v := range add {
		if !slices.Contains(list, v) {
			list = append(list, v)
		}
	}
	return list
}

// appendHeaders appends each header of add that list doesn't hold, comparing them as header names
func appendHeaders(list, add []string) []string {
	for _, name := range add {
		name = http.CanonicalHeaderKey(name)
		if !slices.ContainsFunc(list, func(existing string) bool { return http.CanonicalHeaderKey(existing) == name }) {
			list = append(list, name)
		}
	}
	return list
}

// cacheKeyPrefix digests what changes a global result besides the query and the pool, whose namespace
// follows it: the cached form's version, replica labels and deduplication
func (e *globalEvaluator) cacheKeyPrefix() string {
	h := sha256.New()
	h.Write([]byte(globalCacheVersion + "\x01"))
	for _, label := range e.settings.ReplicaLabels {
		h.Write([]byte(label + "\x00"))
	}
	h.Write([]byte("\x01" + e.settings.Deduplication))
	return globalOriginHost + "." + hex.EncodeToString(h.Sum(nil)[:8])
}

// routesFor returns the options and routes keeping results under req's pool namespace, which also
// keys them by the headers the pool's members key their own answers by
func (cg *cachedGlobal) routesFor(req backends.TSMGlobalRequest) *globalRoutes {
	if rt := cg.routes.Load(); rt != nil && rt.namespace == req.Namespace {
		return rt
	}
	o := cg.base.Clone()
	o.CacheKeyPrefix = cg.prefix + "." + req.Namespace
	rt := &globalRoutes{namespace: req.Namespace, options: o, instant: cg.instant, rng: cg.rng}
	if len(req.KeyHeaders) > 0 {
		rt.instant, rt.rng = cg.instant.Clone(), cg.rng.Clone()
		rt.instant.CacheKeyHeaders = appendHeaders(rt.instant.CacheKeyHeaders, req.KeyHeaders)
		rt.rng.CacheKeyHeaders = appendHeaders(rt.rng.CacheKeyHeaders, req.KeyHeaders)
	}
	cg.routes.Store(rt)
	return rt
}

// route returns the namespace's route for a query, merged with the identity of matched, the ALB path the
// request arrived on, which normalizing its path would lose
func (rt *globalRoutes) route(matched *pathopts.Options, isRange bool) *pathopts.Options {
	pc := rt.instant
	if isRange {
		pc = rt.rng
	}
	if !keysIdentity(matched) {
		return pc
	}
	key := globalVariant{matched: matched, isRange: isRange}
	if v, ok := rt.variants.Load(key); ok {
		return v.(*pathopts.Options)
	}
	variant := pc.Clone()
	mergeIdentity(variant, matched)
	v, _ := rt.variants.LoadOrStore(key, variant)
	return v.(*pathopts.Options)
}

// serve validates r, then answers it from the cache or a whole evaluation, which concurrent requests for
// the same result share; r's path is the query's own
func (cg *cachedGlobal) serve(w http.ResponseWriter, r *http.Request, req backends.TSMGlobalRequest) {
	q, err := parseGlobalQuery(r)
	if err != nil {
		cg.e.reject(w, err)
		return
	}
	// requests that share an evaluation or a cached result must agree on all that decides it
	qp, _, _ := params.GetRequestValues(r)
	qp.Set(partialResponseParam, strconv.FormatBool(cg.e.partial(q)))
	if timeout := cg.e.timeout(q); timeout > 0 {
		qp.Set(timeoutParam, timeout.String())
	} else {
		qp.Del(timeoutParam)
	}
	if q.limit == 0 {
		qp.Del(upLimit)
	}
	var tracer *tracing.Tracer
	var matched *pathopts.Options
	if rsc := request.GetResources(r); rsc != nil {
		tracer, matched = rsc.Tracer, rsc.PathConfig
	}
	rt := cg.routesFor(req)
	pc := rt.route(matched, q.isRange)
	for attempt := 1; ; attempt++ {
		rsc := request.NewResources(rt.options, pc, cg.client.Cache().Configuration(), cg.client.Cache(),
			cg.client, tracer)
		// an authorized request's result is kept under its credential, as time series results are
		rsc.PerCredentialCache = true
		rr := request.SetResources(r.Clone(r.Context()), rsc)
		params.SetRequestValues(rr, qp)
		jw := &joinWriter{w: w, ctx: r.Context(), header: make(http.Header), retry: attempt < maxJoinAttempts}
		cg.dispatchOwned(&globalOwner{ctx: r.Context(), req: req}, jw, rr, rsc, q.isRange)
		if !jw.dropped {
			return
		}
	}
}

// dispatchOwned dispatches r as owner's request, for as long as it runs
func (cg *cachedGlobal) dispatchOwned(owner *globalOwner, w http.ResponseWriter, r *http.Request,
	rsc *request.Resources, isRange bool,
) {
	cg.owners.Store(rsc, owner)
	defer cg.owners.Delete(rsc)
	cg.dispatch(w, r, isRange)
}

// owner returns the client request the cache is evaluating r for, or nil when r is no client's, such
// as a background revalidation
func (cg *cachedGlobal) owner(r *http.Request) *globalOwner {
	if cg == nil {
		return nil
	}
	if v, ok := cg.owners.Load(request.GetResources(r)); ok {
		return v.(*globalOwner)
	}
	return nil
}

// keyedFor reports whether r's result is kept under namespace's results
func (cg *cachedGlobal) keyedFor(r *http.Request, namespace string) bool {
	rsc := request.GetResources(r)
	return rsc != nil && rsc.BackendOptions != nil && rsc.BackendOptions.CacheKeyPrefix == cg.prefix+"."+namespace
}

// joinWriter passes a response through, unless it reports a cancellation this request didn't make, as
// an evaluation it joined reports when the client that started it goes away
type joinWriter struct {
	w      http.ResponseWriter
	ctx    context.Context
	header http.Header
	// retry allows a foreign cancellation to be dropped, so the request runs again
	retry   bool
	status  int
	dropped bool
}

func (j *joinWriter) Header() http.Header { return j.header }

func (j *joinWriter) WriteHeader(status int) {
	if j.status != 0 {
		return
	}
	j.status = status
	if status == statusClientClosedRequest && j.retry && j.ctx.Err() == nil {
		j.dropped = true
		return
	}
	h := j.w.Header()
	maps.Copy(h, j.header)
	j.w.WriteHeader(status)
}

func (j *joinWriter) Write(p []byte) (int, error) {
	if j.status == 0 {
		j.WriteHeader(http.StatusOK)
	}
	if j.dropped {
		return len(p), nil
	}
	return j.w.Write(p)
}

// inProcessTransport answers requests with a handler rather than a network round trip
type inProcessTransport struct {
	handler http.Handler
}

func (t inProcessTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := &bufferedResponse{header: make(http.Header)}
	t.handler.ServeHTTP(w, r)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return &http.Response{
		Status:        strconv.Itoa(w.status) + " " + http.StatusText(w.status),
		StatusCode:    w.status,
		Proto:         r.Proto,
		ProtoMajor:    r.ProtoMajor,
		ProtoMinor:    r.ProtoMinor,
		Header:        w.header,
		Body:          io.NopCloser(bytes.NewReader(w.body.Bytes())),
		ContentLength: int64(w.body.Len()),
		Request:       r,
	}, nil
}

type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}
