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
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// frozen label values: a value emitted outside these sets is a contract
// break, since dashboards filter on them
var (
	frozenConfidence = []string{"exact", "derived", "configured", "unknown"}
	frozenSource     = []string{"registry", "response", "probe", "static", "function", "none"}
	frozenKind       = []string{"narrow", "wide", "find"}
	frozenResult     = []string{"step", "empty", "error"}
	frozenLayer      = []string{"leaf", "ladder", "target", "negative"}
	frozenReason     = []string{"parse_error", "non_series_format", "function_not_allowlisted",
		"unknown_step", "missing_target", "multi_target_step_mismatch",
		"passthrough_max_data_points", "misprediction", "client_identity",
		"tz_unavailable", "resolution_identity"}
)

func labelValues(t *testing.T, c prometheus.Collector, label string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	ch := make(chan prometheus.Metric, 256)
	go func() {
		c.Collect(ch)
		close(ch)
	}()
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatal(err)
		}
		for _, lp := range pb.GetLabel() {
			if lp.GetName() != label {
				continue
			}
			v := pb.GetCounter().GetValue()
			if pb.GetGauge() != nil {
				v = pb.GetGauge().GetValue()
			}
			out[lp.GetValue()] += v
		}
	}
	return out
}

func TestObserverEmitsFrozenMetrics(t *testing.T) {
	// each resolution outcome must populate its metric family with only
	// frozen label values; no metric path or target expression may leak in
	h := newHarness(t)
	name := h.client.Name()

	before := func(c *prometheus.CounterVec, lvs ...string) float64 {
		return testutil.ToFloat64(c.WithLabelValues(append([]string{name}, lvs...)...))
	}
	fallbacksBefore := before(metrics.GraphiteFallbacks, parsing.ReasonFunctionNotAllowlisted)
	lookupsBefore := before(metrics.GraphiteResolutionLookups, "exact", resolution.SourceRegistry)

	// a decline: the fallback reason is counted
	h.same("declined", h.query(url.Values{
		"target": {"movingAverage(dev.fast.cpu.host01.percent, '5min')"}, "from": {"-1h"}}))
	if got := before(metrics.GraphiteFallbacks, parsing.ReasonFunctionNotAllowlisted); got <= fallbacksBefore {
		t.Errorf("fallbacks_total{reason=function_not_allowlisted} did not advance: %v", got)
	}

	// learning: probes are counted by kind and result, and the ladder and
	// registry gauges are published
	if _, err := h.client.learner.Learn(context.Background(), "dev.fast.cpu.host01.percent", nil); err != nil {
		t.Fatal(err)
	}
	probes := labelValues(t, metrics.GraphiteProbes, "kind")
	if probes[resolution.KindNarrow] == 0 {
		t.Errorf("probes_total has no narrow probes: %v", probes)
	}

	// an accelerated request: the lookup is counted with its confidence
	h.same("accelerated", h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-1h"}}))
	if got := before(metrics.GraphiteResolutionLookups, "exact", resolution.SourceRegistry); got <= lookupsBefore {
		t.Errorf("resolution_lookups_total{confidence=exact,source=registry} did not advance: %v", got)
	}
	if v := testutil.ToFloat64(metrics.GraphiteLadders.WithLabelValues(name)); v < 1 {
		t.Errorf("ladders gauge is %v, want at least 1", v)
	}
	entries := labelValues(t, metrics.GraphiteRegistryEntries, "layer")
	if entries[resolution.LayerLeaf] < 1 {
		t.Errorf("registry_entries{layer=leaf} is %v", entries[resolution.LayerLeaf])
	}

	// a misprediction is counted, and never silently
	mispredictBefore := testutil.ToFloat64(metrics.GraphiteStepMispredictions.WithLabelValues(name))
	h.client.observer.Misprediction()
	if got := testutil.ToFloat64(metrics.GraphiteStepMispredictions.WithLabelValues(name)); got != mispredictBefore+1 {
		t.Errorf("step_mispredictions_total did not advance: %v", got)
	}

	// only frozen label values may appear
	for _, tc := range []struct {
		family prometheus.Collector
		label  string
		frozen []string
	}{
		{metrics.GraphiteResolutionLookups, "confidence", frozenConfidence},
		{metrics.GraphiteResolutionLookups, "source", frozenSource},
		{metrics.GraphiteProbes, "kind", frozenKind},
		{metrics.GraphiteProbes, "result", frozenResult},
		{metrics.GraphiteRegistryEntries, "layer", frozenLayer},
		{metrics.GraphiteFallbacks, "reason", frozenReason},
	} {
		for got := range labelValues(t, tc.family, tc.label) {
			if !slices.Contains(tc.frozen, got) {
				t.Errorf("%s label emitted the unfrozen value %q (allowed: %v)", tc.label, got, tc.frozen)
			}
		}
	}
}

func TestGraphiteOutcomesReachProxyMetrics(t *testing.T) {
	// graphite requests must land in the shared trickster_proxy_requests_total
	// family so standard cache-status panels need no graphite-specific wiring
	h := newHarness(t)
	h.learn("dev.fast.cpu.host01.percent")
	q := h.query(url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {"-1h"}})
	h.same("miss", q)
	h.same("hit", q)

	ch := make(chan prometheus.Metric, 512)
	go func() {
		metrics.ProxyRequestStatus.Collect(ch)
		close(ch)
	}()
	statuses := map[string]bool{}
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatal(err)
		}
		labels := map[string]string{}
		for _, lp := range pb.GetLabel() {
			labels[lp.GetName()] = lp.GetValue()
		}
		if labels["provider"] == "graphite" && pb.GetCounter().GetValue() > 0 {
			statuses[labels["cache_status"]] = true
		}
	}
	if !statuses["kmiss"] || !statuses["hit"] {
		t.Errorf("expected graphite kmiss and hit in trickster_proxy_requests_total, got %v", statuses)
	}
	for s := range statuses {
		if strings.TrimSpace(s) == "" {
			t.Error("a graphite outcome was recorded without a cache status")
		}
	}
}
