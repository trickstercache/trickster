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

package dynamic

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	alberr "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	tsmoptions "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

const (
	globalTestALB      = "myalb"
	globalTestTemplate = "series-template"
	globalTestAddr     = "10.0.0.1:9009"
	globalTestMember   = "myalb-m1"
)

func newSeriesTemplate(t *testing.T, provider string, po *prop.Options) *bo.Options {
	t.Helper()
	tmpl := bo.New()
	tmpl.Provider = provider
	tmpl.IsTemplate = true
	tmpl.Prometheus = po
	// a probe with a long interval registers each admitted member's health without contacting it
	tmpl.HealthCheck = &ho.Options{
		Interval: timeconv.Duration(time.Hour), Timeout: timeconv.Duration(50 * time.Millisecond), FailureThreshold: 100,
	}
	require.NoError(t, tmpl.Initialize(globalTestTemplate))
	return tmpl
}

func newTSMTestManager(t *testing.T, evaluation string, tmpl *bo.Options,
) (*Manager, *alb.Client, healthcheck.HealthChecker, *clientBuilds) {
	t.Helper()
	opts := &ao.DiscoveryOptions{DiscovererName: "d", TemplateBackend: tmpl.Name}
	o := bo.New()
	o.Provider = providers.ALB
	o.ALBOptions = &ao.Options{MechanismName: names.MechanismTSM, Discovery: opts}
	o.ALBOptions.TSMOptions.Evaluation = evaluation
	require.NoError(t, o.ALBOptions.Initialize(globalTestALB))
	factories := providerregistry.SupportedProviders()
	cl, err := alb.NewClient(globalTestALB, o, nil, nil, nil, factories)
	require.NoError(t, err)
	c := cl.(*alb.Client)
	require.NoError(t, c.ValidateAndStartPool(backends.Backends{globalTestALB: cl}, nil))
	t.Cleanup(c.StopPool)
	require.NoError(t, opts.Initialize(""))
	hc := healthcheck.New()
	t.Cleanup(hc.Shutdown)
	m := New(Config{
		ALB:           c,
		Options:       opts,
		Template:      tmpl,
		Conf:          config.NewConfig(),
		Factories:     factories,
		Caches:        cache.Lookup{bo.DefaultBackendCacheName: nil},
		HealthChecker: hc,
	})
	t.Cleanup(m.Stop)
	return m, c, hc, recordClientBuilds(m)
}

func TestManagerGlobalEvaluationMembers(t *testing.T) {
	mimir := &prop.Options{Flavor: prop.FlavorMimir}
	for name, tc := range map[string]struct {
		evaluation, provider string
		po                   *prop.Options
		wantErr              error
	}{
		"mimir":          {tsmoptions.EvaluationGlobal, providers.Prometheus, mimir, nil},
		"mimir on merge": {tsmoptions.EvaluationMerge, providers.Prometheus, mimir, nil},
		"remote read with an endpoint": {tsmoptions.EvaluationGlobal, providers.Prometheus, &prop.Options{
			SeriesAPI: prop.SeriesAPIRemoteRead, SeriesEndpoint: "http://mimir:8080/prometheus/api/v1/read",
		}, nil},
		"thanos with an endpoint": {tsmoptions.EvaluationGlobal, providers.Prometheus, &prop.Options{
			Flavor: prop.FlavorThanos, SeriesEndpoint: prop.StoreAPIScheme + "://thanos:10901",
		}, nil},
		"thanos without an endpoint": {
			tsmoptions.EvaluationGlobal, providers.Prometheus,
			&prop.Options{Flavor: prop.FlavorThanos}, prop.ErrSeriesEndpoint,
		},
		"prometheus without a series api":  {tsmoptions.EvaluationGlobal, providers.Prometheus, nil, prop.ErrSeriesEndpoint},
		"prometheus without one, on merge": {tsmoptions.EvaluationMerge, providers.Prometheus, nil, nil},
		"greptimedb":                       {tsmoptions.EvaluationGlobal, providers.GreptimeDB, nil, alberr.ErrGlobalEvaluationMember},
		"greptimedb on merge":              {tsmoptions.EvaluationMerge, providers.GreptimeDB, nil, nil},
		"influxdb":                         {tsmoptions.EvaluationGlobal, providers.InfluxDB, nil, alberr.ErrGlobalEvaluationMember},
	} {
		t.Run(name, func(t *testing.T) {
			m, c, hc, builds := newTSMTestManager(t, tc.evaluation, newSeriesTemplate(t, tc.provider, tc.po))
			m.ApplySnapshot(discovery.Snapshot{member("m1", globalTestAddr)})
			if tc.wantErr == nil {
				require.Len(t, builds.of(globalTestMember), 1)
				require.Equal(t, []string{globalTestMember}, m.MemberNames())
				require.Equal(t, []string{globalTestMember}, c.DynamicPoolNames())
				require.Contains(t, hc.Statuses(), globalTestMember)
				require.NotNil(t, m.AppliedSnapshot())
				return
			}
			// a member that can't serve raw series is never admitted, registered or marked applied
			require.Empty(t, m.MemberNames())
			require.Empty(t, c.DynamicPoolNames())
			require.NotContains(t, hc.Statuses(), globalTestMember)
			require.Nil(t, m.AppliedSnapshot())
			_, err := m.instantiateMember(globalTestMember, member("m1", globalTestAddr))
			require.ErrorIs(t, err, alberr.ErrGlobalEvaluationMember)
			require.ErrorIs(t, err, tc.wantErr)
			require.ErrorContains(t, err, `alb "myalb" template "series-template" member "myalb-m1": `)
			// nor is a client built for it, as the check comes first
			require.Empty(t, builds.of(globalTestMember))
		})
	}
}

