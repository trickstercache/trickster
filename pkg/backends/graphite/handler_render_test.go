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
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/testutil/graphite/mockserver"
)

// a graphite backend in front of a stub origin with a real memory cache,
// driven through the render handler exactly as the router would drive it
type harness struct {
	t      testing.TB
	stub   *mockserver.Server
	client *Client
	o      *bo.Options
	now    time.Time
	// fetches is the number of origin renders the last handler call made,
	// and lastRQ the render query it produced
	fetches int64
	lastRQ  *RenderQuery
	// originURL is where direct() sends its comparison requests
	originURL string
}

var harnessLadders = map[string]string{
	"dev.fast.cpu.host01.percent":     "10s:6h,60s:7d,10m:5y",
	"dev.fast.cpu.host02.percent":     "10s:6h,60s:7d,10m:5y",
	"dev.medium.orders.us-east.count": "60s:2d,5m:30d,1h:2y",
	"dev.coarse.users.active":         "5m:90d",
}

func newHarness(t testing.TB, mods ...func(*bo.Options)) *harness {
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
	// the delta path buffers a whole response to model it, and graphite
	// fetches at native resolution: size this as a real deployment must
	o.MaxObjectSizeBytes = 64 * 1024 * 1024
	o.TimeseriesRetentionFactor = 1000000
	for _, mod := range mods {
		mod(o)
	}
	b, err := NewClient("default", o, nil, caches["default"], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	c.learner.Concurrency = -1
	t.Cleanup(c.Close)
	o.HTTPClient = c.HTTPClient()
	o.Paths = c.DefaultPathConfigs(o)
	return &harness{t: t, stub: origin, client: c, o: o, originURL: origin.URL,
		now: time.Now().Truncate(10 * time.Second)}
}

func newLiveHarness(t *testing.T) *harness {
	t.Helper()
	base := os.Getenv("GRAPHITE_WEB_URL")
	if base == "" {
		t.Skip("GRAPHITE_WEB_URL not set")
	}
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	conf, err := config.Load([]string{"-origin-url", base, "-provider", "graphite"})
	if err != nil {
		t.Fatal(err)
	}
	caches := cr.LoadCachesFromConfig(conf)
	t.Cleanup(func() { cr.CloseCaches(caches) })
	o := conf.Backends["default"]
	if o.Graphite == nil {
		o.Graphite = gro.New()
	}
	o.Graphite.ResolutionRegistry.Persist = false
	b, err := NewClient("default", o, nil, caches["default"], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	t.Cleanup(c.Close)
	o.HTTPClient = c.HTTPClient()
	o.Paths = c.DefaultPathConfigs(o)
	return &harness{t: t, client: c, o: o, originURL: base,
		now: time.Now().Add(-time.Minute).Truncate(10 * time.Second)}
}

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
	var before int64
	if h.stub != nil {
		before = h.stub.Renders.Load()
	}
	h.client.RenderHandler(w, r)
	if h.stub != nil {
		h.fetches = h.stub.Renders.Load() - before
	}
	h.lastRQ = h.client.renderQueryOf(rsc)
	return w
}

func (h *harness) expectFetches(label string, n int64) {
	h.t.Helper()
	if h.fetches != n {
		h.t.Errorf("%s: expected %d origin fetches, got %d", label, n, h.fetches)
	}
}

func (h *harness) direct(q url.Values) string {
	h.t.Helper()
	resp, err := http.Get(h.originURL + "/render?" + q.Encode())
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// renders through the handler and requires the body to equal the origin's
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
	// cold: unknown step, served through the object cache lane identical to
	// the origin; the response itself teaches the step at this age
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
	// a window older than 6h serves from the 60s rung; widening fetches only
	// the gaps, which must stay on that rung despite their younger from
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
	// consolidation of a stitched range equals the origin's
	h.same("stitched+consolidated", h.query(url.Values{"target": target["target"], "from": {"-9h"}, "maxDataPoints": {"37"}}))
	// the 10s rung is a separate cache entry: no cross-resolution reuse
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
	h.same("wildcard", h.query(url.Values{"target": {"aliasByNode(dev.fast.cpu.*.percent, 3)"}, "from": {"-30min"}}))
	h.learn("dev.fast.cpu.host01.percent", "dev.fast.cpu.host02.percent")
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
	// this age
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

func TestLearnOnlyFromTransparentTargets(t *testing.T) {
	h := newHarness(t)
	const leaf = "dev.medium.orders.us-east.count"
	// a summarized response: the fallback reason is function_not_allowlisted,
	// not unknown_step, so it never reaches the learner
	h.same("smartSummarize", h.query(url.Values{"target": {"smartSummarize(" + leaf + ", '1h')"}, "from": {"-6h"}}))
	if key, conf, ok := h.client.Registry().Leaf(leaf); ok {
		t.Errorf("a summarized response must not teach a step (learned %q at %v)", key, conf)
	}
	// an empty response (here: the mock does not evaluate functions, so the
	// target matches nothing) states no step and must teach nothing
	h.same("empty", h.query(url.Values{"target": {"aliasByNode(dev.fast.cpu.host02.percent, 3)"}, "from": {"-90min"}}))
	if _, _, ok := h.client.Registry().Leaf("dev.fast.cpu.host02.percent"); ok {
		t.Error("an empty response must not teach a step")
	}
	// a bare path does teach it
	h.same("bare path", h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-90min"}}))
	if _, _, ok := h.client.Registry().Leaf("dev.fast.cpu.host01.percent"); !ok {
		t.Error("a step-bearing response must teach the step")
	}
}

func TestRenderOversizeDegrades(t *testing.T) {
	h := newHarness(t, func(o *bo.Options) { o.MaxObjectSizeBytes = 2048 })
	h.learn("dev.fast.cpu.host01.percent")
	// a window whose native-resolution response is far larger than 2KB
	q := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-3h"}})
	w := h.render(q)
	if w.Code != http.StatusOK {
		t.Fatalf("an oversize response must degrade, not fail: status %d", w.Code)
	}
	if want := h.direct(q); w.Body.String() != want {
		t.Errorf("degraded response differs from the origin\n got: %.200s\nwant: %.200s", w.Body.String(), want)
	}
	// and a small window on the same target is still accelerated
	small := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-6min"}})
	h.same("small window", small)
	if h.lastRQ.Fallback != "" {
		t.Errorf("a small response must still be accelerated, got fallback %q", h.lastRQ.Fallback)
	}
}

func TestRenderTruncatedCaptureNotServed(t *testing.T) {
	h := newHarness(t, func(o *bo.Options) {
		// far below the size of a 6h window at 10s resolution (~2160 points)
		o.MaxCaptureBytes = 512
	})
	h.learn("dev.fast.cpu.host01.percent")
	q := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-6h"}})
	for pass := range 2 {
		got := h.render(q)
		if got.Code != http.StatusOK {
			t.Fatalf("pass %d: status %d", pass, got.Code)
		}
		want := h.direct(q)
		if len(want) <= 512 {
			t.Fatalf("vacuous: origin body is only %d bytes", len(want))
		}
		if got.Body.String() != want {
			t.Errorf("pass %d: truncated capture leaked: got %d bytes, want %d",
				pass, got.Body.Len(), len(want))
		}
		if cl := got.Header().Get("Content-Length"); cl != "" {
			if n, _ := strconv.Atoi(cl); n != got.Body.Len() {
				t.Errorf("pass %d: Content-Length %s does not match body %d", pass, cl, got.Body.Len())
			}
		}
	}
}

func TestRenderSplitColdIsOneFallback(t *testing.T) {
	h := newHarness(t)
	// one target learned, one cold: the request is not fully accelerable
	h.learn("dev.fast.cpu.host01.percent")
	q := h.query(url.Values{
		"target": {"dev.fast.cpu.host01.percent", "dev.fast.cpu.host02.percent"},
		"from":   {"-30min"},
	})
	w := h.same("cold split", q)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	h.expectFetches("cold split", 1)
	// entirely cold is also one fetch
	h2 := newHarness(t)
	q2 := h2.query(url.Values{
		"target": {"dev.fast.cpu.host01.percent", "dev.fast.cpu.host02.percent"},
		"from":   {"-30min"},
	})
	h2.same("all cold", q2)
	h2.expectFetches("all cold", 1)
}

func TestRenderSplitSharesReferenceTime(t *testing.T) {
	h := newHarness(t)
	h.learn("dev.fast.cpu.host01.percent", "dev.fast.cpu.host02.percent")
	// each clock reading advances a full step, so an unpinned member parse
	// lands in a different newest bucket than the preflight
	base := h.now
	var reads atomic.Int64
	h.client.timeNow = func() time.Time {
		return base.Add(time.Duration(reads.Add(1)-1) * 10 * time.Second)
	}
	q := h.query(url.Values{
		"target": {"dev.fast.cpu.host01.percent", "dev.fast.cpu.host02.percent"},
		"from":   {"-10min"}, "until": {"now"},
	})
	q.Del("now") // relative resolution is the case under test
	w := h.render(q)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var series []struct {
		Target     string        `json:"target"`
		Datapoints [][2]*float64 `json:"datapoints"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &series); err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("expected 2 series, got %d", len(series))
	}
	for i, s := range series {
		if len(s.Datapoints) == 0 {
			t.Fatalf("series %d empty", i)
		}
	}
	firstStart, firstEnd := *series[0].Datapoints[0][1], *series[0].Datapoints[len(series[0].Datapoints)-1][1]
	secondStart, secondEnd := *series[1].Datapoints[0][1], *series[1].Datapoints[len(series[1].Datapoints)-1][1]
	if firstStart != secondStart || firstEnd != secondEnd {
		t.Errorf("members resolved different extents: [%v..%v] vs [%v..%v] after %d clock reads",
			firstStart, firstEnd, secondStart, secondEnd, reads.Load())
	}
	if reads.Load() < 2 {
		t.Fatal("vacuous: the clock was not read by multiple parses")
	}
}

func TestRenderBodyLimits(t *testing.T) {
	h := newHarness(t)
	// declared oversized: rejected on Content-Length alone
	r := httptest.NewRequest(http.MethodPost, "http://trickster/render",
		strings.NewReader("target=a.b"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ContentLength = maxRenderBodyBytes + 1
	w := h.serve(r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("declared oversize: status %d", w.Code)
	}
	// chunked oversized: no Content-Length, the reader is bounded instead
	big := strings.NewReader("target=" + strings.Repeat("a", maxRenderBodyBytes+1024))
	r = httptest.NewRequest(http.MethodPost, "http://trickster/render", big)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ContentLength = -1
	w = h.serve(r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked oversize: status %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "http://trickster/render",
		strings.NewReader("target=a.b"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rsc := request.NewResources(h.o, h.o.Paths[0], h.client.Cache().Configuration(),
		h.client.Cache(), h.client, nil)
	rsc.RequestBody = []byte("target=" + strings.Repeat("a", maxRenderBodyBytes+1024))
	r = request.SetResources(r, rsc)
	w = httptest.NewRecorder()
	h.client.RenderHandler(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("cached oversize: status %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "http://trickster/render?target=a.b",
		strings.NewReader(strings.Repeat("a", maxRenderBodyBytes+1024)))
	r.ContentLength = -1
	w = h.serve(r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked GET oversize: status %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "http://trickster/render?"+url.Values{
		"target": {"dev.fast.cpu.host01.percent"}, "from": {"-30min"},
		"format": {"json"}, "now": {strconv.FormatInt(h.now.Unix(), 10)},
	}.Encode(), strings.NewReader("ignored"))
	r.ContentLength = -1
	rsc = request.NewResources(h.o, h.o.Paths[0], h.client.Cache().Configuration(),
		h.client.Cache(), h.client, nil)
	r = request.SetResources(r, rsc) // handler sees this exact request
	w = httptest.NewRecorder()
	h.client.RenderHandler(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("GET with a small body: status %d: %.200s", w.Code, w.Body.String())
	}
	if r.Body != http.NoBody || r.ContentLength != 0 {
		t.Error("a GET body must be discarded, not forwarded")
	}

	// an ordinary POST still works
	r = postReq(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-30min"},
		"format": {"json"}, "now": {strconv.FormatInt(h.now.Unix(), 10)}})
	if w := h.serve(r); w.Code != http.StatusOK {
		t.Errorf("ordinary POST: status %d: %.200s", w.Code, w.Body.String())
	}
}

func TestRenderOnePointStepSafety(t *testing.T) {
	const leaf = "dev.fast.cpu.host01.percent"

	t.Run("resize detected on a one-bucket gap", func(t *testing.T) {
		h := newHarness(t)
		h.learn(leaf)
		q := h.query(url.Values{"target": {leaf}, "from": {"-30min"}})
		h.same("warm", q)
		h.stub.Remove(leaf)
		h.stub.Add(leaf, "60s:2d,5m:30d")
		wide := h.query(url.Values{"target": {leaf}, "from": {"-30min"}})
		until := h.now.Add(-5 * time.Minute).Add(10 * time.Second)
		wide.Set("until", strconv.FormatInt(until.Unix(), 10))

		w := h.render(wide)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %.200s", w.Code, w.Body.String())
		}
		if got, want := w.Body.String(), h.direct(wide); got != want {
			t.Errorf("post-resize response differs from the origin\n got: %.200s\nwant: %.200s", got, want)
		}
		// the retraction bumped the generation; relearned, the metric is
		// accelerated again at its new step
		h.learn(leaf)
		fresh := h.query(url.Values{"target": {leaf}, "from": {"-30min"}})
		h.same("relearned", fresh)
		if h.lastRQ == nil || h.lastRQ.Fallback != "" {
			t.Errorf("relearned render must be accelerated, got fallback %q", h.lastRQ.Fallback)
		}
	})

	t.Run("one-bucket gap stays accelerated at no extra cost", func(t *testing.T) {
		h := newHarness(t)
		h.learn(leaf)
		q := h.query(url.Values{"target": {leaf}, "from": {"-30min"}})
		h.same("warm", q)

		grow := func(offset time.Duration) url.Values {
			v := h.query(url.Values{"target": {leaf}, "from": {"-30min"}})
			v.Set("until", strconv.FormatInt(h.now.Add(-5*time.Minute).Add(offset).Unix(), 10))
			return v
		}
		// the widened fetch is one origin request, not a fetch plus any
		// verification traffic, and the result is delta-cached
		h.same("first gap", grow(10*time.Second))
		h.expectFetches("first gap", 1)
		if h.lastRQ == nil || h.lastRQ.Fallback != "" {
			t.Fatalf("gap fill must stay accelerated, got fallback %q", h.lastRQ.Fallback)
		}
		h.same("gap hit", grow(10*time.Second))
		h.expectFetches("gap hit", 0)
		h.same("second gap", grow(20*time.Second))
		h.expectFetches("second gap", 1)
	})

	t.Run("unprovable response is refused and served unaccelerated", func(t *testing.T) {
		h := newHarness(t)
		h.learn(leaf)
		q := h.query(url.Values{"target": {leaf}, "from": {"-30min"}})
		h.same("warm", q)
		h.stub.Remove(leaf)
		wide := h.query(url.Values{"target": {leaf}, "from": {"-30min"}})
		wide.Set("until", strconv.FormatInt(h.now.Add(-5*time.Minute).Add(10*time.Second).Unix(), 10))
		w := h.render(wide)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %.200s", w.Code, w.Body.String())
		}
		if got, want := w.Body.String(), h.direct(wide); got != want {
			t.Errorf("refused-fetch response differs from the origin\n got: %.200s\nwant: %.200s", got, want)
		}
	})
}

func BenchmarkRenderCacheHit(b *testing.B) {
	for _, tc := range []struct {
		name string
		from string
	}{
		{"6h_2160pts", "-6h"},
		{"7d_60480pts", "-168h"},
		{"4y_210kpts", "-35040h"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			h := newHarness(b)
			h.learn("dev.fast.cpu.host01.percent")
			q := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {tc.from}})
			// warm the cache
			w := h.render(q)
			if w.Code != http.StatusOK {
				b.Fatalf("warm: status %d", w.Code)
			}
			before := h.stub.Renders.Load()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				w := h.render(q)
				if w.Code != http.StatusOK {
					b.Fatalf("status %d", w.Code)
				}
			}
			b.StopTimer()
			if h.stub.Renders.Load() != before {
				b.Fatalf("cache hits must not fetch: %d origin renders", h.stub.Renders.Load()-before)
			}
		})
	}
}

func TestAmbiguityInvalidatesAndRelearns(t *testing.T) {
	const leaf = "dev.fast.cpu.host01.percent"
	h := newHarness(t)
	h.learn(leaf)
	q := h.query(url.Values{"target": {leaf}, "from": {"-30min"}})
	h.same("warm", q)

	// the metric disappears; the widened gap fetch returns an empty body
	h.stub.Remove(leaf)
	gap := func(offset time.Duration) url.Values {
		v := h.query(url.Values{"target": {leaf}, "from": {"-30min"}})
		v.Set("until", strconv.FormatInt(h.now.Add(-5*time.Minute).Add(offset).Unix(), 10))
		return v
	}
	// first request: one doomed accelerated fetch, then the object-lane
	// fetch — and the leaf binding is invalidated
	h.same("first ambiguous", gap(10*time.Second))
	h.expectFetches("first ambiguous", 2)
	if _, _, ok := h.client.registry.Leaf(leaf); ok {
		t.Fatal("the leaf binding must be invalidated on ambiguity")
	}
	h.same("second request", gap(20*time.Second))
	h.expectFetches("second request", 1)
	if h.lastRQ == nil || h.lastRQ.Fallback != parsing.ReasonUnknownStep {
		t.Fatalf("expected an unknown_step fallback, got %q", h.lastRQ.Fallback)
	}

	// and the state recovers: the metric returns, a relearn re-establishes
	// the ladder, and acceleration resumes
	h.stub.Add(leaf, "10s:6h,60s:7d,10m:5y")
	h.learn(leaf)
	h.same("recovered", h.query(url.Values{"target": {leaf}, "from": {"-30min"}}))
	if h.lastRQ == nil || h.lastRQ.Fallback != "" {
		t.Fatalf("recovered render must be accelerated, got %q", h.lastRQ.Fallback)
	}
}

func TestLocalParamDeclinesAcceleration(t *testing.T) {
	h := newHarness(t)
	h.learn("dev.fast.cpu.host01.percent", "dev.fast.cpu.host02.percent")
	// two origin views, same step, different leaves: local=1 hides host02
	inner := h.stub.Server.Config.Handler
	h.stub.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("local") == "1" {
			for _, tgt := range r.Form["target"] {
				if strings.Contains(tgt, "host02") {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte("[]"))
					return
				}
			}
		}
		inner.ServeHTTP(w, r)
	})

	wild := h.query(url.Values{"target": {"dev.fast.cpu.*.percent"}, "from": {"-30min"}})
	// the global view accelerates
	h.same("global view", wild)
	if h.lastRQ == nil || h.lastRQ.Fallback != "" {
		t.Fatalf("global view must accelerate, got %q", h.lastRQ.Fallback)
	}
	// the local view declines — and is still byte-identical to its origin
	local := h.query(url.Values{"target": {"dev.fast.cpu.host02.percent"}, "from": {"-30min"}})
	local.Set("local", "1")
	h.same("local view", local)
	if h.lastRQ == nil || h.lastRQ.Fallback != parsing.ReasonClientIdentity {
		t.Fatalf("local view must decline as client_identity, got %q", h.lastRQ.Fallback)
	}
	// the pin must cover the synthetic resolution paths too, or the mixed
	// identities would decline acceleration as resolution_identity
	for _, pc := range h.o.Paths {
		if pc.Path == "/render" || pc.Path == "/metrics/expand" {
			pc.RequestParams = map[string]string{"local": "1"}
			pc.RefreshIdentityKeyPart()
		}
	}
	h.client.synthIDs.Store(nil)
	pinned := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-30min"}})
	pinned.Set("local", "1")
	w := h.render(pinned)
	if w.Code != http.StatusOK {
		t.Fatalf("pinned: status %d", w.Code)
	}
	if h.lastRQ == nil {
		t.Fatal("no render query recorded for the pinned request")
	}
	if h.lastRQ.Fallback != "" {
		t.Fatalf("statically pinned local must accelerate, got %q", h.lastRQ.Fallback)
	}

	// every variant of the replaced parameter shares the pinned request's
	// cache entry, so after the one origin fetch each is a zero-fetch hit
	for _, v := range []string{"1", "0", "2", "junk", ""} {
		vq := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-30min"}})
		if v != "" {
			vq.Set("local", v)
		}
		h.same("pinned variant local="+v, vq)
		h.expectFetches("pinned variant local="+v, 0)
		if h.lastRQ == nil || h.lastRQ.Fallback != "" {
			t.Fatalf("pinned variant local=%s must accelerate, got %q", v, h.lastRQ.Fallback)
		}
	}
}

func TestRenderTZBudgetFailsClosed(t *testing.T) {
	h := newHarness(t)
	const leaf = "dev.fast.cpu.host01.percent"
	h.learn(leaf)

	// freeze the token bucket's clock so the budget cannot refill mid-test,
	// then spend it all on distinct hostile names
	frozen := time.Now()
	h.client.tzCache.mu.Lock()
	h.client.tzCache.now = func() time.Time { return frozen }
	h.client.tzCache.mu.Unlock()
	for i := range tzLoadBurst + 8 {
		h.client.tzCache.get(fmt.Sprintf("Not/Hostile%d", i))
	}

	// the next request for a valid uncached zone, with a date-anchored
	// range, is served through the object lane byte-identical to the origin
	q := h.query(url.Values{"target": {leaf}, "from": {"midnight"},
		"tz": {"America/Chicago"}})
	h.same("post-exhaustion date-anchored render", q)
	h.expectFetches("post-exhaustion date-anchored render", 1)
	if h.lastRQ == nil || h.lastRQ.Fallback != parsing.ReasonTZUnavailable {
		t.Fatalf("expected a %s fallback, got %q",
			parsing.ReasonTZUnavailable, h.lastRQ.Fallback)
	}
	// the undetermined zone was not cached as invalid
	if _, res := h.client.tzCache.get("America/Chicago"); res != tzUnavailable {
		t.Fatalf("an undetermined zone must not be cached, got %v", res)
	}

	// once the budget refills, the zone loads and the request accelerates
	h.client.tzCache.mu.Lock()
	h.client.tzCache.now = func() time.Time { return frozen.Add(time.Minute) }
	h.client.tzCache.mu.Unlock()
	q2 := h.query(url.Values{"target": {leaf}, "from": {"-30min"},
		"tz": {"America/Chicago"}})
	h.same("post-refill render", q2)
	if h.lastRQ == nil || h.lastRQ.Fallback != "" {
		t.Fatalf("a refilled budget must restore acceleration, got %q", h.lastRQ.Fallback)
	}
}
