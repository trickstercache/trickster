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
	"strconv"
	"strings"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	vmo "github.com/trickstercache/trickster/v2/pkg/backends/victoriametrics/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	promtest "github.com/prometheus/client_golang/prometheus/testutil"
)

func newTestClient(t *testing.T, origin string, mutate func(*bo.Options)) *Client {
	t.Helper()
	o := bo.New()
	o.Provider, o.OriginURL = providers.VictoriaMetrics, origin
	if mutate != nil {
		mutate(o)
	}
	if err := o.Initialize("vm"); err != nil {
		t.Fatal(err)
	}
	b, err := NewClient(o.Name, o, http.NotFoundHandler(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return b.(*Client)
}

func newRequest(method, path string, v url.Values) *http.Request {
	if method == http.MethodPost {
		r := httptest.NewRequest(method, path, strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}
	return httptest.NewRequest(method, path+"?"+v.Encode(), nil)
}

func secs(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func TestPrepareRequest(t *testing.T) {
	c := newTestClient(t, "http://vm.example:8428", nil)
	past := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	rng := func(extra ...string) url.Values {
		v := url.Values{"query": {"sum(m)"}, "start": {secs(past)}, "end": {secs(past.Add(30 * time.Minute))}, "step": {"60"}}
		for i := 0; i+1 < len(extra); i += 2 {
			v.Add(extra[i], extra[i+1])
		}
		return v
	}
	tests := []struct {
		name, method, path string
		values             url.Values
		want               bool
		reason             string
	}{
		{"range", http.MethodGet, "/api/v1/query_range", rng(), true, reasonEligible},
		{"range post", http.MethodPost, "/api/v1/query_range", rng(), true, reasonEligible},
		{"result params", http.MethodGet, "/api/v1/query_range", rng("round_digits", "2", "extra_label", "a=b", "extra_label", "c=d", "nocache", "0"), true, reasonEligible},
		{"object shape", http.MethodGet, "/api/v1/query_range", url.Values{"query": {"range_avg(m)"}, "start": rng()["start"], "end": rng()["end"], "step": {"60"}}, true, reasonRangeFunc},
		{"put", http.MethodPut, "/api/v1/query_range", rng(), false, reasonMethod},
		{"unknown param", http.MethodGet, "/api/v1/query_range", rng("foo", "bar"), false, reasonUnknownParam},
		{"time on range", http.MethodGet, "/api/v1/query_range", rng("time", "1"), false, reasonUnknownParam},
		{"repeated param", http.MethodGet, "/api/v1/query_range", rng("step", "30"), false, reasonRepeatedParam},
		{"nocache", http.MethodGet, "/api/v1/query_range", rng("nocache", "1"), false, reasonNoCache},
		{"trace", http.MethodGet, "/api/v1/query_range", rng("trace", "true"), false, reasonTrace},
		{"volatile", http.MethodGet, "/api/v1/query_range", url.Values{"query": {"now()"}, "start": rng()["start"], "end": rng()["end"], "step": {"60"}}, false, reasonVolatile},
		{"parse error", http.MethodGet, "/api/v1/query_range", url.Values{"query": {"sum("}, "start": rng()["start"], "end": rng()["end"], "step": {"60"}}, false, reasonParse},
		{"relative start", http.MethodGet, "/api/v1/query_range", url.Values{"query": {"m"}, "start": {"-1h"}, "end": rng()["end"], "step": {"60"}}, false, reasonTime},
		{"bad step", http.MethodGet, "/api/v1/query_range", url.Values{"query": {"m"}, "start": rng()["start"], "end": rng()["end"], "step": {"x"}}, false, reasonTime},
		{"reversed range", http.MethodGet, "/api/v1/query_range", url.Values{"query": {"m"}, "start": rng()["end"], "end": rng()["start"], "step": {"60"}}, false, reasonTime},
		{"historical instant", http.MethodGet, "/api/v1/query", url.Values{"query": {"m"}, "time": {secs(past)}, "step": {"60"}}, true, reasonEligible},
		{"live instant", http.MethodGet, "/api/v1/query", url.Values{"query": {"m"}, "time": {secs(time.Now())}}, false, reasonLiveEdge},
		{"instant without time", http.MethodGet, "/api/v1/query", url.Values{"query": {"m"}}, false, reasonLiveEdge},
		{"instant relative time", http.MethodGet, "/api/v1/query", url.Values{"query": {"m"}, "time": {"now-1h"}}, false, reasonTime},
		{"grafana live instant", http.MethodGet, "/api/v1/query", url.Values{"query": {"m"}, "time": {secs(time.Now()) + ".5682888"}}, false, reasonLiveEdge},
		{"labels", http.MethodGet, "/api/v1/labels", url.Values{"start": rng()["start"], "match[]": {"a", "b"}, "limit": {"5"}}, true, reasonEligible},
		{"label values", http.MethodGet, "/api/v1/label/job/values", url.Values{"end": rng()["end"]}, true, reasonEligible},
		{"series relative", http.MethodGet, "/api/v1/series", url.Values{"start": {"-1d"}, "match[]": {"m"}}, false, reasonTime},
		{"series query param", http.MethodGet, "/api/v1/series", url.Values{"query": {"m"}}, false, reasonUnknownParam},
		{"other route", http.MethodGet, "/api/v1/status/tsdb", url.Values{"topN": {"5"}}, true, reasonEligible},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := newRequest(test.method, test.path, test.values)
			mode := cacheModeProxy
			if test.want {
				mode = cacheModeObject
				if test.path == "/api/v1/query_range" && test.reason == reasonEligible {
					mode = cacheModeDelta
				}
			}
			counter := metrics.VictoriaMetricsQueryAnalysis.WithLabelValues("vm", mode, test.reason)
			before := promtest.ToFloat64(counter)
			if got := c.prepareRequest(r); got != test.want {
				t.Fatalf("got %v want %v", got, test.want)
			}
			if routeOf(test.path) != routeOther && promtest.ToFloat64(counter) != before+1 {
				t.Errorf("analysis (%s, %s) was not counted", mode, test.reason)
			}
		})
	}
}

func TestPreparePostMergesForm(t *testing.T) {
	c := newTestClient(t, "http://vm.example:8428", nil)
	past := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	body := url.Values{"query": {"m"}, "time": {secs(past)}, "extra_label": {"a=b"}}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/query?extra_label=c%3Dd&step=60", strings.NewReader(body.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if !c.prepareRequest(r) {
		t.Fatal("a form spanning the URL and the body was relayed")
	}
	if r.URL.RawQuery != "extra_label=c%3Dd&step=60" {
		t.Errorf("an unchanged POST was rewritten: %s", r.URL.RawQuery)
	}
	// a POST range VictoriaMetrics would align is rewritten to its aligned range
	long := url.Values{"query": {"m"}, "start": {"1700000017"}, "end": {"1700003617"}, "step": {"60"}}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/query_range", strings.NewReader(long.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if !c.prepareRequest(r) {
		t.Fatal("a long POST range was relayed")
	}
	if got, _ := url.ParseQuery(r.URL.RawQuery); got.Get("start") != "1699999980" || got.Get("end") != "1700003580" {
		t.Errorf("aligned values %v", got)
	}
	// a single-valued field in both the URL and the body is ambiguous
	r = httptest.NewRequest(http.MethodPost, "/api/v1/query?query=x", strings.NewReader(body.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.prepareRequest(r) {
		t.Error("a field set in both the URL and the body was cached")
	}
	for _, ct := range []string{"application/json", ""} {
		r = httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(body.Encode()))
		r.Header.Set("Content-Type", ct)
		if c.prepareRequest(r) {
			t.Errorf("content type %q was cached", ct)
		}
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/query?%zz", nil)
	if c.prepareRequest(r) {
		t.Error("an unparsable query string was cached")
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader("%zz"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.prepareRequest(r) {
		t.Error("an unparsable form was cached")
	}
}

func TestPrepareSkipsPathRequestParams(t *testing.T) {
	c := newTestClient(t, "http://vm.example:8428", nil)
	r := newRequest(http.MethodGet, "/api/v1/labels", url.Values{})
	pc := po.New()
	pc.RequestParams = map[string]string{"extra_label": "tenant=a"}
	r = request.SetResources(r, request.NewResources(c.Configuration(), pc, nil, nil, c, nil))
	if c.prepareRequest(r) {
		t.Error("a path that rewrites request parameters was cached")
	}
}

func TestAdjustRange(t *testing.T) {
	c := newTestClient(t, "http://vm.example:8428", nil)
	tests := []struct {
		start, end, step       string
		wantStart, wantEnd     string
		changed, ok            bool
		disableCacheAlignments bool
	}{
		// 61 points: aligned down, with the same number of points
		{"1700000017", "1700003617", "60", "1699999980", "1700003580", true, true, false},
		// fewer than 50 points keep their own grid
		{"1700000017", "1700000617", "60", "1700000017", "1700000617", false, true, false},
		// already on the grid
		{"1700000040", "1700003640", "60", "1700000040", "1700003640", false, true, false},
		{"1700000017.5", "1700003617", "1m", "1699999980", "1700003520", true, true, false},
		{"2023-11-14T22:13:37Z", "1700003617", "60", "1699999980", "1700003580", true, true, false},
		{"1700000017", "1700003617", "60", "1700000017", "1700003617", false, true, true},
		{"1700000017", "1700003617", "0", "", "", false, false, false},
		{"-1h", "1700003617", "60", "", "", false, false, false},
		{"1700000017", "now", "60", "", "", false, false, false},
	}
	for _, test := range tests {
		c.searchDisableCache = test.disableCacheAlignments
		v := url.Values{"start": {test.start}, "end": {test.end}, "step": {test.step}}
		changed, ok := c.adjustRange(v)
		if changed != test.changed || ok != test.ok {
			t.Errorf("%v: got (%v, %v)", test, changed, ok)
			continue
		}
		if ok && (v.Get("start") != test.wantStart || v.Get("end") != test.wantEnd) {
			t.Errorf("%v: got %s..%s", test, v.Get("start"), v.Get("end"))
		}
	}
}

func TestTimesAndSteps(t *testing.T) {
	for v, want := range map[string]int64{
		"1700000000":                1700000000000,
		"1700000000.5":              1700000000500,
		"1700000000.123":            1700000000123,
		"170000000":                 170000000000,
		"2023-11-14T22:13:20Z":      1700000000000,
		"2023-11-14T22:13:20.250Z":  1700000000250,
		"2023-11-14T23:13:20+01:00": 1700000000000,
	} {
		if got, err := absoluteTime(v); err != nil || got != want {
			t.Errorf("%s: got %d, %v", v, got, err)
		}
	}
	for _, v := range []string{
		"1700000000000", "0170000000", "1700000000.1234", "1700000000.", "17e8", "-1h", "now",
		"2023-11-14T22:13:20", "2023-11-14", "2023-11-14T22:13:20.0001Z", "1960-01-01T00:00:00Z", "",
	} {
		if _, err := absoluteTime(v); err == nil {
			t.Errorf("%q was read as an absolute time", v)
		}
	}
	for v, want := range map[string]int64{"60": 60000, "15.5": 15500, "1m": 60000, "1h30m": 5400000} {
		if got, ok := stepMillis(v); !ok || got != want {
			t.Errorf("step %s: got %d, %v", v, got, ok)
		}
	}
	for _, v := range []string{"0", "-5", "x", "0.0001", "undefined"} {
		if _, ok := stepMillis(v); ok {
			t.Errorf("step %q was accepted", v)
		}
	}
	if formatMillis(1700000000000) != "1700000000" || formatMillis(1700000000500) != "1700000000.500" {
		t.Error("unexpected millisecond formatting")
	}
	for v, want := range map[string]bool{"": false, "0": false, "f": false, "FALSE": false, "no": false, "1": true, "yes": true} {
		if truthy(v) != want {
			t.Errorf("truthy(%q) != %v", v, want)
		}
	}
}

func TestInstantReason(t *testing.T) {
	c := newTestClient(t, "http://vm.example:8428", func(o *bo.Options) {
		o.VolatileWindow = timeconv.Duration(10 * time.Minute)
	})
	at := time.Now().Add(-5 * time.Minute)
	if c.instantReason(url.Values{"time": {secs(at)}}) != reasonLiveEdge {
		t.Error("an instant inside the configured volatile window was settled")
	}
	c = newTestClient(t, "http://vm.example:8428", nil)
	if r := c.instantReason(url.Values{"time": {secs(at)}}); r != "" {
		t.Errorf("an instant five minutes ago was %s by default", r)
	}
	if c.instantReason(url.Values{"time": {secs(at)}, "latency_offset": {"10m"}}) != reasonLiveEdge {
		t.Error("a request's own latency offset did not widen the volatile window")
	}
	if c.instantReason(url.Values{"time": {secs(at) + ".1234567"}}) != reasonTime {
		t.Error("a settled time with more than millisecond precision was cached")
	}
	if c.instantReason(url.Values{"time": {"2023-11-14T22:13:20Z"}}) != "" {
		t.Error("an RFC 3339 time was not settled")
	}
}

func TestGraphitePathOption(t *testing.T) {
	c := newTestClient(t, "http://vm.example:8481/select/0/prometheus", func(o *bo.Options) {
		o.VictoriaMetrics = &vmo.Options{GraphitePath: "/custom/graphite", SearchDisableCache: true}
	})
	if c.GraphiteBaseURL().String() != "http://vm.example:8481/custom/graphite" || !c.searchDisableCache {
		t.Errorf("options were not applied: %s", c.GraphiteBaseURL())
	}
}
