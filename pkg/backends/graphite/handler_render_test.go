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

package graphite

import (
	"context"
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

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/testutil/graphite/mockserver"
)

// harness is a graphite backend in front of a stub origin with a real
// memory cache, driven through the render handler exactly as the router
// would drive it
type harness struct {
	t      *testing.T
	stub   *mockserver.Server
	client *Client
	o      *bo.Options
	now    time.Time
	// fetches is the number of origin renders the last handler call made
	fetches int64
}

var harnessLadders = map[string]string{
	"dev.fast.cpu.host01.percent":     "10s:6h,60s:7d,10m:5y",
	"dev.fast.cpu.host02.percent":     "10s:6h,60s:7d,10m:5y",
	"dev.medium.orders.us-east.count": "60s:2d,5m:30d,1h:2y",
	"dev.coarse.users.active":         "5m:90d",
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	origin := mockserver.New()
	t.Cleanup(origin.Close)
	for p, r := range harnessLadders {
		origin.Add(p, r)
	}
	conf, err := config.Load([]string{"-origin-url", origin.URL, "-provider", "graphite"})
	if err != nil {
		t.Fatal(err)
	}
	caches := cr.LoadCachesFromConfig(conf)
	t.Cleanup(func() { cr.CloseCaches(caches) })
	o := conf.Backends["default"]
	if o.Graphite == nil {
		o.Graphite = gro.New()
	}
	// learn synchronously in tests: no background runs
	o.Graphite.ResolutionRegistry.Persist = false
	b, err := NewClient("default", o, nil, caches["default"], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	c.learner.Concurrency = -1
	t.Cleanup(c.Close)
	o.HTTPClient = c.HTTPClient()
	o.Paths = c.DefaultPathConfigs(o)
	return &harness{t: t, stub: origin, client: c, o: o,
		now: time.Now().Truncate(10 * time.Second)}
}

// learn learns every stub ladder synchronously
func (h *harness) learn(leaves ...string) {
	h.t.Helper()
	for _, l := range leaves {
		if _, err := h.client.learner.Learn(context.Background(), l, nil); err != nil {
			h.t.Fatalf("learn %s: %v", l, err)
		}
	}
}

func (h *harness) query(extra url.Values) url.Values {
	q := url.Values{"format": {"json"}, "now": {strconv.FormatInt(h.now.Unix(), 10)}, "until": {"-5min"}}
	maps.Copy(q, extra)
	return q
}

// render drives the handler with a GET and returns the recorder
func (h *harness) render(q url.Values) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://trickster/render?"+q.Encode(), nil)
	return h.serve(r)
}

func (h *harness) serve(r *http.Request) *httptest.ResponseRecorder {
	h.t.Helper()
	rsc := request.NewResources(h.o, h.o.Paths[0], h.client.Cache().Configuration(), h.client.Cache(), h.client, nil)
	r = request.SetResources(r, rsc)
	w := httptest.NewRecorder()
	before := h.stub.Renders.Load()
	h.client.RenderHandler(w, r)
	h.fetches = h.stub.Renders.Load() - before
	return w
}

// expectFetches asserts how many origin renders the last handler call made
func (h *harness) expectFetches(label string, n int64) {
	h.t.Helper()
	if h.fetches != n {
		h.t.Errorf("%s: expected %d origin fetches, got %d", label, n, h.fetches)
	}
}

