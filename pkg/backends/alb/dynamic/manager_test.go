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
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

func newTestManager(t testing.TB, opts *ao.DiscoveryOptions) (*Manager, *alb.Client, healthcheck.HealthChecker) {
	t.Helper()
	o := bo.New()
	o.Provider = providers.ALB
	o.ALBOptions = &ao.Options{MechanismName: "rr", Discovery: opts}
	cl, err := alb.NewClient("myalb", o, nil, nil, nil, nil)
	require.NoError(t, err)
	c := cl.(*alb.Client)
	require.NoError(t,
		c.ValidateAndStartPool(backends.Backends{"myalb": cl}, nil))
	t.Cleanup(c.StopPool)

	tmpl := bo.New()
	tmpl.Provider = providers.ReverseProxyShort
	tmpl.IsTemplate = true
	require.NoError(t, tmpl.Initialize("rp-template"))

	require.NoError(t, opts.Initialize(""))
	hc := healthcheck.New()
	t.Cleanup(hc.Shutdown)
	m := New(Config{
		ALB:           c,
		Options:       opts,
		Template:      tmpl,
		Conf:          config.NewConfig(),
		Factories:     providerregistry.SupportedProviders(),
		HealthChecker: hc,
	})
	t.Cleanup(m.Stop)
	return m, c, hc
}

func member(name, addr string) discovery.Member {
	return discovery.Member{Name: name, Scheme: "http", Address: addr}
}

func TestManagerAddAndRemoveMembers(t *testing.T) {
	m, c, hc := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template",
	})

	m.ApplySnapshot(discovery.Snapshot{
		member("m1", "10.0.0.1:8080"),
		member("m2", "10.0.0.2:8080"),
	})
	require.Equal(t, []string{"myalb-m1", "myalb-m2"}, m.MemberNames())
	require.Equal(t, []string{"myalb-m1", "myalb-m2"}, c.DynamicPoolNames())
	require.Contains(t, hc.Statuses(), "myalb-m1")
	require.Contains(t, hc.Statuses(), "myalb-m2")

	// removal tears down the member and its health registration
	m.ApplySnapshot(discovery.Snapshot{member("m1", "10.0.0.1:8080")})
	require.Equal(t, []string{"myalb-m1"}, m.MemberNames())
	require.Equal(t, []string{"myalb-m1"}, c.DynamicPoolNames())
	require.NotContains(t, hc.Statuses(), "myalb-m2")

	// same name, new address: the member is rebuilt with the new origin
	m.ApplySnapshot(discovery.Snapshot{member("m1", "10.0.0.9:8080")})
	require.Equal(t, []string{"myalb-m1"}, m.MemberNames())
	snap := m.AppliedSnapshot()
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.9:8080", snap[0].Address)

	// Stop releases everything
	m.Stop()
	require.Empty(t, m.MemberNames())
	require.NotContains(t, hc.Statuses(), "myalb-m1")
}

func TestManagerMinMembersGuardrail(t *testing.T) {
	m, _, _ := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template", MinMembers: 2,
	})

	full := discovery.Snapshot{
		member("m1", "10.0.0.1:8080"),
		member("m2", "10.0.0.2:8080"),
	}
	m.ApplySnapshot(full)
	require.Len(t, m.MemberNames(), 2)

	// a suspicious shrink below the floor keeps the last-good membership
	m.ApplySnapshot(discovery.Snapshot{member("m1", "10.0.0.1:8080")})
	require.Len(t, m.MemberNames(), 2)
	m.ApplySnapshot(discovery.Snapshot{})
	require.Len(t, m.MemberNames(), 2)

	// a compliant snapshot still applies
	m.ApplySnapshot(discovery.Snapshot{
		member("m2", "10.0.0.2:8080"),
		member("m3", "10.0.0.3:8080"),
	})
	require.Equal(t, []string{"myalb-m2", "myalb-m3"}, m.MemberNames())
}

