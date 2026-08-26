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
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

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
	windows := []struct{ from, until string }{
		{"-30min", "-5min"},
		{"-8h", "-5min"},
		{"-3d", "-1d"},
		{"-120d", "-5min"},
		{"-6h", "-5h"},
		// dev.fast 10s->60s at 6h, and 60s->10m at 7d
		{"-21599s", "-5min"}, {"-21600s", "-5min"}, {"-21601s", "-5min"},
		{"-604799s", "-5min"}, {"-604800s", "-5min"}, {"-604801s", "-5min"},
		// dev.medium 60s->5m at 2d, and cfg.a's configured 60s->5m at 2d
		{"-172799s", "-5min"}, {"-172800s", "-5min"}, {"-172801s", "-5min"},
		// dev.coarse maxRetention at 90d: the clamp edge, either side
		{"-7775999s", "-5min"}, {"-7776000s", "-5min"}, {"-7776001s", "-5min"},
	}
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
				maps.Copy(q, v.extra)
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
						if tgt, predicted, observed, ok := h.lastRQ.Mispredicted(); ok {
							t.Errorf("%s: mispredicted %s: predicted %v, origin answered %v",
								label, tgt, predicted, observed)
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
			b.WriteString(url.QueryEscape(k))
			b.WriteString("=")
			b.WriteString(url.QueryEscape(base.Get(k)))
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
			b.WriteString(url.QueryEscape(k))
			b.WriteString("=")
			b.WriteString(url.QueryEscape(form.Get(k)))
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
	windows := []struct {
		from, until string
		edge        bool
	}{
		{"-30min", "-5min", false}, {"-8h", "-5min", false}, {"-3d", "-1d", false}, {"-100d", "-2h", false},
		{"-21599s", "-5min", true}, {"-21600s", "-5min", true}, {"-21601s", "-5min", false}, // fast 10s->60s
		{"-604799s", "-2h", true}, {"-604801s", "-2h", false}, // fast 60s->10m
		{"-43199s", "-2h", true}, {"-43200s", "-2h", true}, {"-43201s", "-2h", false}, // drift 30s->5m
		{"-1209601s", "-2h", false},                           // drift 5m->1h
		{"-172799s", "-2h", true}, {"-172801s", "-2h", false}, // medium 60s->5m
		{"-7775999s", "-2h", true}, {"-7776001s", "-2h", false}, // coarse maxRetention
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
				maps.Copy(q, extra)
				label := target + " " + w.from + ".." + w.until + " " + extra.Encode()
				for pass := range 2 {
					got := h.render(q)
					if got.Code != http.StatusOK {
						t.Fatalf("%s (pass %d): status %d", label, pass, got.Code)
					}
					if want := h.direct(q); got.Body.String() != want && !w.edge {
						t.Errorf("%s (pass %d) differs from the origin\n got: %.240s\nwant: %.240s",
							label, pass, got.Body.String(), want)
					} else if pass == 0 {
						if !strings.Contains(want, "datapoints") && extra["format"] == nil {
							empty++
						}
						if tgt, predicted, observed, ok := h.lastRQ.Mispredicted(); ok {
							t.Errorf("%s: mispredicted %s: predicted %v, origin answered %v",
								label, tgt, predicted, observed)
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
