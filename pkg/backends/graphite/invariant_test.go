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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

// TestCorrectnessInvariant is the check that makes "never returns incorrect
// data" (design note §8) a verified claim rather than a design intention:
// over a corpus spanning all four confidence levels, every archive
// boundary, the retention edge, and every client rendering option, the
// response Trickster serves must be byte-identical to what the origin
// would have returned unproxied — cold, warm and cached.
func TestCorrectnessInvariant(t *testing.T) {
	h := newHarness(t, func(o *bo.Options) {
		o.Graphite.StaticRetentions = []gro.StaticRetention{
			{Pattern: `^cfg\.`, Retentions: "60s:2d,5m:30d"},
		}
	})
	h.stub.Add("cfg.a", "60s:2d,5m:30d")
	h.stub.Add("unlearned.a", "10s:6h,60s:7d")
	h.learn("dev.fast.cpu.host01.percent", "dev.fast.cpu.host02.percent",
		"dev.medium.orders.us-east.count", "dev.coarse.users.active")

	// Targets, with the confidence each is expected to earn. The accelerated
	// rows are path expressions rather than function chains because the mock
	// origin does not evaluate functions and would answer them with an empty
	// body, making the comparison vacuous; function chains over a real
	// graphite-web are covered by TestCorrectnessInvariantAgainstGraphiteWeb.
	targets := []struct {
		target string
		want   resolution.Confidence
	}{
		{"dev.fast.cpu.host01.percent", resolution.Exact},
		{"dev.medium.orders.us-east.count", resolution.Exact},
		{"dev.coarse.users.active", resolution.Exact},
		{"dev.fast.cpu.*.percent", resolution.Derived},
		{"dev.fast.cpu.{host01,host02}.percent", resolution.Derived},
		{"cfg.a", resolution.Configured},
		// the unaccelerated lane: unallowlisted functions, generators and
		// absent metrics, which the object cache proxies verbatim
		{"movingAverage(dev.fast.cpu.host01.percent, '5min')", resolution.Unknown},
		{"highestMax(dev.fast.cpu.*.percent, 1)", resolution.Unknown},
		{"nonexistent.*", resolution.Unknown},
		{"constantLine(5)", resolution.Unknown},
	}
	// windows: inside the finest rung, across an archive boundary, deep in
	// a coarse rung, and past the retention edge
	windows := []struct{ from, until string }{
		{"-30min", "-5min"},
		{"-8h", "-5min"},
		{"-3d", "-1d"},
		{"-120d", "-5min"},
		{"-6h", "-5h"},
	}
	// every rendering option that is applied at marshal time
	variants := []struct {
		label string
		extra url.Values
	}{
		{"json", nil},
		{"raw", url.Values{"format": {"raw"}}},
		{"csv", url.Values{"format": {"csv"}}},
		{"msgpack", url.Values{"format": {"msgpack"}}},
		{"maxDataPoints=7", url.Values{"maxDataPoints": {"7"}}},
		{"maxDataPoints=1", url.Values{"maxDataPoints": {"1"}}},
		{"maxDataPoints=1000", url.Values{"maxDataPoints": {"1000"}}},
		{"noNullPoints", url.Values{"noNullPoints": {"1"}}},
		{"pretty", url.Values{"pretty": {"1"}}},
		{"jsonp", url.Values{"jsonp": {"cb"}}},
		{"tz", url.Values{"format": {"csv"}, "tz": {"America/New_York"}}},
	}

	seen := map[resolution.Confidence]int{}
	var checked int
	for _, tg := range targets {
		for _, w := range windows {
			for _, v := range variants {
				q := h.query(url.Values{"target": {tg.target}, "from": {w.from}, "until": {w.until}})
				for k, vals := range v.extra {
					q[k] = vals
				}
				label := tg.target + " " + w.from + ".." + w.until + " " + v.label
				// serve it twice: the second is a cache hit, and a cached
				// response must be just as faithful as a fresh one
				for pass := range 2 {
					got := h.render(q)
					if got.Code != http.StatusOK {
						t.Fatalf("%s (pass %d): status %d", label, pass, got.Code)
					}
					want := h.direct(q)
					if got.Body.String() != want {
						t.Errorf("%s (pass %d) differs from the origin\n got: %.240s\nwant: %.240s",
							label, pass, got.Body.String(), want)
					}
					// an accelerated row that returns nothing would compare
					// two empty bodies and prove nothing
					if tg.want != resolution.Unknown && v.label == "json" &&
						!strings.Contains(want, "datapoints") {
						t.Errorf("%s: the origin returned no data, so this row is vacuous: %.120s", label, want)
					}
					if pass == 0 {
						if h.lastRQ == nil {
							t.Fatalf("%s: no render query", label)
						}
						if h.lastRQ.Confidence != tg.want {
							t.Errorf("%s: confidence %v, want %v", label, h.lastRQ.Confidence, tg.want)
						}
						seen[h.lastRQ.Confidence]++
					}
					checked++
				}
			}
		}
	}
	// the corpus is only meaningful if it actually reached every lane
	for _, c := range []resolution.Confidence{resolution.Exact, resolution.Derived,
		resolution.Configured, resolution.Unknown} {
		if seen[c] == 0 {
			t.Errorf("no corpus entry resolved to %v", c)
		}
	}
	t.Logf("%d responses verified byte-identical to the origin; confidence mix: "+
		"exact=%d derived=%d configured=%d unknown=%d", checked,
		seen[resolution.Exact], seen[resolution.Derived], seen[resolution.Configured],
		seen[resolution.Unknown])
}