func TestManagerGlobalEvaluationMalformedOrigin(t *testing.T) {
	// a remote-read endpoint derives from each member's discovered origin, which must be http or https
	m, c, hc, builds := newTSMTestManager(t, tsmoptions.EvaluationGlobal,
		newSeriesTemplate(t, providers.Prometheus, &prop.Options{Flavor: prop.FlavorMimir}))
	bad := discovery.Member{Name: "bad", Scheme: "tcp", Address: "10.0.0.2:9009"}
	m.ApplySnapshot(discovery.Snapshot{member("good", globalTestAddr), bad})
	require.Equal(t, []string{"myalb-good"}, m.MemberNames())
	require.Equal(t, []string{"myalb-good"}, c.DynamicPoolNames())
	require.NotContains(t, hc.Statuses(), "myalb-bad")
	require.Nil(t, m.AppliedSnapshot(), "a snapshot with a rejected member is retried")

	_, err := m.instantiateMember("myalb-bad", bad)
	require.ErrorIs(t, err, alberr.ErrGlobalEvaluationMember)
	require.ErrorIs(t, err, prop.ErrSeriesEndpoint)
	require.ErrorContains(t, err, `alb "myalb" template "series-template" member "myalb-bad": `)
	require.Empty(t, builds.of("myalb-bad"))
	require.Len(t, builds.of("myalb-good"), 1)

	// a merging tsm reads no raw series, so the same member is admitted
	m, _, _, builds = newTSMTestManager(t, tsmoptions.EvaluationMerge,
		newSeriesTemplate(t, providers.Prometheus, &prop.Options{Flavor: prop.FlavorMimir}))
	m.ApplySnapshot(discovery.Snapshot{bad})
	require.Equal(t, []string{"myalb-bad"}, m.MemberNames())
	require.Len(t, builds.of("myalb-bad"), 1)
}

func TestManagerResidualGlobalBlockAdmitsMembers(t *testing.T) {
	// only a tsm mechanism acts on its tsm block, so another mechanism's members need no series source
	m, c, _ := newTestManager(t, &ao.DiscoveryOptions{DiscovererName: "d", TemplateBackend: "rp-template"})
	c.Configuration().ALBOptions.TSMOptions.Evaluation = tsmoptions.EvaluationGlobal
	residual := New(m.cfg)
	t.Cleanup(residual.Stop)
	residual.ApplySnapshot(discovery.Snapshot{member("m1", globalTestAddr)})
	require.Equal(t, []string{globalTestMember}, residual.MemberNames())
}

type closeCounter struct {
	http.RoundTripper
	closes atomic.Int32
}

func (c *closeCounter) CloseIdleConnections() { c.closes.Add(1) }

type builtClient struct {
	name        string
	web, health closeCounter
}

func (bc *builtClient) closed() bool { return bc.web.closes.Load() > 0 && bc.health.closes.Load() > 0 }