// direct renders the same query at the stub origin
func (h *harness) direct(q url.Values) string {
	h.t.Helper()
	resp, err := http.Get(h.stub.URL + "/render?" + q.Encode())
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// same asserts the handler's response equals the origin's
func (h *harness) same(label string, q url.Values) *httptest.ResponseRecorder {
	h.t.Helper()
	w := h.render(q)
	want := h.direct(q)
	if w.Code != http.StatusOK {
		h.t.Errorf("%s: status %d: %s", label, w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != want {
		h.t.Errorf("%s differs\n got: %.400s\nwant: %.400s", label, got, want)
	}
	return w
}

func TestRenderColdThenAccelerated(t *testing.T) {
	h := newHarness(t)
	q := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-1h"}})
	// cold: unknown step, served through the object cache lane, identical
	// to the origin; the response teaches the step at this age (mode A)
	// while background discovery is refused in tests
	h.same("cold", q)
	key, _, ok := h.client.Registry().Leaf("dev.fast.cpu.host01.percent")
	if !ok || !strings.HasPrefix(key, "~") {
		t.Fatalf("expected a partial ladder learned from the response, got %q %t", key, ok)
	}
	h.learn("dev.fast.cpu.host01.percent")
	// warm: the delta proxy cache fetches once...
	h.same("first accelerated", q)
	h.expectFetches("first accelerated", 1)
	// ...and the same query is then a cache hit with no origin traffic
	h.same("cache hit", q)
	h.expectFetches("cache hit", 0)
	// every client format and option renders identically from the cache
	for _, extra := range []url.Values{
		{"format": {"raw"}}, {"format": {"csv"}}, {"format": {"msgpack"}},
		{"maxDataPoints": {"7"}}, {"maxDataPoints": {"1"}}, {"noNullPoints": {"1"}},
		{"pretty": {"1"}}, {"jsonp": {"cb"}}, {"tz": {"America/New_York"}, "format": {"csv"}},
	} {
		q2 := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-1h"}})
		maps.Copy(q2, extra)
		h.same("cached "+extra.Encode(), q2)
		h.expectFetches("cached "+extra.Encode(), 0)
	}
}

func TestRenderDeltaFetchAndRungPinning(t *testing.T) {
	h := newHarness(t)
	h.learn("dev.fast.cpu.host01.percent")
	target := url.Values{"target": {"dev.fast.cpu.host01.percent"}}
	// a window older than 6h is served from the 60s rung; widening it
	// fetches only the gaps, which must come back at the same rung even
	// though the gap's own from is young enough for the 10s rung
	h.same("8h", h.query(url.Values{"target": target["target"], "from": {"-8h"}}))
	h.expectFetches("8h", 1)
	h.same("9h", h.query(url.Values{"target": target["target"], "from": {"-9h"}}))
	h.expectFetches("9h gap", 1)
	if _, _, _, ok := renderQueryMismatch(h); ok {
		t.Error("a gap fetch must not mispredict")
	}
	if h.client.Registry().Generation() != 0 {
		t.Error("no misprediction may have occurred")
	}
	// a narrower window inside the cached range is a pure hit
	h.same("inside", h.query(url.Values{"target": target["target"], "from": {"-7h"}, "until": {"-5h"}}))
	h.expectFetches("inside", 0)
	// consolidation of a stitched range equals the origin's (7.6)
	h.same("stitched+consolidated", h.query(url.Values{"target": target["target"], "from": {"-9h"}, "maxDataPoints": {"37"}}))
	// the 10s rung is a separate cache entry (7.5): no cross-resolution reuse
	h.same("1h", h.query(url.Values{"target": target["target"], "from": {"-1h"}}))
	h.expectFetches("1h (different rung)", 1)
	h.same("8h again", h.query(url.Values{"target": target["target"], "from": {"-8h"}}))
	h.expectFetches("8h again (60s entry survives)", 0)
}

func renderQueryMismatch(h *harness) (string, time.Duration, time.Duration, bool) {
	q := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-9h"}})
	r := httptest.NewRequest(http.MethodGet, "http://trickster/render?"+q.Encode(), nil)
	rsc := request.NewResources(h.o, h.o.Paths[0], h.client.Cache().Configuration(), h.client.Cache(), h.client, nil)
	r = request.SetResources(r, rsc)
	h.client.RenderHandler(httptest.NewRecorder(), r)
	if rq := h.client.renderQueryOf(rsc); rq != nil {
		return rq.Mispredicted()
	}
	return "", 0, 0, false
}

func TestRenderMultiTarget(t *testing.T) {
	h := newHarness(t)
	h.learn("dev.fast.cpu.host01.percent", "dev.fast.cpu.host02.percent", "dev.coarse.users.active")
	// two targets on the same ladder: split, accelerated, merged in order
	q := h.query(url.Values{"target": {"dev.fast.cpu.host02.percent", "dev.fast.cpu.host01.percent"}, "from": {"-30min"}})
	h.same("split", q)
	h.expectFetches("split", 2)
	h.same("split hit", q)
	h.expectFetches("split hit", 0)
	// consolidation spans every series, as at the origin
	q.Set("maxDataPoints", "11")
	h.same("split consolidated", q)
	// a wildcard that expands to both
	h.same("wildcard", h.query(url.Values{"target": {"aliasByNode(dev.fast.cpu.*.percent, 3)"}, "from": {"-30min"}}))
	// targets on different ladders cannot share a step: the whole request
	// is served unaccelerated, still identical to the origin
	mixed := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent", "dev.coarse.users.active"}, "from": {"-30min"}})
	h.same("mixed ladders", mixed)
	// POST form requests work the same way
	body := q.Encode()
	r := httptest.NewRequest(http.MethodPost, "http://trickster/render", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := h.serve(r)
	if w.Code != http.StatusOK || w.Body.String() != h.direct(q) {
		t.Errorf("POST split: %d %.200s", w.Code, w.Body.String())
	}
}

func TestRenderFallbacks(t *testing.T) {
	h := newHarness(t)
	h.learn("dev.fast.cpu.host01.percent")
	// a function that is not allowlisted takes the object cache lane and
	// is identical to the origin
	h.same("movingAverage", h.query(url.Values{"target": {"movingAverage(dev.fast.cpu.host01.percent, '5min')"}, "from": {"-1h"}}))
	// image formats and unparsable targets too
	for _, q := range []url.Values{
		{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-1h"}, "format": {"raw"}, "maxStep": {"60"}},
		{"target": {"sumSeries("}, "from": {"-1h"}},
		{"target": {"no.such.metric"}, "from": {"-1h"}},
	} {
		w := h.render(h.query(q))
		if w.Body.String() != h.direct(h.query(q)) {
			t.Errorf("%v: fallback differs: %.200s", q, w.Body.String())
		}
	}
	// no target at all
	w := h.render(h.query(url.Values{"from": {"-1h"}}))
	if w.Body.String() != h.direct(h.query(url.Values{"from": {"-1h"}})) {
		t.Errorf("no target: %s", w.Body.String())
	}
}

func TestRenderMisprediction(t *testing.T) {
	h := newHarness(t)
	h.learn("dev.fast.cpu.host01.percent")
	q := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-1h"}})
	h.same("before", q)
	// the operator resizes the file: the learned ladder is now wrong
	h.stub.Remove("dev.fast.cpu.host01.percent")
	h.stub.Add("dev.fast.cpu.host01.percent", "30s:12h,5m:14d")
	gen := h.client.Registry().Generation()
	// a request for a not-yet-cached range fetches, detects the mismatch,
	// retracts the prediction and re-serves the origin's answer verbatim
	q2 := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-2h"}})
	h.same("mispredicted", q2)
	if h.client.Registry().Generation() != gen+1 {
		t.Errorf("a misprediction must bump the registry generation: %d -> %d", gen, h.client.Registry().Generation())
	}
	// nothing was cached under the wrong step: the next request, once the
	// ladder is relearned, fetches fresh and matches the origin
	h.learn("dev.fast.cpu.host01.percent")
	h.same("relearned", q2)
	h.same("relearned 1h", q)
}

func TestRenderLearnOnFirstResponse(t *testing.T) {
	h := newHarness(t)
	q := h.query(url.Values{"target": {"dev.medium.orders.us-east.count"}, "from": {"-90min"}})
	// cold: served unaccelerated, but the response teaches the step at
	// this age (decision D1, mode A)
	h.same("cold", q)
	key, conf, ok := h.client.Registry().Leaf("dev.medium.orders.us-east.count")
	if !ok || conf.String() != "exact" {
		t.Fatalf("expected a partial ladder from the response, got %q %v %t", key, conf, ok)
	}
	// the same age is now accelerated without a background learn
	h.same("warm", q)
	h.expectFetches("warm", 1)
	h.same("hit", q)
	h.expectFetches("hit", 0)
	// an age the partial ladder cannot answer is still unaccelerated
	h.same("other age", h.query(url.Values{"target": {"dev.medium.orders.us-east.count"}, "from": {"-10d"}}))
	// a consolidated response must never teach a step: its timestamps are
	// a multiple of the native step
	h.same("consolidated cold", h.query(url.Values{"target": {"dev.coarse.users.active"}, "from": {"-1d"}, "maxDataPoints": {"10"}}))
	if _, _, ok := h.client.Registry().Leaf("dev.coarse.users.active"); ok {
		t.Error("a consolidated response must not be learned from")
	}
}

// TestRenderConcurrentRenderingVariants is the regression test for
// rendering options leaking between concurrent requests. maxDataPoints,
// format and noNullPoints are applied at marshal time and are deliberately
// not in the cache key, so the delta cache collapses these requests into a
// single upstream fetch; each must still be rendered for itself
// (timeseries.RequestOptions.MarshalVariesByRequest).
func TestRenderConcurrentRenderingVariants(t *testing.T) {
	h := newHarness(t)
	h.learn("dev.fast.cpu.host01.percent")
	// hold each origin response so the requests below overlap in flight
	h.stub.Delay.Store(int64(300 * time.Millisecond))

	variants := []struct {
		label string
		extra url.Values
	}{
		{"maxDataPoints=10", url.Values{"maxDataPoints": {"10"}}},
		{"maxDataPoints=1000", url.Values{"maxDataPoints": {"1000"}}},
		{"format=csv", url.Values{"format": {"csv"}}},
		{"noNullPoints", url.Values{"noNullPoints": {"1"}}},
		{"plain", nil},
	}
	queries := make([]url.Values, len(variants))
	for i, v := range variants {
		q := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-3h"}})
		maps.Copy(q, v.extra)
		queries[i] = q
	}

	got := make([]string, len(variants))
	before := h.stub.Renders.Load()
	var wg sync.WaitGroup
	for i := range variants {
		wg.Go(func() {
			r := httptest.NewRequest(http.MethodGet, "http://trickster/render?"+queries[i].Encode(), nil)
			rsc := request.NewResources(h.o, h.o.Paths[0], h.client.Cache().Configuration(),
				h.client.Cache(), h.client, nil)
			r = request.SetResources(r, rsc)
			w := httptest.NewRecorder()
			h.client.RenderHandler(w, r)
			got[i] = w.Body.String()
		})
	}
	wg.Wait()
	fetches := h.stub.Renders.Load() - before
	h.stub.Delay.Store(0)

	// the test is only meaningful if the requests really did collapse
	if fetches > 2 {
		t.Errorf("expected the requests to collapse into one upstream fetch, got %d", fetches)
	}
	for i, v := range variants {
		if want := h.direct(queries[i]); got[i] != want {
			t.Errorf("%s got another request's rendering\n got: %.200s\nwant: %.200s", v.label, got[i], want)
		}
	}
}