// TestObjectLaneKeyStability confirms that the unaccelerated lane's cache
// key is stable under the parameter ordering a client happens to use, so
// that a Grafana panel and a hand-built URL for the same render share one
// cached object (plan item 8.2)
func TestObjectLaneKeyStability(t *testing.T) {
	h := newHarness(t)
	h.learn("dev.fast.cpu.host01.percent")
	// an unallowlisted function: served by the object cache lane
	const target = "movingAverage(dev.fast.cpu.host01.percent, '5min')"
	base := h.query(url.Values{"target": {target}, "from": {"-1h"}, "maxDataPoints": {"500"}})

	// the same parameters in three different orders
	orders := [][]string{
		{"target", "from", "until", "now", "format", "maxDataPoints"},
		{"maxDataPoints", "format", "now", "until", "from", "target"},
		{"now", "target", "maxDataPoints", "until", "format", "from"},
	}
	var first string
	for i, order := range orders {
		var b strings.Builder
		for j, k := range order {
			if j > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(k) + "=" + url.QueryEscape(base.Get(k)))
		}
		r := httptest.NewRequest(http.MethodGet, "http://trickster/render?"+b.String(), nil)
		w := h.serve(r)
		if w.Code != http.StatusOK {
			t.Fatalf("order %d: status %d", i, w.Code)
		}
		if i == 0 {
			first = w.Body.String()
			continue
		}
		if w.Body.String() != first {
			t.Errorf("order %d returned a different body", i)
		}
		if h.fetches != 0 {
			t.Errorf("order %d re-fetched from the origin: the object cache key is order-dependent", i)
		}
	}

	// and the same for a POST form body, which is how Grafana sends renders
	form := url.Values{"target": {target}, "from": {base.Get("from")}, "until": {base.Get("until")},
		"now": {base.Get("now")}, "format": {"json"}, "maxDataPoints": {"500"}}
	for i, order := range orders {
		var b strings.Builder
		for j, k := range order {
			if j > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(k) + "=" + url.QueryEscape(form.Get(k)))
		}
		r := httptest.NewRequest(http.MethodPost, "http://trickster/render", strings.NewReader(b.String()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := h.serve(r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST order %d: status %d", i, w.Code)
		}
		if i > 0 && h.fetches != 0 {
			t.Errorf("POST order %d re-fetched from the origin: the key is order-dependent", i)
		}
	}
}

// TestCorrectnessInvariantAgainstGraphiteWeb runs the same invariant over
// function chains, wildcards and the developer environment's real ladders,
// against a live graphite-web (GRAPHITE_WEB_URL, http://127.0.0.1:8081 in
// the dev env). It is the counterpart to TestCorrectnessInvariant, which
// cannot cover function targets because the mock origin does not evaluate
// them.
func TestCorrectnessInvariantAgainstGraphiteWeb(t *testing.T) {
	h := newLiveHarness(t)
	targets := []string{
		"dev.fast.cpu.host01.percent",
		"dev.fast.cpu.*.percent",
		"aliasByNode(dev.fast.requests.*.count, 3)",
		"alias(sumSeries(dev.fast.requests.*.count), 'total')",
		"scale(offset(dev.medium.queue.orders.depth, 1), 2)",
		"aliasByNode(dev.drift.temperature.*.celsius, 3)",
		"dev.coarse.storage.bucket-a.bytes",
		"summarize(dev.fast.requests.api.count, '1h')",
		// unaccelerated lanes
		"movingAverage(dev.fast.latency.api.p99, '5min')",
		"highestMax(dev.fast.latency.*.p99, 1)",
	}
	windows := []struct{ from, until string }{
		{"-30min", "-5min"}, {"-8h", "-5min"}, {"-3d", "-1d"}, {"-100d", "-5min"},
	}
	variants := []url.Values{
		nil,
		{"format": {"raw"}}, {"format": {"csv"}},
		{"maxDataPoints": {"7"}}, {"maxDataPoints": {"533"}}, {"maxDataPoints": {"1"}},
		{"noNullPoints": {"1"}}, {"pretty": {"1"}}, {"jsonp": {"cb"}},
	}
	seen := map[resolution.Confidence]int{}
	var checked, empty int
	for _, target := range targets {
		for _, w := range windows {
			for _, extra := range variants {
				q := h.query(url.Values{"target": {target}, "from": {w.from}, "until": {w.until}})
				for k, vals := range extra {
					q[k] = vals
				}
				label := target + " " + w.from + ".." + w.until + " " + extra.Encode()
				for pass := range 2 {
					got := h.render(q)
					if got.Code != http.StatusOK {
						t.Fatalf("%s (pass %d): status %d", label, pass, got.Code)
					}
					if want := h.direct(q); got.Body.String() != want {
						t.Errorf("%s (pass %d) differs from the origin\n got: %.240s\nwant: %.240s",
							label, pass, got.Body.String(), want)
					} else if pass == 0 {
						if !strings.Contains(want, "datapoints") && extra["format"] == nil {
							empty++
						}
						seen[h.lastRQ.Confidence]++
					}
					checked++
				}
			}
		}
	}
	t.Logf("%d responses verified byte-identical to the live origin (%d empty); confidence mix: "+
		"exact=%d derived=%d configured=%d unknown=%d", checked, empty,
		seen[resolution.Exact], seen[resolution.Derived], seen[resolution.Configured],
		seen[resolution.Unknown])
	if seen[resolution.Exact]+seen[resolution.Derived] == 0 {
		t.Error("no target was accelerated: the run proves nothing")
	}
}
