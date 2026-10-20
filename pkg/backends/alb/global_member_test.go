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

package alb

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	tsmoptions "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"

	"github.com/stretchr/testify/require"
)

const globalTestALB = "global"

func newGlobalTestMember(t *testing.T, provider string, po *prop.Options) backends.Backend {
	t.Helper()
	o := bo.New()
	o.Name = keys.Member
	o.Provider = provider
	o.OriginURL = "http://example.com/prometheus"
	o.Prometheus = po
	member, err := backends.New(keys.Member, o, nil, http.NotFoundHandler(), nil)
	require.NoError(t, err)
	return member
}

func newGlobalTestALB(t *testing.T, mechanism string, evaluation string) *Client {
	t.Helper()
	o := bo.New()
	o.Name = globalTestALB
	o.Provider = providers.ALB
	o.ALBOptions = ao.New()
	o.ALBOptions.MechanismName = mechanism
	o.ALBOptions.TSMOptions.Evaluation = evaluation
	o.ALBOptions.Pool = ao.Members(keys.Member)
	if mechanism != names.MechanismTSM {
		client, err := NewClient(globalTestALB, o, nil, nil, nil, nil)
		require.NoError(t, err)
		return client.(*Client)
	}
	// a merge mechanism needs provider factories, which a pool check doesn't use
	base, err := backends.New(globalTestALB, o, nil, http.NotFoundHandler(), nil)
	require.NoError(t, err)
	return &Client{Backend: base}
}

func TestValidateAndStartPoolGlobalEvaluationMembers(t *testing.T) {
	start := func(t *testing.T, member backends.Backend) error {
		alb := newGlobalTestALB(t, names.MechanismTSM, tsmoptions.EvaluationGlobal)
		return alb.ValidateAndStartPool(backends.Backends{globalTestALB: alb, keys.Member: member},
			healthcheck.StatusLookup{keys.Member: &healthcheck.Status{}})
	}
	t.Run("mimir serves remote read", func(t *testing.T) {
		require.NoError(t, start(t, newGlobalTestMember(t, providers.Prometheus, &prop.Options{Flavor: prop.FlavorMimir})))
	})
	t.Run("thanos with a StoreAPI endpoint", func(t *testing.T) {
		require.NoError(t, start(t, newGlobalTestMember(t, providers.Prometheus, &prop.Options{
			Flavor: prop.FlavorThanos, SeriesEndpoint: prop.StoreAPIScheme + "://thanos:10901",
		})))
	})
	t.Run("thanos without an endpoint", func(t *testing.T) {
		err := start(t, newGlobalTestMember(t, providers.Prometheus, &prop.Options{Flavor: prop.FlavorThanos}))
		require.ErrorIs(t, err, errors.ErrGlobalEvaluationMember)
		require.ErrorIs(t, err, prop.ErrSeriesEndpoint)
	})
	t.Run("prometheus without a series api", func(t *testing.T) {
		require.ErrorIs(t, start(t, newGlobalTestMember(t, providers.Prometheus, nil)), errors.ErrGlobalEvaluationMember)
	})
	t.Run("nested alb", func(t *testing.T) {
		leaf := newGlobalTestMember(t, providers.Prometheus, &prop.Options{Flavor: prop.FlavorMimir})
		inner := newGlobalTestALB(t, names.MechanismRR, "")
		require.NoError(t, inner.ValidateAndStartPool(backends.Backends{keys.Member: leaf},
			healthcheck.StatusLookup{keys.Member: &healthcheck.Status{}}))
		outer := newGlobalTestALB(t, names.MechanismTSM, tsmoptions.EvaluationGlobal)
		outer.Configuration().ALBOptions.Pool = ao.Members(keys.Leaf)
		err := outer.ValidateAndStartPool(backends.Backends{keys.Leaf: inner, keys.Member: leaf},
			healthcheck.StatusLookup{keys.Member: &healthcheck.Status{}})
		require.ErrorIs(t, err, errors.ErrGlobalEvaluationMember)
	})
	t.Run("merge evaluation needs no series api", func(t *testing.T) {
		alb := newGlobalTestALB(t, names.MechanismTSM, tsmoptions.EvaluationMerge)
		member := newGlobalTestMember(t, providers.Prometheus, nil)
		require.NoError(t, alb.ValidateAndStartPool(backends.Backends{globalTestALB: alb, keys.Member: member},
			healthcheck.StatusLookup{keys.Member: &healthcheck.Status{}}))
	})
}

type fakeCache struct {
	cache.Cache
}

// cacheMechanism records the cache and options its client hands it
type cacheMechanism struct {
	http.Handler
	cache   cache.Cache
	options *bo.Options
}

func (*cacheMechanism) Name() types.Name { return names.MechanismTSM }

func (m *cacheMechanism) SetCache(c cache.Cache, o *bo.Options) { m.cache, m.options = c, o }

func TestClientSetCacheReachesMechanism(t *testing.T) {
	alb := newGlobalTestALB(t, names.MechanismTSM, tsmoptions.EvaluationGlobal)
	mech := &cacheMechanism{Handler: http.NotFoundHandler()}
	alb.handler = mech
	c := &fakeCache{}
	alb.SetCache(c)
	require.Same(t, c, alb.Cache())
	require.Same(t, c, mech.cache)
	require.Same(t, alb.Configuration(), mech.options)

	plain := newGlobalTestALB(t, names.MechanismRR, "")
	plain.SetCache(c)
	require.Same(t, c, plain.Cache())
}
