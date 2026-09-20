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
package registry

import (
	"errors"
	"testing"

	alberr "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/lbtest"

	"github.com/stretchr/testify/require"
)

// what each mechanism may serve; a change here is a change to what configs validate
var wantPlanes = map[types.Name]types.Plane{
	names.MechanismRR:  types.PlaneHTTP | types.PlaneStream,
	names.MechanismP2C: types.PlaneHTTP,
	names.MechanismHRW: types.PlaneHTTP,
	names.MechanismLT:  types.PlaneHTTP,
	names.MechanismLC:  types.PlaneHTTP,
	names.MechanismFR:  types.PlaneHTTP,
	names.MechanismFGR: types.PlaneHTTP,
	names.MechanismNLM: types.PlaneHTTP,
	names.MechanismTSM: types.PlaneHTTP,
	names.MechanismUR:  types.PlaneHTTP | types.PlaneNative,
}

// the strategies whose weights are an exact apportionment contract
var exactWeights = map[types.Name]bool{names.MechanismRR: true}

func TestEntriesDeclareOneConstructorAndTheirPlanes(t *testing.T) {
	require.Len(t, registry, len(wantPlanes))
	for _, e := range registry {
		require.NotEqual(t, e.New == nil, e.NewSelector == nil,
			"%s must set exactly one of New and NewSelector", e.Name)
		want, ok := wantPlanes[e.ShortName]
		require.True(t, ok, "%s has no expected planes", e.ShortName)
		require.Equal(t, want, e.Planes, e.ShortName)
		require.True(t, e.Planes.Has(types.PlaneHTTP), "%s must serve HTTP", e.ShortName)
		// only a strategy can serve a plane that has no HTTP handler to call
		if e.NewSelector == nil {
			require.False(t, e.Planes.Has(types.PlaneStream), e.ShortName)
		}
	}
}

func TestCompilePanicsWithoutExactlyOneConstructor(t *testing.T) {
	mech := func(*options.Options, rt.Lookup) (types.Mechanism, error) { return nil, nil }
	sel := func(*options.Options) (lb.Selector, error) { return nil, nil }
	for name, e := range map[string]types.RegistryEntry{
		"neither": {Name: "long", ShortName: "short"},
		"both":    {Name: "long", ShortName: "short", New: mech, NewSelector: sel},
	} {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, func() { compileSupportedByName([]types.RegistryEntry{e}) })
		})
	}
}

func TestSupports(t *testing.T) {
	for _, name := range []types.Name{names.MechanismRR, names.MechanismRoundRobin} {
		require.True(t, Supports(name, types.PlaneHTTP), name)
		require.True(t, Supports(name, types.PlaneStream), name)
		require.True(t, Supports(name, types.PlaneHTTP|types.PlaneStream), name)
		require.False(t, Supports(name, types.PlaneNative), name)
	}
	require.False(t, Supports(names.MechanismFR, types.PlaneStream))
	require.False(t, Supports(names.MechanismTSM, types.PlaneStream))
	require.True(t, Supports(names.MechanismUR, types.PlaneNative))
	require.False(t, Supports(names.MechanismUR, types.PlaneStream))
	require.False(t, Supports("nonexistent", types.PlaneHTTP))
	require.False(t, Supports(names.MechanismRR, 0), "no plane is not a supported plane")

	require.Equal(t, []types.Name{names.MechanismRR}, Supporting(types.PlaneStream))
	require.Equal(t, []types.Name{names.MechanismUR}, Supporting(types.PlaneNative))
	require.Len(t, Supporting(types.PlaneHTTP), len(registry))
}

func TestNewWrapsAStrategyForHTTP(t *testing.T) {
	for _, name := range []types.Name{names.MechanismRR, names.MechanismRoundRobin} {
		m, err := New(name, &options.Options{}, nil)
		require.NoError(t, err)
		require.Equal(t, names.MechanismRR, m.Name())
		pm, ok := m.(types.PickerMechanism)
		require.True(t, ok, "a strategy is served as a picker mechanism")
		require.NotNil(t, pm.Picker())
	}
	// each mechanism owns its strategy: two ALBs never share a rotation
	a, _ := New(names.MechanismRR, nil, nil)
	b, _ := New(names.MechanismRR, nil, nil)
	require.NotSame(t, a.(types.PickerMechanism).Picker(), b.(types.PickerMechanism).Picker())
}

func TestNewBalancer(t *testing.T) {
	b, err := NewBalancer(names.MechanismRR, nil)
	require.NoError(t, err)
	require.NotNil(t, b)
	require.Nil(t, b.Pool())
	for _, name := range []types.Name{names.MechanismFR, names.MechanismUR, "nonexistent"} {
		_, err := NewBalancer(name, nil)
		require.ErrorIs(t, err, alberr.ErrUnsupportedMechanism, name)
	}
}

func TestSelectorConstructorErrorsSurface(t *testing.T) {
	errBad := errors.New("bad strategy options")
	saved := registryByName
	t.Cleanup(func() { registryByName = saved })
	registryByName = compileSupportedByName([]types.RegistryEntry{{
		Name: "broken_long", ShortName: "broken", Planes: types.PlaneHTTP,
		NewSelector: func(*options.Options) (lb.Selector, error) { return nil, errBad },
	}})
	_, err := New("broken", nil, nil)
	require.ErrorIs(t, err, errBad)
	_, err = NewBalancer("broken", nil)
	require.ErrorIs(t, err, errBad)
}

// every registered strategy is held to the core's selector contract
func TestRegisteredSelectorsConform(t *testing.T) {
	var strategies int
	for _, e := range registry {
		if e.NewSelector == nil {
			continue
		}
		strategies++
		t.Run(e.ShortName, func(t *testing.T) {
			lbtest.Run(t, func() lb.Selector {
				s, err := e.NewSelector(&options.Options{})
				if err != nil {
					t.Fatal(err)
				}
				return s
			}, lbtest.Options{ExactWeights: exactWeights[e.ShortName]})
		})
	}
	require.NotZero(t, strategies)
}
