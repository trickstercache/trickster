/*
 * Copyright 2026 The Trickster Authors
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

package victoriametrics

import (
	"errors"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// VictoriaMetrics request parameters, as its vmselect handlers read them
const (
	upQuery         = "query"
	upStart         = "start"
	upEnd           = "end"
	upStep          = "step"
	upTime          = "time"
	upMatch         = "match[]"
	upLimit         = "limit"
	upNoCache       = "nocache"
	upTrace         = "trace"
	upTimeout       = "timeout"
	upRoundDigits   = "round_digits"
	upLatencyOffset = "latency_offset"
	upMaxLookback   = "max_lookback"
	upExtraLabel    = "extra_label"
	upExtraFilters  = "extra_filters[]"
	upOptimize      = "optimize_repeated_binary_op_subexprs"
	upDenyPartial   = "denyPartialResponse"
	upStats         = "stats"

	// VictoriaMetrics aligns range queries of at least this many points to their step while its
	// response cache is on (app/vmselect/promql.AdjustStartEnd)
	minPointsForAlignment = 50
	// the volatile window when the backend configures none: VictoriaMetrics replaces points within
	// -search.latencyOffset (30s) of now and makes new samples searchable within seconds
	defaultVolatileWindow = time.Minute
)

var errUnsupportedTime = errors.New("timestamp format is not cacheable")

// cache path labels and request-level reasons for VictoriaMetricsQueryAnalysis
const (
	cacheModeDelta  = "delta"
	cacheModeObject = "object"
	cacheModeProxy  = "proxy"

	reasonEligible      = "eligible"
	reasonMethod        = "unsupported_method"
	reasonContentType   = "unsupported_content_type"
	reasonPathParams    = "path_request_params"
	reasonInvalidParams = "invalid_params"
	reasonUnknownParam  = "unknown_param"
	reasonRepeatedParam = "repeated_param"
	reasonNoCache       = "nocache"
	reasonTrace         = "trace"
	reasonTime          = "unsupported_time"
	reasonLiveEdge      = "live_edge"
)

// resultParams change a MetricsQL response, so they are part of its cache identity.
var resultParams = []string{
	upRoundDigits, upLatencyOffset, upMaxLookback, upExtraLabel, upExtraFilters, upOptimize,
	upNoCache, upTrace, upLimit,
}

// paramSets are the parameters each cached route's handler reads, by route; a request with any
// other parameter is relayed uncached, since its effect on the response is unknown.
var (
	rangeParams    = paramSet(upQuery, upStart, upEnd, upStep)
	instantParams  = paramSet(upQuery, upTime, upStep)
	metadataParams = paramSet(upMatch, upStart, upEnd, upLimit)
	// read by every query and metadata handler; timeout and denyPartialResponse can only fail a
	// request, and an incomplete response is never cached
	commonParams = paramSet(upTimeout, upDenyPartial, upExtraLabel, upExtraFilters)
	queryParams  = paramSet(upNoCache, upTrace, upRoundDigits, upLatencyOffset, upMaxLookback,
		upOptimize, upStats)
	multiValued = paramSet(upMatch, upExtraLabel, upExtraFilters)
)

func paramSet(names ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

type route uint8

const (
	routeOther route = iota
	routeRange
	routeInstant
	routeMetadata
)

func routeOf(path string) route {
	switch {
	case strings.HasSuffix(path, prometheus.APIPath+"query_range"):
		return routeRange
	case strings.HasSuffix(path, prometheus.APIPath+"query"):
		return routeInstant
	case strings.HasSuffix(path, prometheus.APIPath+"series"),
		strings.HasSuffix(path, prometheus.APIPath+"labels"),
		strings.Contains(path, prometheus.APIPath+"label/"):
		return routeMetadata
	}
	return routeOther
}

func (r route) allows(name string) bool {
	if _, ok := commonParams[name]; ok {
		return true
	}
	var set map[string]struct{}
	switch r {
	case routeRange:
		if _, ok := queryParams[name]; ok {
			return true
		}
		set = rangeParams
	case routeInstant:
		if _, ok := queryParams[name]; ok {
			return true
		}
		set = instantParams
	case routeMetadata:
		set = metadataParams
	default:
		return true
	}
	_, ok := set[name]
	return ok
}

// prepareRequest normalizes a MetricsQL API request before cache lookup. It returns false to relay
// the original request uncached: unknown or repeated parameters, relative times, nocache and trace
// requests, volatile or unparsable statements, and instant queries near the live edge.
func (c *Client) prepareRequest(r *http.Request) bool {
	rt := routeOf(r.URL.Path)
	reason, ok := c.prepare(r, rt)
	if rt != routeOther {
		c.observeAnalysis(rt, reason, ok)
	}
	return ok
}

func (c *Client) prepare(r *http.Request, rt route) (string, bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return reasonMethod, false
	}
	if rsc := request.GetResources(r); rsc != nil && rsc.PathConfig != nil && len(rsc.PathConfig.RequestParams) != 0 {
		return reasonPathParams, false
	}
	values, reason := requestValues(r)
	if reason != "" {
		return reason, false
	}
	for name, fields := range values {
		if !rt.allows(name) {
			return reasonUnknownParam, false
		}
		if _, multi := multiValued[name]; !multi && len(fields) != 1 {
			return reasonRepeatedParam, false
		}
	}
	if truthy(values.Get(upNoCache)) {
		return reasonNoCache, false
	}
	if truthy(values.Get(upTrace)) {
		return reasonTrace, false
	}
	reason = reasonEligible
	changed := false
	switch rt {
	case routeRange, routeInstant:
		a := c.analyzer.analyze(values.Get(upQuery))
		if a.mode == modeProxy {
			return a.reason, false
		}
		if a.mode == modeObject {
			reason = a.reason
		}
		if rt == routeRange {
			var ok bool
			if changed, ok = c.adjustRange(values); !ok {
				return reasonTime, false
			}
		} else if r := c.instantReason(values); r != "" {
			return r, false
		}
	case routeMetadata:
		for _, name := range []string{upStart, upEnd} {
			if v := values.Get(name); v != "" {
				if _, err := absoluteTime(v); err != nil {
					return reasonTime, false
				}
			}
		}
	}
	// a POST is rewritten only when its range changed, since rewriting re-encodes the form into both
	// the URL and the body, and the parser already merges them as validated here
	if changed {
		params.SetRequestValues(r, values)
	}
	return reason, true
}

// observeAnalysis counts the cache path chosen for a MetricsQL API request.
func (c *Client) observeAnalysis(rt route, reason string, cacheable bool) {
	mode := cacheModeProxy
	switch {
	case !cacheable:
	case rt == routeRange && reason == reasonEligible:
		mode = cacheModeDelta
	default:
		mode = cacheModeObject
	}
	metrics.VictoriaMetricsQueryAnalysis.WithLabelValues(c.Name(), mode, reason).Inc()
}

// requestValues merges a POST form over the URL query, as Go's Request.FormValue does for the
// VictoriaMetrics handlers: a body value takes precedence over the same URL parameter. It returns a
// reason when the request can't be read that way.
func requestValues(r *http.Request) (url.Values, string) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, reasonInvalidParams
	}
	if r.Method != http.MethodPost {
		return values, ""
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/x-www-form-urlencoded" {
		return nil, reasonContentType
	}
	body, err := request.GetBody(r)
	if err != nil {
		return nil, reasonInvalidParams
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, reasonInvalidParams
	}
	for name, fields := range form {
		if _, multi := multiValued[name]; multi {
			values[name] = append(fields, values[name]...)
		} else if _, exists := values[name]; exists {
			// one value read two ways is ambiguous enough to relay uncached
			return nil, reasonRepeatedParam
		} else {
			values[name] = fields
		}
	}
	return values, ""
}

// truthy matches VictoriaMetrics' httputil.GetBool.
func truthy(v string) bool {
	switch strings.ToLower(v) {
	case "", "0", "f", "false", "no":
		return false
	}
	return true
}

// absoluteTime parses the timestamps VictoriaMetrics and Trickster read alike: Unix seconds with at
// most millisecond precision, or RFC 3339 with a zone. VictoriaMetrics reads other numbers by their
// magnitude, zoneless times in its own zone, and relative times such as -1h against its clock.
func absoluteTime(v string) (int64, error) {
	secs, frac, hasFrac := strings.Cut(v, ".")
	if n := len(secs); n >= 9 && n <= 10 && secs[0] != '0' && isDigits(secs) {
		if hasFrac && (len(frac) == 0 || len(frac) > 3 || !isDigits(frac)) {
			return 0, errUnsupportedTime
		}
		sec, _ := strconv.ParseInt(secs, 10, 64)
		ms, _ := strconv.ParseInt((frac + "000")[:3], 10, 64)
		return sec*1000 + ms, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil || t.Nanosecond()%int(time.Millisecond) != 0 {
		return 0, errUnsupportedTime
	}
	// the same span as nine or ten digit seconds, which VictoriaMetrics reads as seconds too
	if ms := t.UnixMilli(); ms >= 1e11 && ms < 1e13 {
		return ms, nil
	}
	return 0, errUnsupportedTime
}

func isDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// stepMillis parses a step as VictoriaMetrics does: seconds, or a duration such as 1m.
func stepMillis(v string) (int64, bool) {
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		ms := int64(f * 1e3)
		return ms, ms > 0
	}
	d, err := timeconv.ParseDuration(v)
	if err != nil || d < time.Millisecond {
		return 0, false
	}
	return d.Milliseconds(), true
}

// adjustRange applies VictoriaMetrics' own range adjustment, so the time series cache holds the
// grid VictoriaMetrics answers on: with its response cache on, a range of 50 or more points starts
// on a step boundary and keeps its point count.
func (c *Client) adjustRange(values url.Values) (bool, bool) {
	start, err := absoluteTime(values.Get(upStart))
	if err != nil {
		return false, false
	}
	end, err := absoluteTime(values.Get(upEnd))
	if err != nil {
		return false, false
	}
	step, ok := stepMillis(values.Get(upStep))
	if !ok || start > end {
		return false, false
	}
	if c.searchDisableCache {
		return false, true
	}
	points := (end-start)/step + 1
	if points < minPointsForAlignment {
		return false, true
	}
	alignedStart := start - start%step
	alignedEnd := alignedStart + (points-1)*step
	if alignedStart == start && alignedEnd == end {
		return false, true
	}
	values.Set(upStart, formatMillis(alignedStart))
	values.Set(upEnd, formatMillis(alignedEnd))
	return true, true
}

func formatMillis(ms int64) string {
	if ms%1000 == 0 {
		return strconv.FormatInt(ms/1000, 10)
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64)
}

// instantReason returns why an instant query can't be cached, or "" once its time has settled:
// VictoriaMetrics evaluates a time within its latency offset of now at now minus that offset.
func (c *Client) instantReason(values url.Values) string {
	v := values.Get(upTime)
	if v == "" {
		return reasonLiveEdge
	}
	window := c.volatileWindow()
	if lo := values.Get(upLatencyOffset); lo != "" {
		if ms, ok := stepMillis(lo); ok && time.Duration(ms)*time.Millisecond > window {
			window = time.Duration(ms) * time.Millisecond
		}
	}
	// a time near now is live whatever its precision, as Grafana's instant queries are
	if f, err := strconv.ParseFloat(v, 64); err == nil && math.Abs(f) < 1e10 &&
		time.Since(time.UnixMilli(int64(f*1e3))).Abs() < window {
		return reasonLiveEdge
	}
	at, err := absoluteTime(v)
	if err != nil {
		return reasonTime
	}
	if time.Since(time.UnixMilli(at)).Abs() < window {
		return reasonLiveEdge
	}
	return ""
}
