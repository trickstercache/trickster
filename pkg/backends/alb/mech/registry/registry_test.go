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

package registry

import (
	"testing"

	alberr "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
)

func TestNamesAreUnique(t *testing.T) {
	usedNames := sets.New([]types.Name{})
	for _, m := range registry {
		if usedNames.Contains(m.Name) {
			t.Errorf("mechanism Name %s has been reused; Names must be unique.",
				m.Name)
		}
		if usedNames.Contains(m.ShortName) {
			t.Errorf("mechanism %s reuses ShortName %s; ShortNames must be unique.",
				m.Name, m.ShortName)
		}
		usedNames.Set(m.Name)
		usedNames.Set(m.ShortName)
	}
}

func TestIsRegistered(t *testing.T) {
	if ok := IsRegistered(names.MechanismRR); !ok {
		t.Error("expected true")
	}
	if ok := IsRegistered(types.Name("invalid")); ok {
		t.Error("expected false")
	}
}

func TestNewUnregisteredMechanism(t *testing.T) {
	_, err := New("nonexistent", nil, nil)
	require.ErrorIs(t, err, alberr.ErrUnsupportedMechanism)
}

func TestNewTSM(t *testing.T) {
	for _, name := range []string{tsm.Name, tsm.ShortName} {
		t.Run(name, func(t *testing.T) {
			o := &options.Options{OutputFormat: providers.Prometheus}
			m, err := New(name, o, rt.Lookup{providers.Prometheus: prometheus.NewClient})
			require.NoError(t, err)
			require.Equal(t, tsm.ShortName, m.Name())
			require.Implements(t, (*types.PoolMechanism)(nil), m)

			_, err = New(name, nil, nil)
			require.ErrorIs(t, err, errors.ErrInvalidOptions)
			_, err = New(name, o, nil)
			require.ErrorIs(t, err, alberr.ErrInvalidTimeSeriesMergeProvider)
			o.OutputFormat = "not-a-provider"
			_, err = New(name, o, nil)
			require.ErrorIs(t, err, alberr.ErrInvalidTimeSeriesMergeProvider)
		})
	}
}

func TestNewRoundRobinNilOptions(t *testing.T) {
	m, err := New(names.MechanismRR, nil, nil)
	require.NoError(t, err)
	require.Equal(t, names.MechanismRR, m.Name())
}

func TestNewNilConstructor(t *testing.T) {
	original := registryByName
	t.Cleanup(func() { registryByName = original })
	registryByName = compileSupportedByName([]types.RegistryEntry{
		{Name: "nil_constructor", ShortName: "nil"},
	})
	for _, name := range []string{"nil_constructor", "nil"} {
		m, err := New(name, nil, nil)
		require.ErrorIs(t, err, alberr.ErrUnsupportedMechanism)
		require.Nil(t, m)
	}
}
