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
	"context"
	"io"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

// discoveryRetireDrain is the drain a stopped manager's members get before their idle connections close
const discoveryRetireDrain = 100 * time.Millisecond

func sourceRetired(member backends.Backend) bool {
	_, err := member.(backends.TSMGlobalSourceProvider).TSMGlobalSource()
	return err != nil
}

func TestFailedDiscoveryApplyKeepsTheRestoredALBsMembers(t *testing.T) {
	si, c, clients := newDiscoveryFixture(t, unavailableDiscoverer(), &do.Query{Service: "svc"}, ao.StartupPolicyRetry)
	// members read their raw series over a connection of their own, which retiring them closes
	tmpl := bo.New()
	tmpl.Provider = providers.Prometheus
	tmpl.IsTemplate = true
	tmpl.Prometheus = &po.Options{Flavor: po.FlavorThanos, SeriesEndpoint: po.StoreAPIScheme + "://thanos-sidecar:10901"}
	require.NoError(t, tmpl.Initialize("tmpl"))
	c.Backends["tmpl"] = tmpl
	c.MgmtConfig.ReloadDrainTimeout = timeconv.Duration(discoveryRetireDrain)
	caches := cache.Lookup{bo.DefaultBackendCacheName: nil}
	require.NoError(t, applyDiscoveryConfig(si, c, clients, caches, nil, nil))
	si.PoolManagers["alb1"].ApplySnapshot(discovery.Snapshot{{Name: "m1", Scheme: "http", Address: "10.0.0.1:9090"}})
	si.Config = c
	served := clients["alb1"].(*alb.Client)
	targets := served.Pool().ConfiguredTargets()
	require.Len(t, targets, 1)
	member := targets[0].Backend()
	source, err := member.(backends.TSMGlobalSourceProvider).TSMGlobalSource()
	require.NoError(t, err)

	// a reload whose discoverer fails fast stops the outgoing manager, then fails, so the ALB serving the member is restored
	next := config.NewConfig()
	*next = *c
	next.Backends = map[string]*bo.Options{"tmpl": tmpl, "alb1": c.Backends["alb1"].Clone()}
	next.Backends["alb1"].ALBOptions.Discovery.StartupPolicy = ao.StartupPolicyFail
	cl, err := alb.NewClient("alb1", next.Backends["alb1"], nil, nil, nil, nil)
	require.NoError(t, err)
	require.NoError(t, cl.(*alb.Client).ValidateAndStartPool(backends.Backends{"alb1": cl}, nil))
	t.Cleanup(cl.(*alb.Client).StopPool)
	require.Error(t, applyDiscoveryConfig(si, next, backends.Backends{"alb1": cl}, caches, nil, nil))
	require.Never(t, func() bool { return sourceRetired(member) }, 3*discoveryRetireDrain, time.Millisecond,
		"the restored ALB's member was retired")
	got, err := member.(backends.TSMGlobalSourceProvider).TSMGlobalSource()
	require.NoError(t, err)
	require.Same(t, source, got, "the restored ALB reads its member through the same connection")

	// a reload that commits retires the ALB, and the members its pool kept with it
	backends.Retire(context.Background(), served)
	require.True(t, sourceRetired(member))
	require.Error(t, source.(io.Closer).Close(), "the member's connection was closed")
}
