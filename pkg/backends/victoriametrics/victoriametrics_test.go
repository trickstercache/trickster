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
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestClientContract(t *testing.T) {
	c := newTestClient(t, "http://vm.example:8428", nil)
	var b backends.Backend = c
	if _, ok := b.(backends.TSMMergeProvider); ok {
		t.Error("the client offers TSM planning")
	}
	if _, ok := b.(backends.MergeableTimeseriesBackend); ok {
		t.Error("the client offers mergeable paths")
	}
	for _, name := range []string{
		"query_range", "query", "series", "labels", "proxycache", "proxy",
		"health", handlerGraphite, handlerGraphiteProxy,
	} {
		if c.Handlers()[name] == nil {
			t.Errorf("missing handler %s", name)
		}
	}
	for _, name := range []string{"alerts", "admin"} {
		if c.Handlers()[name] != nil {
			t.Errorf("unexpected handler %s", name)
		}
	}
	supported, def := c.StepAlignments()
	if def != timeseries.StepAlignmentTruncate || supported&timeseries.StepAlignmentPartialEnd != 0 {
		t.Errorf("step alignments (%s, %s)", supported, def)
	}
	hc := c.DefaultHealthCheckConfig()
	if hc.Path != "/api/v1/query" || hc.Query != healthQuery || hc.Host != "vm.example:8428" {
		t.Errorf("health check %+v", hc)
	}
	if healthCheckConfig(nil).Path != "" {
		t.Error("a health check without an origin has a path")
	}
}

