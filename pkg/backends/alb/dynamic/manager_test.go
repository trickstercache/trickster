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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
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
		DiscovererName: "d", TemplateBackend: "rp-template"})

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
		DiscovererName: "d", TemplateBackend: "rp-template", MinMembers: 2})

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
		DebounceWindow: timeconv.Duration(50 * time.Millisecond)})

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
		HealthMode: ao.HealthModeProvider})

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
		DiscovererName: "d", TemplateBackend: "rp-template"})
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
		DiscovererName: "d", TemplateBackend: "prom-template"})
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