func TestManagerDebounce(t *testing.T) {
	m, _, _ := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template",
		DebounceWindow: timeconv.Duration(50 * time.Millisecond),
	})

	// the first snapshot applies immediately (leading edge)
	m.ApplySnapshot(discovery.Snapshot{member("m1", "10.0.0.1:8080")})
	require.Equal(t, []string{"myalb-m1"}, m.MemberNames())

	// rapid follow-ups are damped; only the newest applies, on the
	// trailing edge
	m.ApplySnapshot(discovery.Snapshot{member("m2", "10.0.0.2:8080")})
	m.ApplySnapshot(discovery.Snapshot{member("m3", "10.0.0.3:8080")})
	require.Equal(t, []string{"myalb-m1"}, m.MemberNames())
	require.Eventually(t, func() bool {
		names := m.MemberNames()
		return len(names) == 1 && names[0] == "myalb-m3"
	}, time.Second, 10*time.Millisecond)
}

func TestManagerProviderHealthMode(t *testing.T) {
	m, _, hc := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template",
		HealthMode: ao.HealthModeProvider,
	})

	mem := member("m1", "10.0.0.1:8080")
	mem.Ready = discovery.Ready
	m.ApplySnapshot(discovery.Snapshot{mem})
	st := hc.Statuses()["myalb-m1"]
	require.NotNil(t, st)
	require.Equal(t, healthcheck.StatusPassing, st.Get())

	// a readiness flip updates the same status in place
	mem.Ready = discovery.Terminating
	m.ApplySnapshot(discovery.Snapshot{mem})
	require.Equal(t, healthcheck.StatusFailing, st.Get())

	mem.Ready = discovery.ReadyUnknown
	m.ApplySnapshot(discovery.Snapshot{mem})
	require.Equal(t, healthcheck.StatusUnchecked, st.Get())
}

func TestManagerInstantiationFailureRetries(t *testing.T) {
	m, c, _ := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template",
	})
	m.cfg.Template.IsTemplate = false // sabotage instantiation

	s := discovery.Snapshot{member("m1", "10.0.0.1:8080")}
	m.ApplySnapshot(s)
	require.Empty(t, m.MemberNames())
	require.Empty(t, c.DynamicPoolNames())
	require.Nil(t, m.AppliedSnapshot(), "failed applies must not be marked applied")

	// once the failure clears, the identical snapshot is retried
	m.cfg.Template.IsTemplate = true
	m.ApplySnapshot(s)
	require.Equal(t, []string{"myalb-m1"}, m.MemberNames())
}

func TestManagerReplicaGroupChangeRebuildsMember(t *testing.T) {
	m, _, _ := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "prom-template",
	})
	// per-member replica groups require a TSM-capable template
	m.cfg.Template = newPromTemplate(t)
	m.cfg.Caches = cache.Lookup{"default": nil}

	mem := member("m1", "10.0.0.1:9090")
	mem.ReplicaGroup = "shard-0"
	m.ApplySnapshot(discovery.Snapshot{mem})
	require.Equal(t, []string{"myalb-m1"}, m.MemberNames())
	require.Equal(t, "shard-0", m.memberReplicaGroup("myalb-m1"))

	// a regroup under the same name and origin rebuilds the member with
	// the new group
	mem.ReplicaGroup = "shard-1"
	m.ApplySnapshot(discovery.Snapshot{mem})
	require.Equal(t, []string{"myalb-m1"}, m.MemberNames())
	require.Equal(t, "shard-1", m.memberReplicaGroup("myalb-m1"))
}

// newPromTemplate returns a TSM-capable template for replica-group tests
func newPromTemplate(t *testing.T) *bo.Options {
	t.Helper()
	tmpl := bo.New()
	tmpl.Provider = providers.Prometheus
	tmpl.IsTemplate = true
	require.NoError(t, tmpl.Initialize("prom-template"))
	return tmpl
}

// memberReplicaGroup returns the effective replica group of the named live
// member's instantiated backend; empty when the member does not exist
func (m *Manager) memberReplicaGroup(name string) string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	e, ok := m.members[name]
	if !ok || e.client == nil || e.client.Configuration() == nil {
		return ""
	}
	return e.client.Configuration().ReplicaGroup
}

