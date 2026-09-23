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

package greptimedb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	ep "github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

func TestPrometheusPaths(t *testing.T) {
	o := bo.New()
	b, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	paths := c.DefaultPathConfigs(o)
	for _, name := range []string{"query", "query_range", "series", "labels", "label/job/values"} {
		p := paths.Match("GET", promPath+"/api/v1/"+name)
		if p == nil || p.HandlerName == "proxy" || !slices.Contains(p.CacheKeyParams, databaseParam) || !slices.Contains(p.CacheKeyHeaders, databaseHeader) {
			t.Fatalf("missing route identity: %s %+v", name, p)
		}
	}
	for _, path := range []string{"/api/v1/query", promPath + "/api/v1/alerts", promPath + "/write", "/v1/loki/api/v1/push"} {
		if p := paths.Match("POST", path); p == nil || p.HandlerName != "proxy" {
			t.Fatalf("unsupported endpoint must proxy: %s", path)
		}
	}
	if len(c.MergeablePaths()) != 5 || !providers.IsPrometheusCompatible("greptimedb") || o.FastForwardPath.Path != promPath+"/api/v1/query" {
		t.Fatal("missing prefixed fast-forward/merge capability")
	}
	o.FastForwardPath.CacheKeyParams[0] = "changed"
	if paths.Match("GET", promPath+"/api/v1/query").CacheKeyParams[0] == "changed" {
		t.Fatal("fast-forward path shares mutable identity")
	}
}

func TestPrometheusParameterPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, query, form, wantQuery, wantDB string
	}{
		{"url_wins", "query=vector(1)&db=url", "query=vector(2)&db=form&step=15", "vector(1)", "url"},
		{"empty_url_wins", "query=&db=", "query=vector(2)&db=form", "", ""},
		{"form_db_is_ignored", "", "query=vector(2)&db=form", "vector(2)", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", promPath+"/api/v1/query?"+tc.query, strings.NewReader(tc.form))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if !preparePromRequest(r) {
				t.Fatal("valid request rejected")
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			v, err := url.ParseQuery(string(body))
			if err != nil || v.Get("query") != tc.wantQuery || v.Get("db") != tc.wantDB || r.URL.RawQuery != string(body) {
				t.Fatalf("origin semantics changed: %s %s", r.URL.RawQuery, body)
			}
		})
	}
	for _, raw := range []string{"query=a&query=b", "query=%ZZ", "query=a&limit=1"} {
		r := httptest.NewRequest("POST", promPath+"/api/v1/query", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if preparePromRequest(r) {
			t.Fatalf("unsupported request was normalized: %s", raw)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != raw {
			t.Fatal("passthrough lost the original body")
		}
	}
}

func (h *httpHarness) promQuery(t *testing.T, method, endpoint string, v url.Values, hdr http.Header) *httptest.ResponseRecorder {
	t.Helper()
	path := promPath + "/api/v1/" + endpoint
	var body io.Reader
	if method == "POST" {
		body = strings.NewReader(v.Encode())
		// Greptime selects databases only from the URL or header.
		if _, exists := v["db"]; exists {
			path += "?db=" + url.QueryEscape(v.Get("db"))
		}
	} else {
		path += "?" + v.Encode()
	}
	r := httptest.NewRequest(method, path, body)
	if hdr != nil {
		r.Header = hdr.Clone()
	}
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res := h.resources
	pc := h.client.DefaultPathConfigs(res.BackendOptions).Match(method, r.URL.Path)
	r = request.SetResources(r, request.NewResources(res.BackendOptions, pc, res.CacheConfig, res.CacheClient, h.client, res.Tracer))
	w := httptest.NewRecorder()
	h.client.Handlers()[pc.HandlerName].ServeHTTP(w, r)
	return w
}

func TestPrometheusCacheFlow(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, step := range []time.Duration{15 * time.Second, 500 * time.Millisecond} {
			t.Run(method+step.String(), func(t *testing.T) {
				var calls atomic.Int32
				origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					start, err := time.Parse(time.RFC3339Nano, r.Form.Get("start"))
					if err != nil {
						t.Error(err)
					}
					end, err := time.Parse(time.RFC3339Nano, r.Form.Get("end"))
					if err != nil {
						t.Error(err)
					}
					values := make([][]any, 0)
					for at := start; !at.After(end); at = at.Add(step) {
						values = append(values, []any{float64(at.UnixMilli()) / 1000, "2"})
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{"metric": map[string]string{"job": "test"}, "values": values}}}})
				})
				h := newHTTPHarness(t, origin)
				h.resources.BackendOptions.FastForwardDisable = true
				start := time.Now().UTC().Add(-5 * time.Minute).Truncate(step).Add(125 * time.Millisecond)
				v := url.Values{"query": {"up"}, "db": {"public"}, "start": {start.Format(time.RFC3339Nano)}, "step": {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)}}
				for i, want := range []string{"kmiss", "hit", "phit", "hit"} {
					points := 3
					if i > 1 {
						points = 5
					}
					v.Set("end", start.Add(time.Duration(points-1)*step).Format(time.RFC3339Nano))
					w := h.promQuery(t, method, "query_range", v, nil)
					engine, status := headers.ParseResultEngineStatus(w.Header().Get(headers.NameTricksterResult))
					if w.Code != 200 || engine != "DeltaProxyCache" || status != want {
						t.Fatalf("%d %s %s %s", w.Code, engine, status, w.Body.String())
					}
					var got struct {
						Data struct{ Result []struct{ Values [][]any } }
					}
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Data.Result) != 1 || len(got.Data.Result[0].Values) != points {
						t.Fatalf("wrong matrix: %s (%v)", w.Body.String(), err)
					}
					for n, point := range got.Data.Result[0].Values {
						if point[0] != float64(start.Add(time.Duration(n)*step).UnixMilli())/1000 || point[1] != "2" {
							t.Fatalf("changed evaluation grid: %v", point)
						}
					}
				}
				if calls.Load() != 2 {
					t.Fatalf("expected initial and missing extent only: %d", calls.Load())
				}
			})
		}
	}
}

