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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"go.yaml.in/yaml/v3"
)

// a weight written in a static pool reaches the pool's targets unchanged, in pool order, and
// the round robin mechanism apportions requests by it
func TestStaticYAMLWeightsReachThePool(t *testing.T) {
	a := &ao.Options{}
	require.NoError(t, yaml.Unmarshal([]byte(`
mechanism: rr
pool:
  - a
  - name: b
    weight: 3
  - name: c
    weight: 0
  - name: d
    weight: 2
`), a))
	o := bo.New()
	o.Provider = providers.ALB
	o.ALBOptions = a
	cl, err := NewClient("weighted", o, nil, nil, nil, nil)
	require.NoError(t, err)
	c := cl.(*Client)
	t.Cleanup(c.StopPool)

	clients := backends.Backends{"weighted": cl}
	hits := make(map[string]*countingHandler)
	for _, name := range a.Pool.Names() {
		hits[name] = &countingHandler{}
		mo := bo.New()
		mo.Name = name
		b, err := backends.New(name, mo, nil, hits[name], nil)
		require.NoError(t, err)
		clients[name] = b
	}
	require.NoError(t, c.ValidateAndStartPool(clients, nil))

	want := map[string]int{"a": 1, "b": 3, "c": 1, "d": 2}
	targets := c.Pool().ConfiguredTargets()
	require.Len(t, targets, len(want))
	var total int
	for i, tgt := range targets {
		require.Equal(t, a.Pool[i].Name, tgt.Name(), "pool order is preserved")
		require.Equal(t, want[tgt.Name()], tgt.Weight(), tgt.Name())
		total += tgt.Weight()
	}

	const cycles = 4
	h := c.Handlers()[providers.ALB]
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	for range cycles * total {
		h.ServeHTTP(w, r)
	}
	for name, weight := range want {
		require.Equal(t, cycles*weight, hits[name].hits, name)
	}
}

func TestIdleGoroutinesPerALB(t *testing.T) {
	baseline := goleak.IgnoreCurrent()
	clients := make([]*Client, 100)
	for i := range clients {
		clients[i] = newStaticPoolALB(t, benchTargets(8, 1))
	}
	goleak.VerifyNone(t, baseline)
	for _, c := range clients {
		c.StopPool()
	}
	goleak.VerifyNone(t, baseline)
}
