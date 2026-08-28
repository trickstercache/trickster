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

package dynamic

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

func snapshotCount(alb, disco, result string) float64 {
	return testutil.ToFloat64(
		metrics.ALBDiscoverySnapshots.WithLabelValues(alb, disco, result))
}

func changeCount(alb, disco, event string) float64 {
	return testutil.ToFloat64(
		metrics.ALBDiscoveryMemberChanges.WithLabelValues(alb, disco, event))
}

func TestManagerMetrics(t *testing.T) {
	m, _, _ := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template"})

	applied0 := snapshotCount("myalb", "d", resultApplied)
	unchanged0 := snapshotCount("myalb", "d", resultUnchanged)
	adds0 := changeCount("myalb", "d", "add")
	removes0 := changeCount("myalb", "d", "remove")

	m.ApplySnapshot(discovery.Snapshot{
		member("m1", "10.0.0.1:8080"),
		member("m2", "10.0.0.2:8080"),
	})
	require.Equal(t, float64(2), testutil.ToFloat64(
		metrics.ALBDiscoveryMembers.WithLabelValues("myalb", "d")))
	require.Equal(t, applied0+1, snapshotCount("myalb", "d", resultApplied))
	require.Equal(t, adds0+2, changeCount("myalb", "d", "add"))
	require.Positive(t, testutil.ToFloat64(
		metrics.ALBDiscoveryLastRefresh.WithLabelValues("myalb", "d")),
		"last-refresh timestamp set")

	// an identical snapshot is an unchanged (but successful) refresh
	m.ApplySnapshot(discovery.Snapshot{
		member("m1", "10.0.0.1:8080"),
		member("m2", "10.0.0.2:8080"),
	})
	require.Equal(t, unchanged0+1, snapshotCount("myalb", "d", resultUnchanged))

	// removal
	m.ApplySnapshot(discovery.Snapshot{member("m1", "10.0.0.1:8080")})
	require.Equal(t, removes0+1, changeCount("myalb", "d", "remove"))
	require.Equal(t, float64(1), testutil.ToFloat64(
		metrics.ALBDiscoveryMembers.WithLabelValues("myalb", "d")))

	// Stop clears this ALB's discovery series
	m.Stop()
	require.Zero(t, testutil.ToFloat64(
		metrics.ALBDiscoveryMembers.WithLabelValues("myalb", "d")))
}

func TestManagerMetricsRejectedAndPartial(t *testing.T) {
	m, _, _ := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template", MinMembers: 1})
	rejected0 := snapshotCount("myalb", "d", resultRejected)
	partial0 := snapshotCount("myalb", "d", resultPartial)
	refresh0 := testutil.ToFloat64(
		metrics.ALBDiscoveryLastRefresh.WithLabelValues("myalb", "d"))

	// guardrail rejection counts, and does NOT advance last-refresh
	m.ApplySnapshot(discovery.Snapshot{})
	require.Equal(t, rejected0+1, snapshotCount("myalb", "d", resultRejected))
	require.Equal(t, refresh0, testutil.ToFloat64(
		metrics.ALBDiscoveryLastRefresh.WithLabelValues("myalb", "d")),
		"a rejected snapshot is not a successful refresh")

	// an instantiation failure yields a partial result
	m.cfg.Template.IsTemplate = false
	m.ApplySnapshot(discovery.Snapshot{member("m1", "10.0.0.1:8080")})
	require.Equal(t, partial0+1, snapshotCount("myalb", "d", resultPartial))
}

func TestManagerHealthDescriptionTag(t *testing.T) {
	m, _, hc := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template"})
	m.ApplySnapshot(discovery.Snapshot{member("m1", "10.0.0.1:8080")})
	st := hc.Statuses()["myalb-m1"]
	require.NotNil(t, st)
	require.Equal(t, "rp (myalb via d)", st.Description(),
		"discovered members are tagged with their ALB and discoverer")
}

func TestManagerReconcileSpan(t *testing.T) {
	tr, sr := tu.NewRecordingTracer(t)

	o := bo.New()
	o.Provider = providers.ALB
	o.TracingConfigName = "test"
	o.ALBOptions = &ao.Options{MechanismName: "rr",
		Discovery: &ao.DiscoveryOptions{
			DiscovererName: "d", TemplateBackend: "rp-template"}}
	cl, err := alb.NewClient("span-alb", o, nil, nil, nil, nil)
	require.NoError(t, err)
	c := cl.(*alb.Client)
	require.NoError(t,
		c.ValidateAndStartPool(backends.Backends{"span-alb": cl}, nil))
	t.Cleanup(c.StopPool)

	tmpl := bo.New()
	tmpl.Provider = providers.ReverseProxyShort
	tmpl.IsTemplate = true
	require.NoError(t, tmpl.Initialize("rp-template"))

	opts := o.ALBOptions.Discovery
	require.NoError(t, opts.Initialize(""))
	hc := healthcheck.New()
	t.Cleanup(hc.Shutdown)
	m := New(Config{
		ALB:           c,
		Options:       opts,
		Template:      tmpl,
		Conf:          config.NewConfig(),
		Factories:     providerregistry.SupportedProviders(),
		Tracers:       tracing.Tracers{"test": tr},
		HealthChecker: hc,
	})
	t.Cleanup(m.Stop)

	m.ApplySnapshot(discovery.Snapshot{member("m1", "10.0.0.1:8080")})

	var found bool
	for _, span := range sr.Ended() {
		if span.Name() != "alb.discovery.reconcile" {
			continue
		}
		found = true
		attrs := make(map[attribute.Key]attribute.Value, len(span.Attributes()))
		for _, kv := range span.Attributes() {
			attrs[kv.Key] = kv.Value
		}
		require.Equal(t, "span-alb", attrs["alb.name"].AsString())
		require.Equal(t, "d", attrs["discovery.discoverer"].AsString())
		require.Equal(t, resultApplied, attrs["discovery.result"].AsString())
		require.Equal(t, int64(1), attrs["discovery.members.added"].AsInt64())
	}
	require.True(t, found, "expected an alb.discovery.reconcile span")
}
