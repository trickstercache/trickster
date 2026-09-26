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
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type atomicCounter struct{ hits atomic.Int64 }

func (c *atomicCounter) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	c.hits.Add(1)
	w.WriteHeader(http.StatusOK)
}

// startALBFromYAML loads an alb block as the config loader does, then starts its pool over
// counting members named by the pool
func startALBFromYAML(t *testing.T, initialize bool, albYAML string) (*Client, map[string]*atomicCounter) {
	t.Helper()
	a := &ao.Options{}
	require.NoError(t, yaml.Unmarshal([]byte(albYAML), a))
	o := bo.New()
	o.Provider = providers.ALB
	o.ALBOptions = a
	names := a.Pool.Names()
	if initialize {
		require.NoError(t, o.Initialize("dedupe"))
	}
	cl, err := NewClient("dedupe", o, nil, nil, nil, nil)
	require.NoError(t, err)
	c := cl.(*Client)
	t.Cleanup(c.StopPool)
	clients := backends.Backends{"dedupe": cl}
	hits := make(map[string]*atomicCounter)
	for _, name := range names {
		if _, ok := hits[name]; ok {
			continue
		}
		hits[name] = &atomicCounter{}
		mo := bo.New()
		mo.Name = name
		b, err := backends.New(name, mo, nil, hits[name], nil)
		require.NoError(t, err)
		clients[name] = b
	}
	require.NoError(t, c.ValidateAndStartPool(clients, nil))
	return c, hits
}

func serve(c *Client, n int) {
	h := c.Handlers()[providers.ALB]
	for range n {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	}
}

func TestRepeatedPoolMembersAddNoShare(t *testing.T) {
	c, hits := startALBFromYAML(t, true, "mechanism: rr\npool: [a, a, b]\n")
	require.Equal(t, 2, c.Pool().ConfiguredLen())
	require.Contains(t, c.Configuration().ALBOptions.PoolRepeatWarning("dedupe"), "{name: a, weight: 2}")
	serve(c, 12)
	require.EqualValues(t, 6, hits["a"].hits.Load())
	require.EqualValues(t, 6, hits["b"].hits.Load())
}

func TestWeightIsTheOnlyWayToWeight(t *testing.T) {
	c, hits := startALBFromYAML(t, true, "mechanism: rr\npool: [{name: a, weight: 3}, b]\n")
	require.Empty(t, c.Configuration().ALBOptions.PoolRepeatWarning("dedupe"))
	serve(c, 12)
	require.EqualValues(t, 9, hits["a"].hits.Load())
	require.EqualValues(t, 3, hits["b"].hits.Load())
}

func TestFanoutDispatchesToARepeatedMemberOnce(t *testing.T) {
	c, hits := startALBFromYAML(t, true, "mechanism: nlm\npool: [a, b, a]\n")
	require.Equal(t, 2, c.Pool().ConfiguredLen())
	serve(c, 1)
	require.EqualValues(t, 1, hits["a"].hits.Load())
	require.EqualValues(t, 1, hits["b"].hits.Load())
}

// options that never went through the loader still yield a pool of unique members
func TestStartPoolSkipsRepeatsInUninitializedOptions(t *testing.T) {
	c, hits := startALBFromYAML(t, false, "mechanism: rr\npool: [a, b, a, a]\n")
	require.Equal(t, 2, c.Pool().ConfiguredLen())
	serve(c, 10)
	require.EqualValues(t, 5, hits["a"].hits.Load())
	require.EqualValues(t, 5, hits["b"].hits.Load())
}
