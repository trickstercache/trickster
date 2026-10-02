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
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

// vmOrigin answers like VictoriaMetrics: a range query's points fall at start + k*step, valued by
// their own timestamp, so a range assembled from separate fetches equals one answer.
type vmOrigin struct {
	mu      sync.Mutex
	paths   []string
	ranges  []string
	partial bool
	cluster bool
	targets []int
}

func parseOriginTime(v string) (time.Time, error) {
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return time.UnixMilli(int64(f * 1000)), nil
	}
	return time.Parse(time.RFC3339Nano, v)
}

func (o *vmOrigin) envelope() string {
	switch {
	case o.partial:
		return `{"status":"success","isPartial":true,`
	case o.cluster:
		return `{"status":"success","isPartial":false,`
	}
	return `{"status":"success",`
}

func (o *vmOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	o.mu.Lock()
	o.paths = append(o.paths, r.URL.Path)
	env := o.envelope()
	o.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/api/v1/query_range"):
		start, err1 := parseOriginTime(r.Form.Get("start"))
		end, err2 := parseOriginTime(r.Form.Get("end"))
		step, err3 := strconv.Atoi(r.Form.Get("step"))
		if err1 != nil || err2 != nil || err3 != nil || r.Form.Get("query") == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"status":"error","errorType":"400","error":"bad request"}`)
			return
		}
		o.mu.Lock()
		o.ranges = append(o.ranges, fmt.Sprintf("%d-%d", start.Unix(), end.Unix()))
		o.mu.Unlock()
		var points []string
		for t := start; !t.After(end); t = t.Add(time.Duration(step) * time.Second) {
			points = append(points, fmt.Sprintf(`[%d,"%d"]`, t.Unix(), t.Unix()/60))
		}
		fmt.Fprintf(w, `%s"data":{"resultType":"matrix","result":[{"metric":{"__name__":"m","job":"a"},"values":[%s]}]},"stats":{"seriesFetched":"1"}}`,
			env, strings.Join(points, ","))
	case strings.HasSuffix(r.URL.Path, "/api/v1/query"):
		fmt.Fprintf(w, `%s"data":{"resultType":"vector","result":[{"metric":{"job":"a"},"value":[%s,"1"]}]}}`, env, r.Form.Get("time"))
	case strings.HasSuffix(r.URL.Path, "/api/v1/labels"):
		fmt.Fprintf(w, `%s"data":["__name__","job"]}`, env)
	case strings.HasSuffix(r.URL.Path, "/render"):
		o.mu.Lock()
		o.targets = append(o.targets, len(r.Form["target"]))
		o.mu.Unlock()
		fmt.Fprintf(w, `[{"target":"a.b","tags":{"name":"a.b"},"datapoints":[[1,%s]]}]`, r.Form.Get("from"))
	case strings.HasSuffix(r.URL.Path, "/metrics/find"), strings.HasSuffix(r.URL.Path, "/tags/tagSeries"):
		fmt.Fprint(w, `[{"id":"a.b","text":"b","leaf":1}]`)
	default:
		http.NotFound(w, r)
	}
}

func (o *vmOrigin) lastPath() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.paths[len(o.paths)-1]
}

func (o *vmOrigin) fetched() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.ranges...)
}

type harness struct {
	t      *testing.T
	origin *vmOrigin
	client *Client
	rsc    *request.Resources
}

func newHarness(t *testing.T, basePath string) *harness {
	t.Helper()
	origin := &vmOrigin{}
	ts := httptest.NewServer(origin)
	t.Cleanup(ts.Close)
	placeholder, _, req, _, err := tu.NewTestInstance("", (&Client{}).DefaultPathConfigs, 200, "{}", nil,
		"victoriametrics", "/api/v1/query", "error")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(placeholder.Close)
	initial := request.GetResources(req)
	o := initial.BackendOptions
	o.OriginURL = ts.URL + basePath
	if err := o.Initialize("default"); err != nil {
		t.Fatal(err)
	}
	b, err := NewClient("default", o, nil, initial.CacheClient, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	o.HTTPClient = c.HTTPClient()
	o.Paths = c.DefaultPathConfigs(o)
	return &harness{t: t, origin: origin, client: c, rsc: initial}
}

func (h *harness) do(method, path string, v url.Values, hdr http.Header) *httptest.ResponseRecorder {
	h.t.Helper()
	var body io.Reader
	target := "http://trickster" + path
	if method == http.MethodPost {
		body = strings.NewReader(v.Encode())
	} else if len(v) > 0 {
		target += "?" + v.Encode()
	}
	r := httptest.NewRequest(method, target, body)
	maps.Copy(r.Header, hdr)
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	o := h.client.Configuration()
	pc := o.Paths.Match(method, path)
	if pc == nil {
		h.t.Fatalf("no route for %s %s", method, path)
	}
	r = request.SetResources(r, request.NewResources(o, pc, h.rsc.CacheConfig, h.rsc.CacheClient, h.client, h.rsc.Tracer))
	w := httptest.NewRecorder()
	h.client.Handlers()[pc.HandlerName].ServeHTTP(w, r)
	return w
}

func resultStatus(w *httptest.ResponseRecorder) (string, string) {
	return headers.ParseResultEngineStatus(w.Header().Get(headers.NameTricksterResult))
}

type matrixDoc struct {
	Status    string `json:"status"`
	IsPartial *bool  `json:"isPartial"`
	Data      struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func decodeMatrix(t *testing.T, w *httptest.ResponseRecorder) matrixDoc {
	t.Helper()
	var d matrixDoc
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return d
}

func rangeValues(query string, start time.Time, points int) url.Values {
	return url.Values{
		"query": {query}, "start": {secs(start)},
		"end": {secs(start.Add(time.Duration(points-1) * time.Minute))}, "step": {"60"},
	}
}

func TestDeltaProxyCache(t *testing.T) {
	h := newHarness(t, "")
	start := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	v := rangeValues("sum(rate(m[5m]))", start, 61)
	for i, want := range []string{"kmiss", "hit"} {
		w := h.do(http.MethodGet, "/api/v1/query_range", v, nil)
		if engine, status := resultStatus(w); engine != "DeltaProxyCache" || status != want {
			t.Fatalf("request %d: %s %s %s", i, engine, status, w.Body.String())
		}
		d := decodeMatrix(t, w)
		if len(d.Data.Result) != 1 || len(d.Data.Result[0].Values) != 61 || d.Data.Result[0].Metric["__name__"] != "m" {
			t.Fatalf("unexpected result %+v", d)
		}
	}
	// ten steps later, only the new points are fetched
	w := h.do(http.MethodGet, "/api/v1/query_range", rangeValues("sum(rate(m[5m]))", start.Add(10*time.Minute), 61), nil)
	if _, status := resultStatus(w); status != "phit" {
		t.Fatalf("got %s", status)
	}
	if f := h.origin.fetched(); len(f) != 2 || !strings.HasPrefix(f[1], strconv.FormatInt(start.Add(61*time.Minute).Unix(), 10)) {
		t.Errorf("fetched %v", f)
	}
	// an unaligned range of 50 or more points is served on VictoriaMetrics' aligned grid, from cache
	unaligned := rangeValues("sum(rate(m[5m]))", start.Add(17*time.Second), 61)
	w = h.do(http.MethodGet, "/api/v1/query_range", unaligned, nil)
	if _, status := resultStatus(w); status != "hit" {
		t.Fatalf("got %s", status)
	}
	if d := decodeMatrix(t, w); firstPoint(d) != start.Unix() {
		t.Errorf("first point %v", d.Data.Result[0].Values[0])
	}
	for _, want := range []string{"kmiss", "hit"} {
		if _, status := resultStatus(h.do(http.MethodPost, "/api/v1/query_range", v, nil)); status != want {
			t.Fatalf("POST: got %s want %s", status, want)
		}
	}
}

func firstPoint(d matrixDoc) int64 {
	if len(d.Data.Result) == 0 || len(d.Data.Result[0].Values) == 0 {
		return 0
	}
	f, _ := d.Data.Result[0].Values[0][0].(float64)
	return int64(f)
}

func TestShortRangeKeepsItsGrid(t *testing.T) {
	h := newHarness(t, "")
	start := time.Now().Add(-6 * time.Hour).Truncate(time.Hour).Add(17 * time.Second)
	w := h.do(http.MethodGet, "/api/v1/query_range", rangeValues("m", start, 10), nil)
	d := decodeMatrix(t, w)
	if first := firstPoint(d); first != start.Unix() {
		t.Errorf("a short range lost its own grid: first point %d", first)
	}
}

func TestObjectAndProxyShapes(t *testing.T) {
	h := newHarness(t, "")
	start := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	for i, want := range []string{"kmiss", "hit"} {
		w := h.do(http.MethodGet, "/api/v1/query_range", rangeValues("range_avg(m)", start, 61), nil)
		if engine, status := resultStatus(w); engine != "ObjectProxyCache" || status != want {
			t.Fatalf("request %d: %s %s", i, engine, status)
		}
	}
	for _, v := range []url.Values{
		rangeValues("now()", start, 61),
		func() url.Values { v := rangeValues("m", start, 61); v.Set("nocache", "1"); return v }(),
		func() url.Values { v := rangeValues("m", start, 61); v.Set("unknown", "1"); return v }(),
	} {
		w := h.do(http.MethodGet, "/api/v1/query_range", v, nil)
		if engine, _ := resultStatus(w); engine != "HTTPProxy" {
			t.Errorf("%v: served by %s", v, engine)
		}
	}
	// instant queries are cached as objects only once settled
	at := url.Values{"query": {"m"}, "time": {secs(start)}}
	for _, want := range []string{"kmiss", "hit"} {
		if _, status := resultStatus(h.do(http.MethodGet, "/api/v1/query", at, nil)); status != want {
			t.Fatalf("instant: got %s want %s", status, want)
		}
	}
	at.Set("time", secs(time.Now()))
	if engine, _ := resultStatus(h.do(http.MethodGet, "/api/v1/query", at, nil)); engine != "HTTPProxy" {
		t.Errorf("a live instant query was served by %s", engine)
	}
}

func TestPartialResponsesAreNotCached(t *testing.T) {
	h := newHarness(t, "/select/0/prometheus")
	h.origin.partial = true
	start := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	v := rangeValues("m", start, 61)
	for range 2 {
		w := h.do(http.MethodGet, "/api/v1/query_range", v, nil)
		if engine, _ := resultStatus(w); engine != "HTTPProxy" {
			t.Fatalf("a partial range was served by %s", engine)
		}
		if d := decodeMatrix(t, w); d.IsPartial == nil || !*d.IsPartial {
			t.Fatal("the partial indicator was lost")
		}
	}
	at := url.Values{"query": {"m"}, "time": {secs(start)}}
	for range 2 {
		w := h.do(http.MethodGet, "/api/v1/query", at, nil)
		if _, status := resultStatus(w); status != "kmiss" || w.Header().Get(headers.NameCacheControl) == "" {
			t.Fatalf("a partial instant response was stored: %s", status)
		}
	}
	// once complete, the cluster's isPartial is kept in responses served from cache
	h.origin.mu.Lock()
	h.origin.partial, h.origin.cluster = false, true
	h.origin.mu.Unlock()
	for _, want := range []string{"kmiss", "hit"} {
		w := h.do(http.MethodGet, "/api/v1/query_range", v, nil)
		d := decodeMatrix(t, w)
		if _, status := resultStatus(w); status != want || d.IsPartial == nil || *d.IsPartial {
			t.Fatalf("got %s, isPartial %v: %s", status, d.IsPartial, w.Body.String())
		}
	}
}

func TestGraphiteHandlers(t *testing.T) {
	h := newHarness(t, "/select/0/prometheus")
	past := time.Now().Add(-6 * time.Hour)
	render := url.Values{"target": {"a.b"}, "from": {secs(past.Add(-time.Hour))}, "until": {secs(past)}, "format": {"json"}}
	step := http.Header{"Storage-Step": {"10s"}}
	for _, path := range []string{"/render", "/graphite/render"} {
		for _, want := range []string{"kmiss", "hit"} {
			w := h.do(http.MethodPost, path, render, step)
			if _, status := resultStatus(w); status != want {
				t.Fatalf("%s: got %s want %s: %s", path, status, want, w.Body.String())
			}
		}
		render.Set("format", "json")
		render.Add("maxDataPoints", "10")
	}
	if got := h.origin.lastPath(); got != "/select/0/graphite/render" {
		t.Errorf("render reached %s", got)
	}
	// a POST form reaches the origin as sent, each target once
	for i, n := range h.origin.targets {
		if n != 1 {
			t.Errorf("render %d reached the origin with %d targets", i, n)
		}
	}
	// the Storage-Step header is part of the response's identity
	if _, status := resultStatus(h.do(http.MethodPost, "/render", render, http.Header{"Storage-Step": {"60s"}})); status != "kmiss" {
		t.Errorf("another Storage-Step was a %s", status)
	}
	render.Set("until", secs(time.Now()))
	if engine, _ := resultStatus(h.do(http.MethodGet, "/render", render, step)); engine != "HTTPProxy" {
		t.Errorf("a live render was served by %s", engine)
	}
	if _, status := resultStatus(h.do(http.MethodGet, "/metrics/find", url.Values{"query": {"a.*"}}, nil)); status != "kmiss" {
		t.Errorf("find: %s", status)
	}
	w := h.do(http.MethodPost, "/tags/tagSeries", url.Values{"path": {"a.b;c=d"}}, nil)
	if engine, _ := resultStatus(w); engine != "HTTPProxy" || h.origin.lastPath() != "/select/0/graphite/tags/tagSeries" {
		t.Errorf("tag registration: %s to %s", engine, h.origin.lastPath())
	}
}
