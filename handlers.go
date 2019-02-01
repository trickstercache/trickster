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

package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/golang/snappy"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"github.com/prometheus/common/model"
)

const (
	// Origin database types
	otPrometheus = "prometheus"

	// Common HTTP Header Values
	hvNoCache         = "no-cache"
	hvApplicationJSON = "application/json"

	// Common HTTP Header Names
	hnCacheControl  = "Cache-Control"
	hnAllowOrigin   = "Access-Control-Allow-Origin"
	hnContentType   = "Content-Type"
	hnAuthorization = "Authorization"

	// HTTP methods
	hmGet = "GET"

	// Prometheus response values
	rvSuccess = "success"
	rvMatrix  = "matrix"
	rvVector  = "vector"

	// Common URL parameter names
	upQuery      = "query"
	upStart      = "start"
	upEnd        = "end"
	upStep       = "step"
	upOriginFqdn = "origin_fqdn"
	upOriginPort = "origin_port"
	upTimeout    = "timeout"
	upOrigin     = "origin"
	upTime       = "time"

	// Cache lookup results
	crKeyMiss    = "kmiss"
	crRangeMiss  = "rmiss"
	crHit        = "hit"
	crPartialHit = "phit"
	crPurge      = "purge"
)

// TricksterHandler contains the services the Handlers need to operate
type TricksterHandler struct {
	Logger           log.Logger
	Config           *Config
	Metrics          *ApplicationMetrics
	Cacher           Cache
	ResponseChannels map[string]chan *ClientRequestContext
	ChannelCreateMtx sync.Mutex
}

// HTTP Handlers

// pingHandler handles calls to /ping, which checks the health of the Trickster app, but not connectivity to upstream origins
// it respond with 200 OK and "pong" so long as the HTTP Server is running and taking requests
func (t *TricksterHandler) pingHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(hnCacheControl, hvNoCache)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("pong"))
}