func TestPrometheusGridFallback(t *testing.T) {
	b, err := NewClient("test", bo.New(), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	for _, tc := range []struct{ start, step string }{
		{"NaN", "15"},
		{"+Inf", "15"},
		{"1704067200.0001", "15"},
		{"1704067200", "0"},
		{"1704067200", "-1"},
		{"1704067200", "0.0001"},
		{"99999999999999", "1"},
		{"0001-01-01T00:00:00Z", "15"},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			v := url.Values{"query": {"up"}, "start": {tc.start}, "end": {"1704067500"}, "step": {tc.step}}
			r := httptest.NewRequest("GET", promPath+"/api/v1/query_range?"+v.Encode(), nil)
			if _, _, _, err := c.ParseTimeRangeQuery(r); err == nil {
				t.Fatal("unsupported grid was admitted")
			}
		})
	}
	c.Configuration().DoesShard = true
	r := httptest.NewRequest("GET", promPath+"/api/v1/query_range?query=up&start=1704067207&end=1704067507&step=15", nil)
	if _, _, _, err := c.ParseTimeRangeQuery(r); err != timeseries.ErrUnknownFormat {
		t.Fatalf("offset grid must not use epoch-aligned sharding: %v", err)
	}
}

func TestPrometheusCacheIdentity(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			var calls atomic.Int32
			origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := calls.Add(1)
				_ = r.ParseForm()
				at, err := time.Parse(time.RFC3339Nano, r.Form.Get("start"))
				if err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"job":"fixture"},"values":[[%d,"%d"]]}]}}`, at.Unix(), id)
			})
			h := newHTTPHarness(t, origin)
			h.resources.BackendOptions.FastForwardDisable = true
			start := time.Now().UTC().Add(-5 * time.Minute).Truncate(15 * time.Second).Add(7 * time.Second)
			for _, tc := range []struct {
				name                 string
				param, value, header string
			}{
				{"default", "", "", ""},
				{"db_a", "db", "alpha", ""},
				{"db_b", "db", "beta", ""},
				{"header_a", "", "alpha", databaseHeader},
				{"header_b", "", "beta", databaseHeader},
				{"lookback_a", "lookback", "1m", ""},
				{"lookback_b", "lookback", "2m", ""},
				{"authorization_a", "", "Basic Zm9vOmJhcg==", "Authorization"},
				{"authorization_b", "", "Basic YmFyOmJheg==", "Authorization"},
				{"greptime_auth", "", "Basic YmFyOmJheg==", "X-Greptime-Auth"},
				{"different_phase", "start", start.Add(time.Second).Format(time.RFC3339Nano), ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					v := url.Values{"query": {"up"}, "start": {start.Format(time.RFC3339Nano)}, "end": {start.Add(30 * time.Second).Format(time.RFC3339Nano)}, "step": {"15"}}
					hdr := make(http.Header)
					if tc.param != "" {
						v.Set(tc.param, tc.value)
					}
					if tc.header != "" {
						hdr.Set(tc.header, tc.value)
					}
					before := calls.Load()
					var first string
					for _, status := range []string{"kmiss", "hit"} {
						w := h.promQuery(t, method, "query_range", v, hdr)
						_, got := headers.ParseResultEngineStatus(w.Header().Get(headers.NameTricksterResult))
						if w.Code != 200 || got != status {
							t.Fatalf("%d %s %s", w.Code, got, w.Body.String())
						}
						if status == "kmiss" {
							first = w.Body.String()
						} else if first != w.Body.String() {
							t.Fatal("cached response changed")
						}
					}
					if calls.Load() != before+1 {
						t.Fatal("cache identity collided or failed to reuse")
					}
				})
			}
		})
	}
}

func TestPrometheusUnsupportedPreservesRequest(t *testing.T) {
	for _, tc := range []struct{ method, path, query, body, contentType string }{
		{"PUT", "query_range", "query=up", "original", "text/plain"},
		{"GET", "query_range", "query=up&limit=1", "", ""},
		{"GET", "query_range", "query=up&query=down", "", ""},
		{"POST", "query_range", "", "query=up", "application/json"},
		{"POST", "query_range", "", "query=%XX", "application/x-www-form-urlencoded"},
		{"POST", "label/job/values", "", "match[]=up", "application/x-www-form-urlencoded"},
	} {
		t.Run(tc.method+tc.path+tc.query+tc.body, func(t *testing.T) {
			origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != tc.method || r.URL.RawQuery != tc.query || string(body) != tc.body {
					t.Errorf("rewrote unsupported request: %s %s %q", r.Method, r.URL.RawQuery, body)
				}
				http.Error(w, "origin response", http.StatusBadRequest)
			})
			h := newHTTPHarness(t, origin)
			r := httptest.NewRequest(tc.method, promPath+"/api/v1/"+tc.path+"?"+tc.query, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			res := h.resources
			pc := h.client.DefaultPathConfigs(res.BackendOptions).Match(tc.method, r.URL.Path)
			r = request.SetResources(r, request.NewResources(res.BackendOptions, pc, res.CacheConfig, res.CacheClient, h.client, res.Tracer))
			w := httptest.NewRecorder()
			h.client.Handlers()[pc.HandlerName].ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || w.Body.String() != "origin response\n" {
				t.Fatalf("lost origin error: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPrometheusMergePlanContract(t *testing.T) {
	b, err := NewClient("test", bo.New(), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	t.Run("URL expression selects the plan", func(t *testing.T) {
		r := httptest.NewRequest("POST", promPath+"/api/v1/query?query=avg(up)&db=public", strings.NewReader("query=min(up)&db=ignored"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		plan, err := c.PlanTSMMerge(r, "min(up)")
		if err != nil {
			t.Fatal(err)
		}
		if plan.OriginalQuery != "avg(up)" || plan.Reduction.Kind != merge.TSMReductionWeightedAverage {
			t.Fatalf("wrong authoritative expression: %+v", plan)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "query=min(up)&db=ignored" || r.URL.Query().Get("query") != "avg(up)" {
			t.Fatal("planner changed caller request")
		}
	})
	for _, query := range []string{"group(up)", "topk(2, up)", "sort(group(up))"} {
		t.Run(query, func(t *testing.T) {
			r := httptest.NewRequest("GET", promPath+"/api/v1/query?query="+url.QueryEscape(query), nil)
			plan, err := c.PlanTSMMerge(r, query)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.StripInjectedLabels {
				t.Fatal("aggregation retains per-backend routing labels")
			}
		})
	}
	t.Run("quantile retains the origin metric name", func(t *testing.T) {
		trq := &timeseries.TimeRangeQuery{Statement: "up"}
		ts, err := c.Modeler().WireUnmarshaler([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"up","host":"a"},"value":[100,"1"]},{"metric":{"__name__":"up","host":"b"},"value":[100,"3"]},{"metric":{"__name__":"up","host":"c"},"value":[100,"10"]}]}}`), trq)
		if err != nil {
			t.Fatal(err)
		}
		c.FinalizeTSMMerge("quantile(0.5, up)", ts)
		ds := ts.(*dataset.DataSet)
		if len(ds.Results) != 1 || len(ds.Results[0].SeriesList) != 1 {
			t.Fatal("wrong quantile result")
		}
		s := ds.Results[0].SeriesList[0]
		if s.Header.Name != "up" || s.Header.Tags["__name__"] != "up" || s.Points[0].Values[0] != "3" {
			t.Fatalf("lost Greptime metric identity: %+v", s)
		}
	})
}

