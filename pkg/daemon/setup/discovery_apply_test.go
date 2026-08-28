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
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	dp "github.com/trickstercache/trickster/v2/pkg/discovery/providers"

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
		Kubernetes: &do.KubernetesOptions{InCluster: true}}
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