// probedTemplate gives the manager's template an active probe with a long
// interval and a lenient failure threshold, so members enter Initializing
// and the immediate first probe against an unroutable address cannot mark
// them down within the test
func probedTemplate(m *Manager) {
	m.cfg.Template.HealthCheck = &ho.Options{
		Interval:         timeconv.Duration(time.Hour),
		Timeout:          timeconv.Duration(50 * time.Millisecond),
		FailureThreshold: 100,
	}
}

// In probe mode, a member the provider reports ready is admitted ahead of
// its first probe result: the orchestrator retired its predecessor on that
// same signal, so waiting for a probe leaves the pool empty meanwhile
func TestManagerProbeModeAdmitsProviderReadyMembers(t *testing.T) {
	m, c, hc := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template",
	})
	probedTemplate(m)
	// the probe itself cannot admit anyone: it expects a code the upstream
	// never returns, so only provider readiness can open the pool
	m.cfg.Template.HealthCheck.ExpectedCodes = []int{http.StatusTeapot}

	upstream := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ready")) //nolint:errcheck
		}))
	defer upstream.Close()

	ready := member("ready", upstream.Listener.Addr().String())
	ready.Ready = discovery.Ready
	notReady := member("notready", "10.0.0.2:8080")
	notReady.Ready = discovery.NotReady
	unknown := member("unknown", "10.0.0.3:8080")

	m.ApplySnapshot(discovery.Snapshot{ready, notReady, unknown})
	statuses := hc.Statuses()
	require.Equal(t, healthcheck.StatusPassing, statuses["myalb-ready"].Get(),
		"provider-ready member is admitted pending its first probe")
	require.Equal(t, healthcheck.StatusInitializing, statuses["myalb-notready"].Get(),
		"not-ready member waits for its probe")
	require.Equal(t, healthcheck.StatusInitializing, statuses["myalb-unknown"].Get(),
		"readiness-unknown member waits for its probe")

	// the pool dispatches to the admitted member alone: every request lands
	// on the ready upstream rather than a 502 from an empty pool or a dial
	// against an unroutable Initializing member
	albHandler := c.Handlers()[providers.ALB]
	for range 6 {
		rec := httptest.NewRecorder()
		albHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "ready", rec.Body.String())
	}
}

// A readiness flip on a live probe-mode member admits it while its probe
// is still pending, but never overrides a verdict the probe has reached
func TestManagerProbeModeReadinessFlipAdmitsPendingMembersOnly(t *testing.T) {
	m, _, hc := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template",
	})
	probedTemplate(m)

	pending := member("pending", "10.0.0.1:8080")
	pending.Ready = discovery.NotReady
	failed := member("failed", "10.0.0.2:8080")
	failed.Ready = discovery.NotReady
	m.ApplySnapshot(discovery.Snapshot{pending, failed})
	statuses := hc.Statuses()
	require.Equal(t, healthcheck.StatusInitializing, statuses["myalb-pending"].Get())
	// the probe has already judged this one
	statuses["myalb-failed"].Set(healthcheck.StatusFailing)

	pending.Ready = discovery.Ready
	failed.Ready = discovery.Ready
	m.ApplySnapshot(discovery.Snapshot{pending, failed})
	require.Equal(t, healthcheck.StatusPassing, statuses["myalb-pending"].Get(),
		"readiness admits a member whose probe has not yet reported")
	require.Equal(t, healthcheck.StatusFailing, statuses["myalb-failed"].Get(),
		"readiness must not override a probe verdict")

	// a flip back to not-ready is the probe's business in probe mode
	pending.Ready = discovery.NotReady
	m.ApplySnapshot(discovery.Snapshot{pending, failed})
	require.Equal(t, healthcheck.StatusPassing, statuses["myalb-pending"].Get())
}

