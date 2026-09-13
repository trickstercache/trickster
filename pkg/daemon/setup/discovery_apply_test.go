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

package setup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/dynamic"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	kubeopts "github.com/trickstercache/trickster/v2/pkg/discovery/kubernetes/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	dp "github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

// newDiscoveryFixture builds a config, its ALB client map, and a
// ServerInstance ready for applyDiscoveryConfig
func newDiscoveryFixture(t *testing.T, discoverer *do.Options,
	query *do.Query, policy string,
) (*instance.ServerInstance, *config.Config, backends.Backends) {
	t.Helper()
	c := config.NewConfig()
	delete(c.Backends, "default")

	tmpl := bo.New()
	tmpl.Provider = providers.ReverseProxyShort
	tmpl.IsTemplate = true
	require.NoError(t, tmpl.Initialize("tmpl"))
	c.Backends["tmpl"] = tmpl

	albOpts := bo.New()
	albOpts.Provider = providers.ALB
	albOpts.ALBOptions = &ao.Options{
		MechanismName: "rr",
		Discovery: &ao.DiscoveryOptions{
			DiscovererName:  "d1",
			TemplateBackend: "tmpl",
			Query:           query,
			StartupPolicy:   policy,
		},
	}
	require.NoError(t, albOpts.Initialize("alb1"))
	c.Backends["alb1"] = albOpts
	require.NoError(t, discoverer.Initialize("d1"))
	c.Discovery = do.Lookup{"d1": discoverer}

	cl, err := alb.NewClient("alb1", albOpts, nil, nil, nil, nil)
	require.NoError(t, err)
	albClient := cl.(*alb.Client)
	require.NoError(t, albClient.ValidateAndStartPool(
		backends.Backends{"alb1": cl}, nil))
	t.Cleanup(albClient.StopPool)

	si := &instance.ServerInstance{HealthChecker: healthcheck.New()}
	t.Cleanup(si.HealthChecker.Shutdown)
	t.Cleanup(func() {
		for _, m := range si.PoolManagers {
			m.Stop()
		}
		for _, d := range si.Discoverers {
			_ = d.Stop()
		}
	})
	return si, c, backends.Backends{"alb1": cl}
}

// kubernetes with in_cluster outside a cluster cannot construct: the
// canonical "discoverer unavailable at startup" condition
func unavailableDiscoverer() *do.Options {
	return &do.Options{Provider: dp.Kubernetes,
		Kubernetes: &kubeopts.Options{InCluster: true}}
}

func TestApplyDiscoveryStartupPolicyFail(t *testing.T) {
	si, c, clients := newDiscoveryFixture(t, unavailableDiscoverer(),
		&do.Query{Service: "svc"}, ao.StartupPolicyFail)
	err := applyDiscoveryConfig(si, c, clients, nil, nil, nil)
	require.Error(t, err, "policy fail: an unavailable discoverer fails startup")
	require.Contains(t, err.Error(), `discoverer "d1" unavailable`)
}

func TestApplyDiscoveryStartupPolicyRetry(t *testing.T) {
	si, c, clients := newDiscoveryFixture(t, unavailableDiscoverer(),
		&do.Query{Service: "svc"}, ao.StartupPolicyRetry)
	err := applyDiscoveryConfig(si, c, clients, nil, nil, nil)
	require.NoError(t, err,
		"policy retry: the ALB serves static members and startup proceeds")
	require.Empty(t, si.Discoverers)
	require.Contains(t, si.PoolManagers, "alb1",
		"the manager exists so a later reload can attach a discoverer")
}