type clientBuilds struct {
	mtx    sync.Mutex
	builds []*builtClient
}

func (b *clientBuilds) of(name string) []*builtClient {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	var out []*builtClient
	for _, bc := range b.builds {
		if bc.name == name {
			out = append(out, bc)
		}
	}
	return out
}

func recordClientBuilds(m *Manager) *clientBuilds {
	b := &clientBuilds{}
	factories := make(rt.Lookup, len(m.cfg.Factories))
	for provider, factory := range m.cfg.Factories {
		if factory == nil {
			continue
		}
		factories[provider] = func(name string, o *bo.Options, r http.Handler, c cache.Cache,
			members backends.Backends, f rt.Lookup,
		) (backends.Backend, error) {
			client, err := factory(name, o, r, c, members, f)
			if err != nil {
				return client, err
			}
			bc := &builtClient{name: name}
			bc.web.RoundTripper, bc.health.RoundTripper = http.DefaultTransport, http.DefaultTransport
			if hc := client.HTTPClient(); hc != nil {
				hc.Transport = &bc.web
			}
			if hc := client.HealthCheckHTTPClient(); hc != nil {
				hc.Transport = &bc.health
			}
			b.mtx.Lock()
			b.builds = append(b.builds, bc)
			b.mtx.Unlock()
			return client, nil
		}
	}
	m.cfg.Factories = factories
	return b
}

type failingRegistry struct {
	healthcheck.HealthChecker
}

func (failingRegistry) Register(string, string, *ho.Options, *http.Client) (*healthcheck.Status, error) {
	return nil, errors.New("registration refused")
}

func TestManagerReleasesMemberClients(t *testing.T) {
	setup := func(t *testing.T) (*Manager, *clientBuilds) {
		m, _, _ := newTestManager(t, &ao.DiscoveryOptions{DiscovererName: "d", TemplateBackend: "rp-template"})
		return m, recordClientBuilds(m)
	}
	// a client built for a member that is then refused is released at once, on both of its transports
	refused := func(t *testing.T, m *Manager, builds *clientBuilds, wantErr string) {
		t.Helper()
		_, err := m.instantiateMember(globalTestMember, member("m1", globalTestAddr))
		require.ErrorContains(t, err, wantErr)
		built := builds.of(globalTestMember)
		require.Len(t, built, 1)
		require.True(t, built[0].closed())
	}
	t.Run("admitted", func(t *testing.T) {
		m, builds := setup(t)
		probedTemplate(m)
		_, err := m.instantiateMember(globalTestMember, member("m1", globalTestAddr))
		require.NoError(t, err)
		built := builds.of(globalTestMember)
		require.Len(t, built, 1)
		require.Zero(t, built[0].web.closes.Load())
		require.Zero(t, built[0].health.closes.Load())
	})
	t.Run("unsupported step alignment", func(t *testing.T) {
		m, builds := setup(t)
		m.cfg.Template.StepAlignment = timeseries.StepAlignmentTruncate
		refused(t, m, builds, "step_alignment")
	})
	t.Run("health check registration", func(t *testing.T) {
		m, builds := setup(t)
		probedTemplate(m)
		m.cfg.HealthChecker = failingRegistry{HealthChecker: m.cfg.HealthChecker}
		refused(t, m, builds, "registration refused")
	})
	t.Run("removed and rebuilt", func(t *testing.T) {
		m, builds := setup(t)
		probedTemplate(m)
		m.ApplySnapshot(discovery.Snapshot{member("m1", globalTestAddr), member("m2", "10.0.0.2:9009")})
		require.Equal(t, []string{globalTestMember, "myalb-m2"}, m.MemberNames())
		// m1 moves, so its client is rebuilt, and m2 is gone
		m.ApplySnapshot(discovery.Snapshot{member("m1", "10.0.0.9:9009")})
		require.Equal(t, []string{globalTestMember}, m.MemberNames())
		m1, m2 := builds.of(globalTestMember), builds.of("myalb-m2")
		require.Len(t, m1, 2)
		require.Len(t, m2, 1)
		require.Eventually(t, func() bool { return m1[0].closed() && m2[0].closed() },
			5*time.Second, 5*time.Millisecond)
		require.Zero(t, m1[1].web.closes.Load())
		require.Zero(t, m1[1].health.closes.Load())
	})
}