func TestDerivedGraphitePath(t *testing.T) {
	for in, want := range map[string]string{
		"http://vm:8428":  "",
		"http://vm:8428/": "",
		"http://vmselect:8481/select/0/prometheus":    "/select/0/graphite",
		"http://vmselect:8481/select/1:2/prometheus/": "/select/1:2/graphite",
		"http://vmauth/vm/select/0/prometheus":        "/vm/select/0/graphite",
		"http://gateway/metrics":                      "/metrics",
		"http://vm:8428/prometheus":                   "/graphite",
	} {
		u, _ := url.Parse(in)
		if got := derivedGraphitePath(u); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
	if derivedGraphitePath(nil) != "" {
		t.Error("a nil origin has a Graphite path")
	}
	c := newTestClient(t, "http://vmselect:8481/select/0/prometheus", nil)
	if got := c.GraphiteBaseURL().String(); got != "http://vmselect:8481/select/0/graphite" {
		t.Errorf("graphite base %s", got)
	}
}

func TestDefaultPathConfigs(t *testing.T) {
	c := newTestClient(t, "http://vm.example:8428", func(o *bo.Options) {
		o.TimeseriesTTL = timeconv.Duration(2 * time.Hour)
	})
	o := c.Configuration()
	paths := c.DefaultPathConfigs(o)
	byKey := map[string][]string{}
	for _, p := range paths {
		byKey[p.Path] = append(byKey[p.Path], p.HandlerName)
		if _, set := p.ResponseHeaders[headers.NameCacheControl]; set {
			t.Errorf("%s sets Cache-Control, which would replace a no-store", p.Path)
		}
	}
	for path, handler := range map[string]string{
		"/api/v1/query_range": "query_range", "/api/v1/query": "query", "/api/v1/series": "series",
		"/api/v1/status": handlerProxyCache, "/render": handlerGraphite, "/graphite/render": handlerGraphite,
		"/metrics/find": handlerGraphite, "/tags/": handlerGraphite, "/graphite/functions": handlerGraphite,
		"/tags/tagSeries": handlerGraphiteProxy, "/graphite/tags/delSeries": handlerGraphiteProxy,
		"/": providers.Proxy, "/api/v1/": providers.Proxy,
	} {
		if !slices.Contains(byKey[path], handler) {
			t.Errorf("%s: handlers %v, want %s", path, byKey[path], handler)
		}
	}
	if o.FastForwardPath == nil || o.FastForwardPath.Path != "/api/v1/query" {
		t.Error("the fast forward path was not set")
	}
	for _, p := range paths {
		switch p.Path {
		case "/api/v1/query_range":
			if !slices.Contains(p.CacheKeyParams, upExtraLabel) || !slices.Contains(p.CacheKeyParams, upRoundDigits) ||
				slices.Contains(p.CacheKeyParams, upStart) {
				t.Errorf("range key params %v", p.CacheKeyParams)
			}
		case "/api/v1/status":
			if !slices.Equal(p.CacheKeyParams, []string{"*"}) {
				t.Errorf("unclassified route key params %v", p.CacheKeyParams)
			}
		case "/":
			if !slices.Equal(p.Methods, methods.AllHTTPMethods()) {
				t.Error("the catch-all does not relay every method")
			}
		case "/render":
			if p.ResponseHeaders[appendCacheControl] != "s-maxage=7200" ||
				!slices.Equal(p.CacheKeyHeaders, []string{storageStepHeader}) {
				t.Errorf("render %v %v", p.ResponseHeaders, p.CacheKeyHeaders)
			}
		case "/metrics/find":
			if p.ResponseHeaders[appendCacheControl] != "s-maxage=30" {
				t.Errorf("find %v", p.ResponseHeaders)
			}
		}
	}
	if graphitePaths(nil)[0].ResponseHeaders[appendCacheControl] != "s-maxage=30" {
		t.Error("render without options")
	}
}

// the Graphite read APIs take QUERY, forwarded as a form POST; the GET-only ones do not
func TestGraphitePathsQueryMethod(t *testing.T) {
	queryable := []string{
		"/render", "/metrics/find", "/metrics/expand",
		"/graphite/render", "/graphite/metrics/find", "/graphite/metrics/expand",
	}
	var seen int
	for _, p := range graphitePaths(nil) {
		takesQuery := slices.Contains(p.Methods, methods.MethodQuery)
		if slices.Contains(queryable, p.Path) {
			seen++
			if !takesQuery || !slices.Equal(p.QueryMediaTypes, []string{headers.ValueXFormURLEncoded}) {
				t.Errorf("%s: methods %v, QUERY media types %v", p.Path, p.Methods, p.QueryMediaTypes)
			}
			continue
		}
		if p.HandlerName == handlerGraphite && (takesQuery || p.QueryMediaTypes != nil) {
			t.Errorf("%s: GET-only route takes QUERY", p.Path)
		}
	}
	if seen != len(queryable) {
		t.Errorf("found %d of %d QUERY routes", seen, len(queryable))
	}
}

func TestParseTimeRangeQuery(t *testing.T) {
	c := newTestClient(t, "http://vm.example:8428", nil)
	past := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	parse := func(c *Client, v url.Values) *timeseries.TimeRangeQuery {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/query_range?"+v.Encode(), nil)
		trq, rlo, _, err := c.ParseTimeRangeQuery(r)
		if err != nil || !rlo.FallbackToProxyOnError {
			t.Fatalf("%v %+v", err, rlo)
		}
		return trq
	}
	v := url.Values{"query": {"rate(m[5m])"}, "start": {secs(past)}, "end": {secs(past.Add(time.Hour))}, "step": {"60"}}
	trq := parse(c, v)
	if trq.StepAlignment != timeseries.StepAlignmentTruncate || trq.VolatileWindow != defaultVolatileWindow {
		t.Errorf("delta shape: %s %s", trq.StepAlignment, trq.VolatileWindow)
	}
	v.Set("query", "range_avg(m)")
	if trq = parse(c, v); trq.StepAlignments != timeseries.StepAlignmentOff || trq.StepAlignment != timeseries.StepAlignmentOff {
		t.Errorf("object shape: %s", trq.StepAlignment)
	}
	v.Set("latency_offset", "5m")
	if trq = parse(c, v); trq.VolatileWindow <= 5*time.Minute {
		t.Errorf("a request latency offset was not volatile: %s", trq.VolatileWindow)
	}
	configured := newTestClient(t, "http://vm.example:8428", func(o *bo.Options) {
		o.VolatileWindow = timeconv.Duration(10 * time.Minute)
	})
	v.Del("latency_offset")
	if trq = parse(configured, v); trq.VolatileWindow != 0 {
		t.Errorf("a configured volatile window was replaced: %s", trq.VolatileWindow)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/query_range?query=m", nil)
	if _, _, _, err := c.ParseTimeRangeQuery(r); err == nil {
		t.Error("a range query without a range was parsed")
	}
}

func TestDirectives(t *testing.T) {
	c := newTestClient(t, "http://vm.example:8428", nil)
	past := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	for query, want := range map[string]timeseries.StepAlignment{
		"rate(m[5m]) # trickster-step-align:drop":                              timeseries.StepAlignmentDrop,
		`label_set(m, "a", "# trickster-step-align:off")`:                      0,
		"rate(m[5m]) # trickster-step-align:off trickster-step-align:truncate": timeseries.StepAlignmentTruncate,
		"rate(m[5m]) # trickster-step-align:bogus":                             0,
	} {
		v := url.Values{"query": {query}, "start": {secs(past)}, "end": {secs(past.Add(time.Hour))}, "step": {"60"}}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/query_range?"+v.Encode(), nil)
		trq, _, _, err := c.ParseTimeRangeQuery(r)
		if err != nil {
			t.Fatal(err)
		}
		if trq.Directives.StepAlignment != want {
			t.Errorf("%s: directive %s, want %s", query, trq.Directives.StepAlignment, want)
		}
		if strings.Contains(trq.KeyParamValues["query"], "trickster-step-align") && want != 0 {
			t.Errorf("%s: the directive is part of the cache identity", query)
		}
	}
}