func TestGreptimeMetricName(t *testing.T) {
	for _, tc := range []struct {
		query, name string
		known       bool
	}{
		{"up", "up", true},
		{"sum(up)", "up", true},
		{"sum by () (up)", "", true},
		{"sum by (host) (up)", "", true},
		{"sum by (__name__) (up)", "up", true},
		{"sum without (host) (up)", "up", true},
		{"sum without (__name__) (up)", "", true},
		{"-up", "", true},
		{"up + 1", "", true},
		{"count(up) or vector(0)", "up", true},
		{"up and down", "up", true},
		{"up unless down", "up", true},
		{"rate(up[5m])", "up", true},
		{"max_over_time((up)[5m:])", "up", true},
		{`{"__name__"="quoted.metric"}`, "quoted.metric", true},
		{`{__name__=~"up|down"}`, "", false},
		{`{host="a"}`, "", false},
		{`quantile by (host) (0.5, {__name__=~"up|down"})`, "", false},
		{`quantile(0.5, {__name__=~"up|down"}) / 2`, "", false},
		{`histogram_fraction(0, 1, up)`, "up", true},
		{"sum(", "", false},
		{`label_replace(up, "a", "b", "c", "d")`, "up", true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			name, known := greptimeMetricName(tc.query)
			if name != tc.name || known != tc.known {
				t.Fatalf("got %q %t, want %q %t", name, known, tc.name, tc.known)
			}
		})
	}
}