// promHealthCheckHandler returns the health of Trickster
// can't support multi-origin full proxy for path-based proxying
func (t *TricksterHandler) promHealthCheckHandler(w http.ResponseWriter, r *http.Request) {
	level.Debug(t.Logger).Log(lfEvent, "promHealthCheckHandler", "path", r.URL.Path, "method", r.Method)

	// Check the labels path for Prometheus Origin Handler to satisfy health check
	path := prometheusAPIv1Path + mnLabels

	originURL := t.getOrigin(r).OriginURL + strings.Replace(path, "//", "/", 1)
	body, resp, _, err := t.getURL(r.Method, originURL, r.URL.Query(), getProxyableClientHeaders(r))
	if err != nil {
		level.Error(t.Logger).Log(lfEvent, "error fetching data from origin Prometheus", lfDetail, err.Error())
		w.WriteHeader(http.StatusBadGateway)
		return
	}

	for k, v := range resp.Header {
		w.Header().Set(k, strings.Join(v, ","))
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// promFullProxyHandler handles calls to non-api paths for single-origin configurations and multi-origin via param or hostname
// can't support multi-origin full proxy for path-based proxying
func (t *TricksterHandler) promFullProxyHandler(w http.ResponseWriter, r *http.Request) {
	level.Debug(t.Logger).Log(lfEvent, "promFullProxyHandler", "path", r.URL.Path, "method", r.Method)

	path := r.URL.Path
	vars := mux.Vars(r)

	// clear out the origin moniker from the front of the API path
	if originName, ok := vars["originMoniker"]; ok {
		if strings.HasPrefix(path, "/"+originName) {
			path = strings.Replace(path, "/"+originName, "", 1)
		}
	}

	originURL := t.getOrigin(r).OriginURL + strings.Replace(path, "//", "/", 1)
	body, resp, _, err := t.getURL(r.Method, originURL, r.URL.Query(), getProxyableClientHeaders(r))
	if err != nil {
		level.Error(t.Logger).Log(lfEvent, "error fetching data from origin Prometheus", lfDetail, err.Error())
		w.WriteHeader(http.StatusBadGateway)
		return
	}

	for k, v := range resp.Header {
		w.Header().Set(k, strings.Join(v, ","))
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// promQueryHandler handles calls to /query (for instantaneous values)
func (t *TricksterHandler) promQueryHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	vars := mux.Vars(r)

	// clear out the origin moniker from the front of the API path
	if originName, ok := vars["originMoniker"]; ok {
		if strings.HasPrefix(path, "/"+originName) {
			path = strings.Replace(path, "/"+originName, "", 1)
		}
	}

	originURL := t.getOrigin(r).OriginURL + strings.Replace(path, "//", "/", 1)

	// Get the params from the User request so we can inspect them and pass on to prometheus
	if err := r.ParseForm(); err != nil {
		level.Error(t.Logger).Log(lfEvent, "error parsing form", lfDetail, err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	params := r.Form

	body, resp, err := t.fetchPromQuery(originURL, params, r)
	if err != nil {
		level.Error(t.Logger).Log(lfEvent, "error fetching data from origin Prometheus", lfDetail, err.Error())
		w.WriteHeader(http.StatusBadGateway)
		return
	}

	writeResponse(w, body, resp)
}

// promQueryRangeHandler handles calls to /query_range (requests for timeseries values)
func (t *TricksterHandler) promQueryRangeHandler(w http.ResponseWriter, r *http.Request) {
	ctx, err := t.buildRequestContext(w, r)
	if err != nil {
		level.Error(t.Logger).Log(lfEvent, "error building request context", lfDetail, err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// This WaitGroup ensures that the server does not write the response until we are 100% done Trickstering the range request.
	// The responsders that fulfill client requests will mark the waitgroup done when the response is ready for delivery.
	ctx.WaitGroup.Add(1)
	if ctx.CacheLookupResult == crHit {
		t.respondToCacheHit(ctx)
	} else {
		t.queueRangeProxyRequest(ctx)
	}

	// Wait until the response is fulfilled before delivering.
	ctx.WaitGroup.Wait()
}

// End HTTP Handlers

// Helper functions

// defaultPrometheusMatrixEnvelope returns an empty envelope
func defaultPrometheusMatrixEnvelope() PrometheusMatrixEnvelope {
	return PrometheusMatrixEnvelope{
		Data: PrometheusMatrixData{
			ResultType: rvMatrix,
			Result:     make([]*model.SampleStream, 0),
		},
	}
}

// getProxyableClientHeaders returns any pertinent http headers from the client that we should pass through to the Origin when proxying
func getProxyableClientHeaders(r *http.Request) http.Header {
	headers := http.Header{}

	// pass through Authorization Header
	if authorization, ok := r.Header[hnAuthorization]; ok {
		headers.Add(hnAuthorization, strings.Join(authorization, " "))
	}

	return headers
}

// getOrigin determines the origin server to service the request based on the Host header and url params
func (t *TricksterHandler) getOrigin(r *http.Request) PrometheusOriginConfig {
	var originName string
	var ok bool

	vars := mux.Vars(r)

	// Check for the Origin Name URL Path
	if originName, ok = vars["originMoniker"]; !ok {
		// Check for the Origin Name URL Parmameter (origin=)
		if on, ok := r.URL.Query()[upOrigin]; ok {
			originName = on[1]
		} else {
			// Otherwise use the Host Header
			originName = r.Host
		}
	}

	// If we have matching origin in our Origins Map, return it.
	if p, ok := t.Config.Origins[originName]; ok {
		return p
	}

	// Otherwise, return the default origin if it is configured
	p, ok := t.Config.Origins["default"]
	if !ok {
		p = defaultOriginConfig()
	}

	if t.Config.DefaultOriginURL != "" {
		p.OriginURL = t.Config.DefaultOriginURL
	}

	return p
}

// setResponseHeaders adds any needed headers to the response object.
// this should be called before the body is written
func setResponseHeaders(w http.ResponseWriter) {
	// We're read only and a harmless API, so allow all CORS
	w.Header().Set(hnAllowOrigin, "*")
	// Set the Content-Type so browser's jQuery will auto-parse the response payload
	w.Header().Set(hnContentType, hvApplicationJSON)
}

// getURL makes an HTTP request to the provided URL with the provided parameters and returns the response body
func (t *TricksterHandler) getURL(method string, uri string, params url.Values, headers http.Header) ([]byte, *http.Response, time.Duration, error) {
	if len(params) > 0 {
		uri += "?" + params.Encode()
	}

	parsedURL, err := url.Parse(uri)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("error parsing URL %q: %v", uri, err)
	}

	startTime := time.Now()
	client := &http.Client{}
	resp, err := client.Do(&http.Request{Method: method, URL: parsedURL})
	if err != nil {
		return nil, nil, 0, fmt.Errorf("error downloading URL %q: %v", uri, err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("error reading body from HTTP response for URL %q: %v", uri, err)
	}

	if resp.StatusCode != http.StatusOK {
		// We don't want to return non-200 status codes as internal Go errors,
		// as we want to proxy those status codes all the way back to the user.
		level.Warn(t.Logger).Log(lfEvent, "error downloading URL", "url", uri, "status", resp.Status)
		return body, resp, 0, nil
	}

	duration := time.Since(startTime)

	level.Debug(t.Logger).Log(lfEvent, "prometheusOriginHttpRequest", "url", uri, "duration", duration)

	return body, resp, duration, nil
}

func (t *TricksterHandler) getVectorFromPrometheus(url string, params url.Values, r *http.Request) (PrometheusVectorEnvelope, []byte, *http.Response, error) {
	pe := PrometheusVectorEnvelope{}

	// Make the HTTP Request
	body, resp, err := t.fetchPromQuery(url, params, r)
	if err != nil {
		return pe, body, nil, fmt.Errorf("error fetching data from Prometheus: %v", err)
	}
	// Unmarshal the prometheus data into another PrometheusMatrixEnvelope
	err = json.Unmarshal(body, &pe)
	if err != nil {
		// If we get a scalar response, we just want to return the resp without an error
		// this will allow the upper layers to just use the raw response
		if pe.Data.ResultType != "scalar" {
			return pe, nil, nil, fmt.Errorf("Prometheus vector unmarshaling error for URL %q: %v", url, err)
		}
	}

	return pe, body, resp, nil
}

func (t *TricksterHandler) getMatrixFromPrometheus(url string, params url.Values, r *http.Request) (PrometheusMatrixEnvelope, []byte, *http.Response, time.Duration, error) {
	pe := PrometheusMatrixEnvelope{}

	// Make the HTTP Request - don't use fetchPromQuery here, that is for instantaneous only.
	body, resp, duration, err := t.getURL(r.Method, url, params, getProxyableClientHeaders(r))
	if err != nil {
		return pe, nil, nil, 0, err
	}

	if resp.StatusCode == http.StatusOK {
		// Unmarshal the prometheus data into another PrometheusMatrixEnvelope
		err := json.Unmarshal(body, &pe)
		if err != nil {
			return pe, nil, nil, 0, fmt.Errorf("Prometheus matrix unmarshaling error for URL %q: %v", url, err)
		}
	}

	return pe, body, resp, duration, nil
}

// fetchPromQuery checks for cached instantaneous value for the query and returns it if found,
// otherwise proxies the request to the Prometheus origin and sets the cache with a low TTL
// fetchPromQuery does not do any data marshalling
func (t *TricksterHandler) fetchPromQuery(originURL string, params url.Values, r *http.Request) ([]byte, *http.Response, error) {
	var ttl int64 = 15
	var end int64
	var err error

	cacheKeyBase := originURL
	// if we have an authorization header, that should be part of the cache key to ensure only authorized users can access cached datasets
	if authorization, ok := r.Header[hnAuthorization]; ok {
		cacheKeyBase += strings.Join(authorization, " ")
	}

	if ts, ok := params[upTime]; ok {
		reqStart, err := parseTime(ts[0])
		if err != nil {
			return nil, nil, err
		}
		end = reqStart.Unix()
		if end <= (time.Now().Unix()-1800) && end%1800 == 0 {
			// the Time param is perfectly on the hour and not recent, this is unusual for random dashboard loads.
			// It might be some kind of a daily or hourly rollup. Let's cache it longer than 15s
			ttl = 1800
		}
		end = (end / 15) * 15

		params.Set(upTime, strconv.Itoa(int(end)))
	}

	cacheKey := deriveCacheKey(cacheKeyBase, params)

	var body []byte
	resp := &http.Response{}
	var duration time.Duration

	cacheResult := crKeyMiss

	// check for it in the cache
	cachedBody, err := t.Cacher.Retrieve(cacheKey)
	if err != nil {
		// Cache Miss, we need to get it from prometheus
		body, resp, duration, err = t.getURL(r.Method, originURL, params, getProxyableClientHeaders(r))
		if err != nil {
			return nil, nil, err
		}

		t.Metrics.ProxyRequestDuration.WithLabelValues(originURL, otPrometheus, mnQuery, crKeyMiss, strconv.Itoa(resp.StatusCode)).Observe(duration.Seconds())
		t.Cacher.Store(cacheKey, string(body), ttl)
	} else {
		// Cache hit, return the data set
		body = []byte(cachedBody)
		cacheResult = crHit
		resp.StatusCode = http.StatusOK
	}

	t.Metrics.CacheRequestStatus.WithLabelValues(originURL, otPrometheus, mnQuery, cacheResult, strconv.Itoa(resp.StatusCode)).Inc()

	return body, resp, nil
}

// buildRequestContext Creates a ClientRequestContext based on the incoming client request
func (t *TricksterHandler) buildRequestContext(w http.ResponseWriter, r *http.Request) (*ClientRequestContext, error) {
	var err error

	ctx := &ClientRequestContext{
		Request: r,
		Writer:  w,
		Origin:  t.getOrigin(r),
		Time:    time.Now().Unix(),
	}

	ctx.Origin.OriginURL += strings.Replace(ctx.Origin.APIPath+"/", "//", "/", 1)

	// Get the params from the User request so we can inspect them and pass on to prometheus
	if err := r.ParseForm(); err != nil {
		return nil, errors.Wrap(err, "unable to parse form")
	}
	ctx.RequestParams = r.Form

	// Validate and parse the step value from the user request URL params.
	if len(ctx.RequestParams[upStep]) == 0 {
		return nil, fmt.Errorf("missing step parameter")
	}
	ctx.StepParam = ctx.RequestParams[upStep][0]
	step, err := parseDuration(ctx.StepParam)
	if err != nil {
		return nil, errors.Wrap(err, fmt.Sprintf("failed to parse parameter %q with value %q", upStep, ctx.StepParam))
	}
	if step <= 0 {
		return nil, fmt.Errorf("step parameter %v <= 0, has to be positive", step)
	}
	ctx.StepMS = int64(step.Seconds() * 1000)

	cacheKeyBase := ctx.Origin.OriginURL + ctx.StepParam
	// if we have an authorization header, that should be part of the cache key to ensure only authorized users can access cached datasets
	if authorization, ok := r.Header[hnAuthorization]; ok {
		cacheKeyBase += strings.Join(authorization, " ")
	}

	// Derive a hashed cacheKey for the query where we will get and set the result set
	// inclusion of the step ensures that datasets with different resolutions are not written to the same key.
	ctx.CacheKey = deriveCacheKey(cacheKeyBase, ctx.RequestParams)

	// We will look for a Cache-Control: No-Cache request header and,
	// if present, bypass the cache for a fresh full query from prometheus.
	// Any user can trigger w/ hard reload (ctrl/cmd+shift+r) to clear out cache-related anomalies
	noCache := false
	if ctx.Origin.IgnoreNoCacheHeader == false && (strings.ToLower(r.Header.Get(hnCacheControl)) == hvNoCache) {
		noCache = true
	}

	// get the browser-requested start/end times, so we can determine what part of the range is not in the cache
	if len(ctx.RequestParams[upStart]) == 0 {
		return nil, fmt.Errorf("missing start time parameter")
	}

	reqStart, err := parseTime(ctx.RequestParams[upStart][0])
	if err != nil {
		return nil, errors.Wrap(err, fmt.Sprintf("failed to parse parameter %q with value %q", upStart, ctx.RequestParams[upStart][0]))
	}

	if len(ctx.RequestParams[upEnd]) == 0 {
		return nil, fmt.Errorf("missing end time parameter")
	}

	reqEnd, err := parseTime(ctx.RequestParams[upEnd][0])
	if err != nil {
		return nil, errors.Wrap(err, fmt.Sprintf("failed to parse parameter %q with value %q", upEnd, ctx.RequestParams[upEnd][0]))
	}

	ctx.RequestExtents.Start, ctx.RequestExtents.End, err = alignStepBoundaries(reqStart.Unix()*1000, reqEnd.Unix()*1000, ctx.StepMS, ctx.Time)
	if err != nil {
		return nil, errors.Wrap(err, "error aligning step boundary")
	}

	// setup some variables to determine and track the status of the query vs what's in the cache
	ctx.Matrix = defaultPrometheusMatrixEnvelope()
	ctx.CacheLookupResult = crKeyMiss

	// parameters for filling gap on the upper bound
	ctx.OriginUpperExtents.Start = ctx.RequestExtents.Start
	ctx.OriginUpperExtents.End = ctx.RequestExtents.End

	// Get the cached result set if present
	cachedBody, err := t.Cacher.Retrieve(ctx.CacheKey)

	if err != nil || noCache {
		// Cache Miss, Get the whole blob from Prometheus.
		// Pass on the browser-requested start/end parameters to our Prom Query
		if noCache {
			ctx.CacheLookupResult = crPurge
		}
	} else {
		// We had a Redis Key Hit for the hashed query key, but we may not have all points requested by browser
		// So we can have a Range Miss, Partial Hit, Full Hit when comparing cached range to what the client requested.
		// So let's find out what we are missing (if anything) and fetch what we don't have

		// See if cache data is compressed by looking for the first character to be "{":, with which the uncompressed JSON would start
		// We do this instead of checking the Compression config bit because if someone turns compression on or off when using filesystem or redis cache,
		// we will have no idea if what is already in the cache was compressed or not based on previous settings
		cb := []byte(cachedBody)
		if cb[0] != 123 {
			// Not a JSON object, try decompressing
			level.Debug(t.Logger).Log("event", "Decompressing Cached Data", "cacheKey", ctx.CacheKey)
			cb, err = snappy.Decode(nil, cb)
			if err == nil {
				cachedBody = string(cb)
			}
		}

		// Marshall the cache payload into a PrometheusMatrixEnvelope struct
		err = json.Unmarshal([]byte(cachedBody), &ctx.Matrix)
		// If there is an error unmarshaling the cache we should treat it as a cache miss
		// and re-fetch from origin
		if err != nil {
			ctx.CacheLookupResult = crRangeMiss
			return ctx, nil
		}

		// Get the Extents of the data in the cache
		ce := ctx.Matrix.getExtents()

		extent := "none"

		// Figure out our Deltas
		if ce.End == 0 || ce.Start == 0 {
			// Something went wrong fetching extents
			ctx.CacheLookupResult = crRangeMiss
		} else if ctx.RequestExtents.Start >= ce.Start && ctx.RequestExtents.End <= ce.End {
			// Full cache hit, no need to refresh dataset.
			// Everything we are requesting is already in cache
			ctx.CacheLookupResult = crHit
			ctx.OriginUpperExtents.Start = 0
			ctx.OriginUpperExtents.End = 0
		} else if ctx.RequestExtents.Start < ce.Start && ctx.RequestExtents.End > ce.End {
			// Partial Cache hit on both ends.
			ctx.CacheLookupResult = crPartialHit
			ctx.OriginUpperExtents.Start = ce.End + ctx.StepMS
			ctx.OriginUpperExtents.End = ctx.RequestExtents.End
			ctx.OriginLowerExtents.Start = ((ctx.RequestExtents.Start / ctx.StepMS) * ctx.StepMS)
			ctx.OriginLowerExtents.End = ce.Start
			extent = "both"
		} else if ctx.RequestExtents.Start > ce.End {
			// Range Miss on the Upper Extent of Cache. We will fill from where our cached data stops to the requested end
			ctx.CacheLookupResult = crRangeMiss
			ctx.OriginUpperExtents.Start = ce.End + ctx.StepMS
			extent = "upper"
		} else if ctx.RequestExtents.End > ce.End {
			// Partial Cache Hit, Missing the Upper Extent
			ctx.CacheLookupResult = crPartialHit
			ctx.OriginUpperExtents.Start = ce.End + ctx.StepMS
			extent = "upper"
		} else if ctx.RequestExtents.End < ce.Start {
			// Range Miss on the Lower Extent of Cache. We will fill from the requested start up to where our cached data stops
			ctx.CacheLookupResult = crRangeMiss
			ctx.OriginLowerExtents.Start = ((ctx.RequestExtents.Start / ctx.StepMS) * ctx.StepMS)
			ctx.OriginLowerExtents.End = ce.Start - ctx.StepMS
			ctx.OriginUpperExtents.Start = 0
			ctx.OriginUpperExtents.End = 0
			extent = "lower"
		} else if ctx.RequestExtents.Start < ce.Start {
			// Partial Cache Hit, Missing Lower Extent
			ctx.CacheLookupResult = crPartialHit
			ctx.OriginLowerExtents.Start = ((ctx.RequestExtents.Start / ctx.StepMS) * ctx.StepMS)
			ctx.OriginLowerExtents.End = ce.Start - ctx.StepMS
			ctx.OriginUpperExtents.Start = 0
			ctx.OriginUpperExtents.End = 0
			extent = "upper"
		} else {
			panic(fmt.Sprintf("Reaching this final clause should be impossible. Yikes! reqStart=%d, reqEnd=%d, ce.Start=%d, ce.End=%d", ctx.RequestExtents.Start, ctx.RequestExtents.End, ce.Start, ce.End))
		}

		level.Debug(t.Logger).Log(lfEvent, "deltaRoutineCompleted", "CacheLookupResult", ctx.CacheLookupResult, lfCacheKey, ctx.CacheKey,
			"cacheStart", ce.Start, "cacheEnd", ce.End, "reqStart", ctx.RequestExtents.Start, "reqEnd", ctx.RequestExtents.End,
			"OriginLowerExtents.Start", ctx.OriginLowerExtents.Start, "OriginLowerExtents.End", ctx.OriginLowerExtents.End,
			"OriginUpperExtents.Start", ctx.OriginUpperExtents.Start, "OriginUpperExtents.End", ctx.OriginUpperExtents.End, "extent", extent)
	}

	return ctx, nil
}

func (t *TricksterHandler) respondToCacheHit(ctx *ClientRequestContext) {
	defer ctx.WaitGroup.Done()
	t.Metrics.CacheRequestStatus.WithLabelValues(ctx.Origin.OriginURL, otPrometheus, mnQueryRange, ctx.CacheLookupResult, "200").Inc()

	// Do the extraction of the range the user requested from the fully cached dataset, if needed.
	ctx.Matrix.cropToRange(ctx.RequestExtents.Start, ctx.RequestExtents.End+ctx.StepMS)

	r := &http.Response{}

	// If Fast Forward is enabled and the request is a real-time request, go get that data
	if !ctx.Origin.FastForwardDisable && !(ctx.RequestExtents.End < (ctx.Time*1000)-ctx.StepMS) {
		// Query the latest points if Fast Forward is enabled
		queryURL := ctx.Origin.OriginURL + mnQuery
		originParams := url.Values{}
		// Add the prometheus query params from the user urlparams to the origin request
		passthroughParam(upQuery, ctx.RequestParams, originParams, nil)
		passthroughParam(upTimeout, ctx.RequestParams, originParams, nil)
		passthroughParam(upTime, ctx.RequestParams, originParams, nil)
		ffd, _, resp, err := t.getVectorFromPrometheus(queryURL, originParams, ctx.Request)
		if err != nil {
			level.Error(t.Logger).Log(lfEvent, "error fetching data from origin Prometheus", lfDetail, err.Error())
			ctx.Writer.WriteHeader(http.StatusBadGateway)
			return
		}
		r = resp
		if resp.StatusCode == http.StatusOK && ffd.Status == rvSuccess {
			ctx.Matrix = t.mergeVector(ctx.Matrix, ffd)
		}
	}

	// Marshal the Envelope back to a json object for User Response)
	body, err := json.Marshal(ctx.Matrix)
	if err != nil {
		level.Error(t.Logger).Log(lfEvent, "prometheus matrix marshaling error", lfDetail, err.Error())
		ctx.Writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResponse(ctx.Writer, body, r)
}

func writeResponse(w http.ResponseWriter, body []byte, resp *http.Response) {
	// Now we need to respond to the user request with the dataset
	setResponseHeaders(w)

	if resp.StatusCode == 0 {
		resp.StatusCode = http.StatusOK
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func (t *TricksterHandler) queueRangeProxyRequest(ctx *ClientRequestContext) {
	t.ChannelCreateMtx.Lock()
	ch, ok := t.ResponseChannels[ctx.CacheKey]
	if !ok {
		level.Info(t.Logger).Log(lfEvent, "starting originRangeProxyHandler", lfCacheKey, ctx.CacheKey)
		ch = make(chan *ClientRequestContext, 100)
		t.ResponseChannels[ctx.CacheKey] = ch
		go t.originRangeProxyHandler(ctx.CacheKey, ch)
	}
	t.ChannelCreateMtx.Unlock()

	ch <- ctx
}

func (t *TricksterHandler) originRangeProxyHandler(cacheKey string, originRangeRequests <-chan *ClientRequestContext) {
	// Close handler goroutine if its request channel is empty.
	go func() {
		for {
			time.Sleep(10 * time.Second)

			t.ChannelCreateMtx.Lock()

			if len(originRangeRequests) == 0 {
				if _, ok := t.ResponseChannels[cacheKey]; ok {
					close(t.ResponseChannels[cacheKey])
					delete(t.ResponseChannels, cacheKey)
					t.ChannelCreateMtx.Unlock()
					return
				}
			}

			t.ChannelCreateMtx.Unlock()
		}
	}()

	for r := range originRangeRequests {
		// get the cache data for this request again, in case anything about the record has changed
		// between the time we queued the request and the time it was consumed from the channel
		ctx, err := t.buildRequestContext(r.Writer, r.Request)
		if err != nil {
			level.Error(t.Logger).Log(lfEvent, "error building request context", lfDetail, err.Error())
			r.Writer.WriteHeader(http.StatusBadRequest)
			r.WaitGroup.Done()
			continue
		}

		// The cache miss became a cache hit between the time it was queued and processed.
		if ctx.CacheLookupResult == crHit {
			level.Debug(t.Logger).Log(lfEvent, "delayedCacheHit", lfDetail, "cache was populated with needed data by another proxy request while this one was queued.")
			// Lay the newly-retreived data into the original origin range request so it can fully service the client
			r.Matrix = ctx.Matrix
			// And change the lookup result to a hit.
			r.CacheLookupResult = crHit
			// Respond with the modified original request object so the right WaitGroup is marked as Done()
			t.respondToCacheHit(r)
		} else {

			// Now we know if we need to make any calls to the Origin, lets set those up
			upperDeltaData := PrometheusMatrixEnvelope{}
			lowerDeltaData := PrometheusMatrixEnvelope{}
			fastForwardData := PrometheusVectorEnvelope{}

			var wg sync.WaitGroup

			var m sync.Mutex // Protects originErr and resp below.
			var originErr error
			var errorBody []byte
			resp := &http.Response{}

			if ctx.OriginLowerExtents.Start > 0 && ctx.OriginLowerExtents.End > 0 {
				wg.Add(1)
				go func() {
					defer wg.Done()

					queryURL := ctx.Origin.OriginURL + mnQueryRange
					originParams := url.Values{}
					// Add the prometheus query params from the user urlparams to the origin request
					passthroughParam(upQuery, ctx.RequestParams, originParams, nil)
					passthroughParam(upTimeout, ctx.RequestParams, originParams, nil)
					originParams.Add(upStep, ctx.StepParam)
					originParams.Add(upStart, strconv.FormatInt(ctx.OriginLowerExtents.Start/1000, 10))
					originParams.Add(upEnd, strconv.FormatInt(ctx.OriginLowerExtents.End/1000, 10))
					ldd, b, r, duration, err := t.getMatrixFromPrometheus(queryURL, originParams, r.Request)

					if err != nil {
						m.Lock()
						originErr = err
						m.Unlock()
						return
					}

					m.Lock()
					if resp.StatusCode == 0 || r.StatusCode != http.StatusOK {
						if r.StatusCode != http.StatusOK {
							errorBody = b
						}
						resp = r
					}
					m.Unlock()

					if r.StatusCode == http.StatusOK && ldd.Status == rvSuccess {
						lowerDeltaData = ldd
						t.Metrics.ProxyRequestDuration.WithLabelValues(ctx.Origin.OriginURL, otPrometheus,
							mnQueryRange, ctx.CacheLookupResult, strconv.Itoa(r.StatusCode)).Observe(duration.Seconds())
					}
				}()
			}

			if ctx.OriginUpperExtents.Start > 0 && ctx.OriginUpperExtents.End > 0 {
				wg.Add(1)
				go func() {
					defer wg.Done()

					queryURL := ctx.Origin.OriginURL + mnQueryRange
					originParams := url.Values{}
					// Add the prometheus query params from the user urlparams to the origin request
					passthroughParam(upQuery, ctx.RequestParams, originParams, nil)
					passthroughParam(upTimeout, ctx.RequestParams, originParams, nil)
					originParams.Add(upStep, ctx.StepParam)
					originParams.Add(upStart, strconv.FormatInt(ctx.OriginUpperExtents.Start/1000, 10))
					originParams.Add(upEnd, strconv.FormatInt(ctx.OriginUpperExtents.End/1000, 10))
					udd, b, r, duration, err := t.getMatrixFromPrometheus(queryURL, originParams, r.Request)

					if err != nil {
						m.Lock()
						originErr = err
						m.Unlock()
						return
					}

					m.Lock()
					if resp.StatusCode == 0 || r.StatusCode != http.StatusOK {
						if r.StatusCode != http.StatusOK {
							errorBody = b
						}
						resp = r
					}
					m.Unlock()

					if r != nil && r.StatusCode == http.StatusOK && udd.Status == rvSuccess {
						upperDeltaData = udd
						t.Metrics.ProxyRequestDuration.WithLabelValues(ctx.Origin.OriginURL, otPrometheus,
							mnQueryRange, ctx.CacheLookupResult, strconv.Itoa(r.StatusCode)).Observe(duration.Seconds())
					}
				}()
			}

			if !ctx.Origin.FastForwardDisable && !(ctx.RequestExtents.End < ctx.Time-ctx.StepMS) {
				wg.Add(1)
				go func() {
					defer wg.Done()

					// Query the latest points if Fast Forward is enabled
					queryURL := ctx.Origin.OriginURL + mnQuery
					originParams := url.Values{}
					// Add the prometheus query params from the user urlparams to the origin request
					passthroughParam(upQuery, ctx.RequestParams, originParams, nil)
					passthroughParam(upTimeout, ctx.RequestParams, originParams, nil)
					passthroughParam(upTime, ctx.RequestParams, originParams, nil)
					ffd, b, r, err := t.getVectorFromPrometheus(queryURL, originParams, r.Request)

					if err != nil {
						m.Lock()
						originErr = err
						m.Unlock()
						return
					}

					m.Lock()
					if resp.StatusCode == 0 || r.StatusCode != http.StatusOK {
						if r.StatusCode != http.StatusOK {
							errorBody = b
						}
						resp = r
					}
					m.Unlock()

					if r != nil && r.StatusCode == http.StatusOK && ffd.Status == rvSuccess {
						fastForwardData = ffd
					}
				}()
			}

			wg.Wait()

			if originErr != nil {
				level.Error(t.Logger).Log(lfEvent, "error fetching data from origin Prometheus", lfDetail, originErr.Error())
				r.Writer.WriteHeader(http.StatusBadGateway)
				r.WaitGroup.Done()
				continue
			}

			t.Metrics.CacheRequestStatus.WithLabelValues(ctx.Origin.OriginURL, otPrometheus, mnQueryRange, ctx.CacheLookupResult, strconv.Itoa(resp.StatusCode)).Inc()

			uncachedElementCnt := int64(0)

			if lowerDeltaData.Status == rvSuccess {
				uncachedElementCnt += lowerDeltaData.getValueCount()
				ctx.Matrix = t.mergeMatrix(ctx.Matrix, lowerDeltaData)
			}

			if upperDeltaData.Status == rvSuccess {
				uncachedElementCnt += upperDeltaData.getValueCount()
				ctx.Matrix = t.mergeMatrix(upperDeltaData, ctx.Matrix)
			}

			// Prune any old points based on retention policy
			ctx.Matrix.cropToRange(int64(ctx.Time-ctx.Origin.MaxValueAgeSecs)*1000, 0)

			// If it's not a full cache hit, we want to write this back to the cache
			if ctx.CacheLookupResult != crHit {
				cacheMatrix := ctx.Matrix.copy()
				if ctx.Origin.NoCacheLastDataSecs != 0 {
					cacheMatrix.cropToRange(0, int64(ctx.Time-ctx.Origin.NoCacheLastDataSecs)*1000)
				}

				// Marshal the Envelope back to a json object for Cache Storage
				cacheBody, err := json.Marshal(cacheMatrix)
				if err != nil {
					level.Error(t.Logger).Log(lfEvent, "prometheus matrix marshaling error", lfDetail, err.Error())
					r.Writer.WriteHeader(http.StatusInternalServerError)
					r.WaitGroup.Done()
					continue
				}

				if t.Config.Caching.Compression {
					level.Debug(t.Logger).Log("event", "Compressing Cached Data", "cacheKey", ctx.CacheKey)
					cacheBody = snappy.Encode(nil, cacheBody)
				}

				// Set the Cache Key with the merged dataset
				t.Cacher.Store(cacheKey, string(cacheBody), t.Config.Caching.RecordTTLSecs)
				level.Debug(t.Logger).Log(lfEvent, "setCacheRecord", lfCacheKey, cacheKey, "ttl", t.Config.Caching.RecordTTLSecs)
			}

			//Do the extraction of the range the user requested, if needed.
			// The only time it may not be needed is if the result was a Key Miss (so the dataset we have is exactly what the user asked for)
			// I add one more step on the end of the request to ensure we catch the fast forward data
			if ctx.CacheLookupResult != crKeyMiss {
				ctx.Matrix.cropToRange(ctx.RequestExtents.Start, ctx.RequestExtents.End+ctx.StepMS)
			}

			allElementCnt := ctx.Matrix.getValueCount()
			cachedElementCnt := allElementCnt - uncachedElementCnt

			if uncachedElementCnt > 0 {
				t.Metrics.CacheRequestElements.WithLabelValues(ctx.Origin.OriginURL, otPrometheus, "uncached").Add(float64(uncachedElementCnt))
			}

			if cachedElementCnt > 0 {
				t.Metrics.CacheRequestElements.WithLabelValues(ctx.Origin.OriginURL, otPrometheus, "cached").Add(float64(cachedElementCnt))
			}

			// Stictch in Fast Forward Data
			if fastForwardData.Status == rvSuccess {
				ctx.Matrix = t.mergeVector(ctx.Matrix, fastForwardData)
			}

			// Marshal the Envelope back to a json object for User Response)
			body, err := json.Marshal(ctx.Matrix)
			if err != nil {
				level.Error(t.Logger).Log(lfEvent, "prometheus matrix marshaling error", lfDetail, err.Error())
				r.Writer.WriteHeader(http.StatusInternalServerError)
				r.WaitGroup.Done()
				continue
			}

			if resp.StatusCode != http.StatusOK {
				writeResponse(r.Writer, errorBody, resp)
			} else {
				writeResponse(r.Writer, body, resp)
			}
			r.WaitGroup.Done()
		}
		// Explicitly release the request context so that the underlying memory can be
		// freed before the next request is received via the channel, which overwrites "r".
		r = nil
	}
}

func alignStepBoundaries(start int64, end int64, stepMS int64, now int64) (int64, int64, error) {
	// Don't query beyond Time.Now() or charts will have weird data on the far right
	if end > now*1000 {
		end = now * 1000
	}

	// In case the user had the start/end parameters reversed chronologically, lets return an error
	if start > end {
		return 0, 0, fmt.Errorf("start is after end")
	}

	// Failsafe to 60s if something inexplicably happened to the step param
	if stepMS <= 0 {
		return 0, 0, fmt.Errorf("step must be > 0")
	}

	// Align start/end to step boundaries
	start = (start / stepMS) * stepMS
	end = ((end / stepMS) * stepMS)

	return start, end, nil
}

func (pe PrometheusMatrixEnvelope) getValueCount() int64 {
	i := int64(0)
	for j := range pe.Data.Result {
		i += int64(len(pe.Data.Result[j].Values))
	}
	return i
}

// mergeVector merges the passed PrometheusVectorEnvelope object with the calling PrometheusVectorEnvelope object
func (t *TricksterHandler) mergeVector(pe PrometheusMatrixEnvelope, pv PrometheusVectorEnvelope) PrometheusMatrixEnvelope {
	if len(pv.Data.Result) == 0 {
		level.Debug(t.Logger).Log(lfEvent, "mergeVectorPrematureExit")
		return pe
	}

	for i := range pv.Data.Result {
		result2 := pv.Data.Result[i]
		for j := range pe.Data.Result {
			result1 := pe.Data.Result[j]
			if result2.Metric.Equal(result1.Metric) {
				if result2.Timestamp > result1.Values[len(result1.Values)-1].Timestamp {
					pe.Data.Result[j].Values = append(pe.Data.Result[j].Values, model.SamplePair{
						Timestamp: model.Time((int64(result2.Timestamp) / 1000) * 1000),
						Value:     result2.Value,
					})
				}
			}
		}
	}

	return pe
}

// mergeMatrix merges the passed PrometheusMatrixEnvelope object with the calling PrometheusMatrixEnvelope object
func (t *TricksterHandler) mergeMatrix(pe PrometheusMatrixEnvelope, pe2 PrometheusMatrixEnvelope) PrometheusMatrixEnvelope {
	if pe.Status != rvSuccess {
		pe = pe2
		return pe2
	} else if pe2.Status != rvSuccess {
		return pe
	}

	for i := range pe2.Data.Result {
		metricSetFound := false
		result2 := pe2.Data.Result[i]
	METRIC_MERGE:
		for j := range pe.Data.Result {
			result1 := pe.Data.Result[j]
			if result2.Metric.Equal(result1.Metric) {
				metricSetFound = true
				// Ensure that we don't duplicate datapoints or put points out-of-order
				// This method assumes that `pe2` is "before" `pe`, we need to actually
				// check and enforce that assumption
				first := result1.Values[0]
				for x := len(result2.Values) - 1; x >= 0; x-- {
					v := result2.Values[x]
					if v.Timestamp < first.Timestamp {
						result1.Values = append(result2.Values[:x+1], result1.Values...)
						break METRIC_MERGE
					}
				}
				break METRIC_MERGE
			}
		}

		if !metricSetFound {
			level.Debug(t.Logger).Log(lfEvent, "MergeMatrixEnvelopeNewMetric", lfDetail, "Did not find mergeable metric set in cache", "metricFingerprint", result2.Metric.Fingerprint())
			// Couldn't find metrics with that name in the existing resultset, so this must
			// be new for this poll. That's fine, just add it outright instead of merging.
			pe.Data.Result = append(pe.Data.Result, result2)
		}
	}

	return pe
}

// cropToRange crops the datasets in a given PrometheusMatrixEnvelope down to the provided start and end times
func (pe *PrometheusMatrixEnvelope) cropToRange(start int64, end int64) {
	seriesToRemove := make([]int, 0)

	// iterate through each metric series in the result
	for i := range pe.Data.Result {
		if start > 0 {
			// Now we First determine the correct start index for each series in the Matrix
			// iterate through each value in the given metric series
			for j := range pe.Data.Result[i].Values {
				// If the timestamp for this data point is at or after the client requested start time,
				// update the slice and break the loop.
				ts := int64(pe.Data.Result[i].Values[j].Timestamp)
				if ts >= start {
					pe.Data.Result[i].Values = pe.Data.Result[i].Values[j:]
					break
				}
			}

			if len(pe.Data.Result[i].Values) == 0 || int64(pe.Data.Result[i].Values[len(pe.Data.Result[i].Values)-1].Timestamp) < start {
				seriesToRemove = append(seriesToRemove, i)
			}
		}

		if end > 0 {
			// Then we determine the correct end index for each series in the Matrix
			// iterate *backwards* through each value in the given metric series
			for j := len(pe.Data.Result[i].Values) - 1; j >= 0; j-- {
				// If the timestamp of this metric is at or after the client requested start time,
				// update the offset and break.
				ts := int64(pe.Data.Result[i].Values[j].Timestamp)
				if ts <= end {
					pe.Data.Result[i].Values = pe.Data.Result[i].Values[:j+1]
					break
				}
			}

			if len(pe.Data.Result[i].Values) == 0 || int64(pe.Data.Result[i].Values[0].Timestamp) > end {
				if len(seriesToRemove) == 0 || seriesToRemove[len(seriesToRemove)-1] != i {
					seriesToRemove = append(seriesToRemove, i)
				}
			}
		}
	}

	for i := len(seriesToRemove) - 1; i >= 0; i-- {
		toRemove := seriesToRemove[i]
		pe.Data.Result = append(pe.Data.Result[:toRemove], pe.Data.Result[toRemove+1:]...)
	}
}

// getCacheExtents returns the timestamps of the oldest and newest cached data points for the given query.
func (pe PrometheusMatrixEnvelope) getExtents() MatrixExtents {
	r := pe.Data.Result

	var oldest int64
	var newest int64

	for series := range r {
		if len(r[series].Values) > 0 {
			// Update Oldest Value
			ts := int64(r[series].Values[0].Timestamp)
			if oldest == 0 || ts < oldest {
				oldest = ts
			}

			// Update Newest Value
			ts = int64(r[series].Values[len(r[series].Values)-1].Timestamp)
			if newest == 0 || ts > newest {
				newest = ts
			}
		}
	}

	return MatrixExtents{Start: oldest, End: newest}
}

// copy return a deep copy of PrometheusMatrixEnvelope.
func (pe PrometheusMatrixEnvelope) copy() PrometheusMatrixEnvelope {
	resPe := PrometheusMatrixEnvelope{
		Status: pe.Status,
		Data: PrometheusMatrixData{
			ResultType: pe.Data.ResultType,
			Result:     make([]*model.SampleStream, len(pe.Data.Result)),
		},
	}
	for index := range pe.Data.Result {
		resSampleSteam := *pe.Data.Result[index]
		resPe.Data.Result[index] = &resSampleSteam
	}
	return resPe
}

// passthroughParam passes the parameter with paramName, if present in the requestParams, on to the proxyParams collection
func passthroughParam(paramName string, requestParams url.Values, proxyParams url.Values, filterFunc func(string) string) {
	if value, ok := requestParams[paramName]; ok {
		if filterFunc != nil {
			value[0] = filterFunc(value[0])
		}
		proxyParams.Add(paramName, value[0])
	}
}

// md5sum returns the calculated hex string version of the md5 checksum for the input string
func md5sum(input string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(input)))
}

// deriveCacheKey calculates a query-specific keyname based on the prometheus query in the user request
func deriveCacheKey(prefix string, params url.Values) string {
	k := ""
	// if we have a prefix, set it up
	if len(prefix) > 0 {
		k = md5sum(prefix)
	}

	if query, ok := params[upQuery]; ok {
		k += "." + md5sum(query[0])
	}

	if t, ok := params[upTime]; ok {
		k += "." + md5sum(t[0])
	}

	return k
}

var reRelativeTime = regexp.MustCompile(`([0-9]+)([mshdw])`)

// parseTime converts a query time URL parameter to time.Time.
// Copied from https://github.com/prometheus/prometheus/blob/v2.2.1/web/api/v1/api.go#L798-L807
func parseTime(s string) (time.Time, error) {
	if t, err := strconv.ParseFloat(s, 64); err == nil {
		s, ns := math.Modf(t)
		return time.Unix(int64(s), int64(ns*float64(time.Second))), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cannot parse %q to a valid timestamp", s)
}

// parseDuration converts a duration URL parameter to time.Duration.
// Copied from https://github.com/prometheus/prometheus/blob/v2.2.1/web/api/v1/api.go#L809-L821
func parseDuration(s string) (time.Duration, error) {
	if d, err := strconv.ParseFloat(s, 64); err == nil {
		ts := d * float64(time.Second)
		if ts > float64(math.MaxInt64) || ts < float64(math.MinInt64) {
			return 0, fmt.Errorf("cannot parse %q to a valid duration. It overflows int64", s)
		}
		return time.Duration(ts), nil
	}
	if d, err := model.ParseDuration(s); err == nil {
		return time.Duration(d), nil
	}
	return 0, fmt.Errorf("cannot parse %q to a valid duration", s)
}
