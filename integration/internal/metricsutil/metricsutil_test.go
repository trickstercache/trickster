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

package metricsutil

import (
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"
)

func TestKey(t *testing.T) {
	require.Equal(t, "metric", Key("metric", nil))
	require.Equal(t, "metric", Key("metric", map[string]string{}))
	require.Equal(t,
		`m{a="1",b="2"}`,
		Key("m", map[string]string{"b": "2", "a": "1"}),
		"labels must be sorted alphabetically",
	)
}

func TestScrapeAndDelta(t *testing.T) {
	reg := prometheus.NewRegistry()
	ctr := prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "demo_total", Help: "demo"},
		[]string{"mech", "reason"},
	)
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "demo_gauge", Help: "demo"})
	reg.MustRegister(ctr, gauge)

	gauge.Set(7)
	ctr.WithLabelValues("fr", "panic").Add(2)
	ctr.WithLabelValues("tsm", "panic").Add(3)

	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	t.Cleanup(srv.Close)

	// Reuse the test server's own client so we don't fight macOS ephemeral
	// ports across rapid-fire scrapes against ephemeral httptest servers.
	cli := srv.Client()
	before := ScrapeURL(t, srv.URL, cli)
	require.Equal(t, 7.0, before["demo_gauge"], "gauge value")
	require.Equal(t, 2.0, before[`demo_total{mech="fr",reason="panic"}`])
	require.Equal(t, 3.0, before[`demo_total{mech="tsm",reason="panic"}`])

	ctr.WithLabelValues("fr", "panic").Add(5)
	after := ScrapeURL(t, srv.URL, cli)

	RequireDelta(t, before, after, "demo_total",
		map[string]string{"mech": "fr", "reason": "panic"}, 5)
	RequireDelta(t, before, after, "demo_total",
		map[string]string{"mech": "tsm", "reason": "panic"}, 0)

	// Asserting on a metric+labels that doesn't exist in either snapshot
	// must treat the delta as zero (not panic on a missing key).
	RequireDelta(t, before, after, "demo_total",
		map[string]string{"mech": "nlm", "reason": "panic"}, 0)
}

func TestScrapeHistogram(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "demo_hist",
			Help:    "demo",
			Buckets: []float64{1, 2, 5},
		},
		[]string{"mech"},
	)
	reg.MustRegister(h)
	h.WithLabelValues("tsm").Observe(0.5)
	h.WithLabelValues("tsm").Observe(3)

	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	t.Cleanup(srv.Close)

	snap := ScrapeURL(t, srv.URL, srv.Client())
	require.Equal(t, 2.0, snap[`demo_hist_count{mech="tsm"}`])
	require.Equal(t, 3.5, snap[`demo_hist_sum{mech="tsm"}`])
	require.Equal(t, 1.0, snap[`demo_hist_bucket{le="1",mech="tsm"}`])
	require.Equal(t, 1.0, snap[`demo_hist_bucket{le="2",mech="tsm"}`])
	require.Equal(t, 2.0, snap[`demo_hist_bucket{le="5",mech="tsm"}`])
}

func TestRequireDeltaGreater(t *testing.T) {
	reg := prometheus.NewRegistry()
	ctr := prometheus.NewCounter(prometheus.CounterOpts{Name: "demo_counter"})
	reg.MustRegister(ctr)
	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	t.Cleanup(srv.Close)

	before := ScrapeURL(t, srv.URL, srv.Client())
	for range 4 {
		ctr.Inc()
	}
	after := ScrapeURL(t, srv.URL, srv.Client())
	RequireDeltaGreater(t, before, after, "demo_counter", nil, 2)
}