func TestPrometheusCompressedError(t *testing.T) {
	const body = `{"status":"error","errorType":"internal","error":"origin failed"}`
	for _, encoding := range []string{"gzip", "br", "zstd", "deflate"} {
		for _, method := range []string{"GET", "POST"} {
			t.Run(encoding+method, func(t *testing.T) {
				var encoded bytes.Buffer
				init, header := ep.GetEncoderInitializer(encoding)
				encoder := init(&encoded, 1)
				if _, err := io.WriteString(encoder, body); err != nil {
					t.Fatal(err)
				}
				if err := encoder.Close(); err != nil {
					t.Fatal(err)
				}
				h := newHTTPHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Content-Encoding", header)
					w.Header().Set("X-Origin-Error", "retained")
					w.Header().Set("Content-Length", strconv.Itoa(encoded.Len()))
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write(encoded.Bytes())
				}))
				start := time.Now().Add(-5 * time.Minute).Truncate(15 * time.Second).Add(125 * time.Millisecond)
				v := url.Values{"query": {"up"}, "start": {start.Format(time.RFC3339Nano)}, "end": {start.Add(30 * time.Second).Format(time.RFC3339Nano)}, "step": {"15"}}
				w := h.promQuery(t, method, "query_range", v, http.Header{"Accept-Encoding": {"identity"}})
				if w.Code != http.StatusServiceUnavailable || w.Body.String() != body || w.Header().Get("Content-Encoding") != "" || w.Header().Get("X-Origin-Error") != "retained" {
					t.Fatalf("compressed origin error corrupted: %d %v %q", w.Code, w.Header(), w.Body.String())
				}
			})
		}
	}
}