// deadClusterDiscoverer names a reachable-looking kubeconfig whose API
// server is not listening: the client builds, so only a preflight catches it
func deadClusterDiscoverer(t *testing.T) *do.Options {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
clusters:
- cluster: {server: "https://127.0.0.1:1"}
  name: dead
contexts:
- context: {cluster: dead, user: dead}
  name: dead
current-context: dead
users:
- name: dead
  user: {token: not-a-real-token}
`), 0o600))
	return &do.Options{Provider: dp.Kubernetes,
		Kubernetes: &kubeopts.Options{Kubeconfig: path,
			Timeout: timeconv.Duration(2 * time.Second)}}
}

// Informers are lazy, so a discoverer over an unreachable API server starts
// clean and then simply never delivers members. Under policy fail that is
// exactly the silence the preflight exists to convert into a startup error.
func TestApplyDiscoveryPreflightFailsStartup(t *testing.T) {
	si, c, clients := newDiscoveryFixture(t, deadClusterDiscoverer(t),
		&do.Query{Service: "svc"}, ao.StartupPolicyFail)
	err := applyDiscoveryConfig(si, c, clients, nil, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), `discoverer "d1" unavailable`)
	require.Empty(t, si.Discoverers)
}

func TestApplyDiscoveryPreflightSkippedUnderRetry(t *testing.T) {
	si, c, clients := newDiscoveryFixture(t, deadClusterDiscoverer(t),
		&do.Query{Service: "svc"}, ao.StartupPolicyRetry)
	require.NoError(t, applyDiscoveryConfig(si, c, clients, nil, nil, nil))
	require.Contains(t, si.Discoverers, "d1",
		"policy retry keeps watching for the API server to come back")
	for _, d := range si.Discoverers {
		require.NoError(t, d.Stop())
	}
}

func TestApplyDiscoveryEndToEndFileProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "members.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("- address: 10.0.0.1:9090\n- address: 10.0.0.2:9090\n"), 0o644))

	si, c, clients := newDiscoveryFixture(t,
		&do.Options{Provider: dp.File},
		&do.Query{Path: path}, ao.StartupPolicyRetry)
	require.NoError(t, applyDiscoveryConfig(si, c, clients, nil, nil, nil))
	require.Contains(t, si.Discoverers, "d1")
	mgr := si.PoolManagers["alb1"]
	require.NotNil(t, mgr)

	// the discoverer's initial snapshot flows through subscription,
	// manager, template instantiation, and pool swap
	require.Eventually(t, func() bool {
		return len(mgr.MemberNames()) == 2
	}, 5*time.Second, 10*time.Millisecond)
	albClient := clients["alb1"].(*alb.Client)
	require.Len(t, albClient.DynamicPoolNames(), 2)
}

func TestApplyDiscoveryReloadSeedsMembership(t *testing.T) {
	si, c, clients := newDiscoveryFixture(t, unavailableDiscoverer(),
		&do.Query{Service: "svc"}, ao.StartupPolicyRetry)
	require.NoError(t, applyDiscoveryConfig(si, c, clients, nil, nil, nil))

	// simulate a previously-discovered membership on the outgoing manager
	si.PoolManagers["alb1"].ApplySnapshot(discovery.Snapshot{
		{Name: "m1", Scheme: "http", Address: "10.0.0.1:9090"}})
	require.Len(t, si.PoolManagers["alb1"].MemberNames(), 1)
	si.Config = c

	// a no-op reload: the new manager is seeded with the outgoing
	// membership even though the discoverer is still unavailable
	require.NoError(t, applyDiscoveryConfig(si, c, clients, nil, nil, nil))
	require.Equal(t, []string{"alb1-m1"},
		si.PoolManagers["alb1"].MemberNames(),
		"membership preserved across a no-op reload")

	// a changed discovery config must NOT seed. As on a real reload, the
	// new client map is built from the new config.
	c2 := config.NewConfig()
	*c2 = *c
	c2.Backends = map[string]*bo.Options{
		"tmpl": c.Backends["tmpl"], "alb1": c.Backends["alb1"].Clone()}
	c2.Backends["alb1"].ALBOptions.Discovery.MinMembers = 3
	cl2, err := alb.NewClient("alb1", c2.Backends["alb1"], nil, nil, nil, nil)
	require.NoError(t, err)
	albClient2 := cl2.(*alb.Client)
	require.NoError(t, albClient2.ValidateAndStartPool(
		backends.Backends{"alb1": cl2}, nil))
	t.Cleanup(albClient2.StopPool)
	require.NoError(t, applyDiscoveryConfig(si, c2,
		backends.Backends{"alb1": cl2}, nil, nil, nil))
	require.Empty(t, si.PoolManagers["alb1"].MemberNames(),
		"changed discovery config starts fresh")
}

// newTwoDiscovererFixture builds two discovery-backed ALBs, each bound to
// its own discoverer, with independent startup policies
func newTwoDiscovererFixture(t *testing.T, first, second *do.Options,
	firstPolicy, secondPolicy string,
) (*instance.ServerInstance, *config.Config, backends.Backends) {
	t.Helper()
	si, c, clients := newDiscoveryFixture(t, first,
		&do.Query{Service: "svc"}, firstPolicy)

	c.Discovery["d2"] = second
	second.Name = "d2"

	albOpts := bo.New()
	albOpts.Provider = providers.ALB
	albOpts.ALBOptions = &ao.Options{
		MechanismName: "rr",
		Discovery: &ao.DiscoveryOptions{
			DiscovererName:  "d2",
			TemplateBackend: "tmpl",
			Query:           &do.Query{Service: "svc2"},
			StartupPolicy:   secondPolicy,
		},
	}
	require.NoError(t, albOpts.Initialize("alb2"))
	c.Backends["alb2"] = albOpts
	cl, err := alb.NewClient("alb2", albOpts, nil, nil, nil, nil)
	require.NoError(t, err)
	clients["alb2"] = cl
	return si, c, clients
}

// A fail-fast policy belongs to the discoverer its ALB references. One
// ALB's policy has no business failing startup over a discoverer it does
// not use.
func TestApplyDiscoveryStartupPolicyIsPerDiscoverer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "members.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("- name: m1\n  address: 127.0.0.1:9090\n"), 0o600))

	// d1 is reachable and fail-fast; d2 is unreachable but only retry
	reachable := &do.Options{Provider: dp.File, Name: "d1"}
	si, c, clients := newTwoDiscovererFixture(t,
		reachable, unavailableDiscoverer(),
		ao.StartupPolicyFail, ao.StartupPolicyRetry)
	c.Backends["alb1"].ALBOptions.Discovery.Query = &do.Query{Path: path}

	require.NoError(t, applyDiscoveryConfig(si, c, clients, nil, nil, nil),
		"an unreachable retry-policy discoverer must not fail startup for an "+
			"unrelated fail-policy ALB")
	require.Contains(t, si.Discoverers, "d1")
	require.NotContains(t, si.Discoverers, "d2")
	for _, d := range si.Discoverers {
		require.NoError(t, d.Stop())
	}
}

// The converse: the discoverer that actually carries the fail policy still
// fails startup when it cannot be brought up
func TestApplyDiscoveryFailPolicyStillFailsItsOwnDiscoverer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "members.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("- name: m1\n  address: 127.0.0.1:9090\n"), 0o600))

	si, c, clients := newTwoDiscovererFixture(t,
		&do.Options{Provider: dp.File, Name: "d1"}, unavailableDiscoverer(),
		ao.StartupPolicyRetry, ao.StartupPolicyFail)
	c.Backends["alb1"].ALBOptions.Discovery.Query = &do.Query{Path: path}

	err := applyDiscoveryConfig(si, c, clients, nil, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), `discoverer "d2" unavailable`)
}

// A discoverer started before a later fail-policy error must not be left
// running. Until the control plane is published on the ServerInstance
// nothing else can reach it, so a partial publish is the observable
// symptom: normal teardown iterates si.Discoverers, and anything absent
// from it can never be stopped.
func TestApplyDiscoveryDoesNotPublishAPartialControlPlane(t *testing.T) {
	path := filepath.Join(t.TempDir(), "members.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("- name: m1\n  address: 127.0.0.1:9090\n"), 0o600))

	si, c, clients := newTwoDiscovererFixture(t,
		&do.Options{Provider: dp.File, Name: "d1"}, unavailableDiscoverer(),
		ao.StartupPolicyFail, ao.StartupPolicyFail)
	c.Backends["alb1"].ALBOptions.Discovery.Query = &do.Query{Path: path}

	require.Error(t, applyDiscoveryConfig(si, c, clients, nil, nil, nil))
	require.Empty(t, si.Discoverers,
		"a failed apply must leave nothing for teardown to miss")
	require.Empty(t, si.PoolManagers)
}

// A failed apply must also not leave the previous instance's control plane
// published, since it was already stopped on the way in
func TestApplyDiscoveryFailureClearsPreviousControlPlane(t *testing.T) {
	path := filepath.Join(t.TempDir(), "members.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("- name: m1\n  address: 127.0.0.1:9090\n"), 0o600))
	si, c, clients := newDiscoveryFixture(t,
		&do.Options{Provider: dp.File, Name: "d1"},
		&do.Query{Path: path}, ao.StartupPolicyRetry)
	require.NoError(t, applyDiscoveryConfig(si, c, clients, nil, nil, nil))
	require.Contains(t, si.Discoverers, "d1")

	// now make the same discoverer unresolvable under a fail policy
	c.Discovery["d1"] = unavailableDiscoverer()
	c.Discovery["d1"].Name = "d1"
	c.Backends["alb1"].ALBOptions.Discovery.StartupPolicy = ao.StartupPolicyFail
	require.Error(t, applyDiscoveryConfig(si, c, clients, nil, nil, nil))
	require.Empty(t, si.Discoverers)
	require.Empty(t, si.PoolManagers)
}

// A manager seeded from the previous membership has already instantiated
// its member clients and registered their health checks. If its
// subscription then fails under a fail policy, the cleanup must be able to
// reach it: it is not published on the ServerInstance, so anything the
// cleanup misses has no owner left that could ever stop it.
func TestApplyDiscoveryStopsSeededManagerOnSubscribeFailure(t *testing.T) {
	// a file discoverer that starts cleanly, with a query that cannot be
	// subscribed: the file provider requires a path. The discoverer coming
	// up is what puts the failure in the subscription loop rather than the
	// construction loop.
	si, c, clients := newDiscoveryFixture(t,
		&do.Options{Provider: dp.File, Name: "d1"},
		&do.Query{}, ao.StartupPolicyFail)

	// a probed template makes member instantiation observable: a live
	// member holds a health check registration, a stopped one does not. The
	// probe is bounded tightly because the member address is unroutable and
	// stopping a target waits out its in-flight probe.
	tmpl := c.Backends["tmpl"]
	tmpl.HealthCheck = &ho.Options{
		Interval: timeconv.Duration(time.Hour),
		Timeout:  timeconv.Duration(10 * time.Millisecond)}

	// stand up the previous instance's manager by hand, with an applied
	// membership. A prior apply cannot produce this state, because the same
	// unsubscribable query that makes the reload fail would have made the
	// first apply fail too.
	albClient := clients["alb1"].(*alb.Client)
	prev := dynamic.New(dynamic.Config{
		ALB:           albClient,
		Options:       c.Backends["alb1"].ALBOptions.Discovery,
		Template:      tmpl,
		Conf:          c,
		Factories:     providerregistry.SupportedProviders(),
		HealthChecker: si.HealthChecker,
	})
	t.Cleanup(prev.Stop)
	prev.ApplySnapshot(discovery.Snapshot{
		{Name: "m1", Scheme: "http", Address: "10.0.0.1:9090"}})
	require.Equal(t, []string{"alb1-m1"}, prev.MemberNames())
	require.Contains(t, si.HealthChecker.Statuses(), "alb1-m1")
	si.PoolManagers = map[string]*dynamic.Manager{"alb1": prev}
	si.Config = c

	// the reload seeds the new manager from that membership, then fails to
	// subscribe it
	err := applyDiscoveryConfig(si, c, clients, nil, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), `discoverer "d1" unavailable`)
	require.Empty(t, si.PoolManagers,
		"a failed apply must leave nothing for teardown to miss")
	require.NotContains(t, si.HealthChecker.Statuses(), "alb1-m1",
		"the seeded manager's members outlived the failed apply")
}

// A reload hands the discovered sources over: the incoming discoverer is
// subscribed before the outgoing one is stopped, so the shared informers
// behind an unchanged query never lose their last reference and are not
// re-listed, and the outgoing one is nonetheless stopped by the end
func TestApplyDiscoveryReloadStopsOutgoingAfterHandoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "members.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("- address: 10.0.0.1:9090\n"), 0o644))
	si, c, clients := newDiscoveryFixture(t,
		&do.Options{Provider: dp.File},
		&do.Query{Path: path}, ao.StartupPolicyRetry)
	require.NoError(t, applyDiscoveryConfig(si, c, clients, nil, nil, nil))
	outgoing := si.Discoverers["d1"]
	require.NotNil(t, outgoing)
	si.Config = c

	require.NoError(t, applyDiscoveryConfig(si, c, clients, nil, nil, nil))
	incoming := si.Discoverers["d1"]
	require.NotNil(t, incoming)
	require.NotSame(t, outgoing, incoming, "a reload builds a new discoverer")

	// the outgoing discoverer has been stopped: it accepts no subscription
	_, err := outgoing.Subscribe(&do.Query{Path: path}, func(discovery.Snapshot) {})
	require.Error(t, err, "the outgoing discoverer must be stopped once the handoff is done")
	unsub, err := incoming.Subscribe(&do.Query{Path: path}, func(discovery.Snapshot) {})
	require.NoError(t, err, "the incoming discoverer is live")
	unsub()
}