// A template with no probe registers no status at all; readiness must not
// invent one
func TestManagerProbeModeReadinessWithoutProbe(t *testing.T) {
	m, _, hc := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template",
	})
	m.cfg.Template.HealthCheck = nil

	mem := member("m1", "10.0.0.1:8080")
	mem.Ready = discovery.NotReady
	m.ApplySnapshot(discovery.Snapshot{mem})
	mem.Ready = discovery.Ready
	require.NotPanics(t, func() { m.ApplySnapshot(discovery.Snapshot{mem}) })
	require.NotContains(t, hc.Statuses(), "myalb-m1")
}

// a discovered member is health checked the way a configured one is: a udp origin, which
// nothing can probe, follows the provider's readiness instead of an http probe that can only
// fail, and a tcp origin is probed by connecting to it
func TestManagerProbeModeByOriginProtocol(t *testing.T) {
	m, c, hc := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template",
	})
	probedTemplate(m)
	m.cfg.Template.HealthCheck.FailureThreshold = 1

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	datagrams := discovery.Member{Name: "dns", Scheme: "udp", Address: "10.0.0.9:53", Ready: discovery.Ready}
	pending := discovery.Member{Name: "pending", Scheme: "udp", Address: "10.0.0.10:53", Ready: discovery.NotReady}
	stream := discovery.Member{Name: "db", Scheme: "tcp", Address: ln.Addr().String()}
	m.ApplySnapshot(discovery.Snapshot{datagrams, pending, stream})

	statuses := hc.Statuses()
	require.Equal(t, healthcheck.StatusPassing, statuses["myalb-dns"].Get(),
		"a udp member the provider reports ready is available")
	require.Equal(t, healthcheck.StatusFailing, statuses["myalb-pending"].Get())
	require.Eventually(t, func() bool { return statuses["myalb-db"].Get() == healthcheck.StatusPassing },
		5*time.Second, 10*time.Millisecond, "a tcp member is probed by connecting to it")
	// no probe ever runs against the udp member, so it never goes down on one
	time.Sleep(150 * time.Millisecond)
	require.Equal(t, healthcheck.StatusPassing, statuses["myalb-dns"].Get())

	names := []string{}
	for _, tgt := range c.Pool().Targets() {
		names = append(names, tgt.Name())
	}
	require.ElementsMatch(t, []string{"myalb-dns", "myalb-db"}, names)

	// and it goes on following the provider
	pending.Ready = discovery.Ready
	datagrams.Ready = discovery.NotReady
	m.ApplySnapshot(discovery.Snapshot{datagrams, pending, stream})
	require.Equal(t, healthcheck.StatusPassing, statuses["myalb-pending"].Get())
	require.Equal(t, healthcheck.StatusFailing, statuses["myalb-dns"].Get())
}

// a member that keeps its name while its origin changes from one that is probed to one that
// cannot be leaves no probe behind: the retired origin is not contacted again
func TestManagerProbeRetiredWhenAMemberBecomesUnprobeable(t *testing.T) {
	m, _, hc := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template",
	})
	probedTemplate(m)
	m.cfg.Template.HealthCheck.Interval = timeconv.Duration(5 * time.Millisecond)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	var accepted atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()

	m.ApplySnapshot(discovery.Snapshot{{Name: "db", Scheme: "tcp", Address: ln.Addr().String()}})
	require.Eventually(t, func() bool { return accepted.Load() > 2 }, 5*time.Second, 5*time.Millisecond,
		"the tcp member was never probed")
	probed := hc.Statuses()["myalb-db"]

	m.ApplySnapshot(discovery.Snapshot{{Name: "db", Scheme: "udp", Address: "10.0.0.9:53", Ready: discovery.Ready}})
	st := hc.Statuses()["myalb-db"]
	require.NotSame(t, probed, st, "the member kept the status of the origin it left")
	require.Equal(t, healthcheck.StatusPassing, st.Get())
	// a probe in flight when the origin changed may still land; none starts after it
	time.Sleep(50 * time.Millisecond)
	settled := accepted.Load()
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, settled, accepted.Load(), "the retired origin is still being probed")
	require.Equal(t, healthcheck.StatusPassing, hc.Statuses()["myalb-db"].Get())
}
