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

package spread

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"

	"github.com/stretchr/testify/require"
)

func TestSpreadMechanisms(t *testing.T) {
	for _, test := range []struct {
		entry     types.RegistryEntry
		name      types.Name
		spread    types.Spread
		protocols []string
	}{
		{RegistryEntryRace(), names.MechanismRace, types.SpreadRace, []string{"tcp", "tls"}},
		{RegistryEntryMirror(), names.MechanismMirror, types.SpreadMirror, []string{"udp"}},
	} {
		require.Equal(t, test.name, test.entry.ShortName)
		require.Equal(t, types.PlaneStream, test.entry.Planes)
		require.Equal(t, test.protocols, test.entry.Protocols)
		m, err := test.entry.New(nil, nil)
		require.NoError(t, err)
		require.Equal(t, test.name, m.Name())
		sm, ok := m.(types.SpreadMechanism)
		require.True(t, ok)
		require.Equal(t, test.spread, sm.Spread())

		// stopping before a pool is set is harmless, and a set pool is stopped
		sm.StopPool()
		require.Nil(t, sm.Pool())
		p := pool.New(nil, 0)
		sm.SetPool(p)
		require.Equal(t, p, sm.Pool())
		sm.StopPool()

		w := httptest.NewRecorder()
		m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		require.Equal(t, http.StatusBadGateway, w.Code)
	}
}
