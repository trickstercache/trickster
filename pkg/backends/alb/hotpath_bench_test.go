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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
)

type nopHandler struct{}

func (nopHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

func benchTargets(n, weight int) pool.Targets {
	targets := make(pool.Targets, n)
	for i := range targets {
		w := 1
		if weight > 1 && i%2 == 0 {
			w = weight
		}
		targets[i] = pool.NewWeightedTarget(nopHandler{}, passingStatus(), nil, w)
	}
	return targets
}

// newStaticPoolALB installs the pool the way startup does: directly on the
// mechanism, before any traffic
func newStaticPoolALB(t testing.TB, targets pool.Targets) *Client {
	c := newRRALB(t, "bench-static")
	c.handler.(types.PoolMechanism).SetPool(pool.New(targets, 0))
	return c
}

// newDiscoveredPoolALB installs the pool through the runtime discovery
// swap path (several times, as a churning discoverer would)
func newDiscoveredPoolALB(t testing.TB, targets pool.Targets) *Client {
	c := newRRALB(t, "bench-discovered")
	for range 3 {
		if !c.SetDynamicTargets(targets) {
			t.Fatal("swap rejected")
		}
	}
	return c
}

// waitZeroAllocSteadyState spins until the pool's async refresh worker
// has drained and dispatch serves the cached zero-alloc fast path
func waitZeroAllocSteadyState(h http.Handler, w *httptest.ResponseRecorder, r *http.Request) {
	deadline := time.Now().Add(2 * time.Second)
	for testing.AllocsPerRun(1, func() { h.ServeHTTP(w, r) }) != 0 {
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// TestHotPathAllocParityStaticVsDiscovered enforces the step-33 contract:
// with a stable pool, request dispatch through a discovery-installed pool
// allocates exactly what a statically-built pool does -- zero.
func TestHotPathAllocParityStaticVsDiscovered(t *testing.T) {
	targets := benchTargets(8, 1)
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	w := httptest.NewRecorder()

	static := newStaticPoolALB(t, targets)
	defer static.StopPool()
	discovered := newDiscoveredPoolALB(t, targets)
	defer discovered.StopPool()

	waitZeroAllocSteadyState(static.Handlers()[providers.ALB], w, r)
	waitZeroAllocSteadyState(discovered.Handlers()[providers.ALB], w, r)
	staticAllocs := testing.AllocsPerRun(1000, func() {
		static.Handlers()[providers.ALB].ServeHTTP(w, r)
	})
	discoveredAllocs := testing.AllocsPerRun(1000, func() {
		discovered.Handlers()[providers.ALB].ServeHTTP(w, r)
	})
	if staticAllocs != discoveredAllocs {
		t.Errorf("alloc delta on the hot path: static=%v discovered=%v",
			staticAllocs, discoveredAllocs)
	}
	if discoveredAllocs != 0 {
		t.Errorf("expected zero allocations per dispatch, got %v",
			discoveredAllocs)
	}

	// weighted selection must hold the same bar
	weighted := newDiscoveredPoolALB(t, benchTargets(8, 3))
	defer weighted.StopPool()
	waitZeroAllocSteadyState(weighted.Handlers()[providers.ALB], w, r)
	if got := testing.AllocsPerRun(1000, func() {
		weighted.Handlers()[providers.ALB].ServeHTTP(w, r)
	}); got != 0 {
		t.Errorf("expected zero allocations for weighted dispatch, got %v", got)
	}
}

func benchmarkDispatch(b *testing.B, c *Client) {
	b.Helper()
	h := c.Handlers()[providers.ALB]
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	w := httptest.NewRecorder()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		h.ServeHTTP(w, r)
	}
}

func BenchmarkALBDispatchStaticPool(b *testing.B) {
	c := newStaticPoolALB(b, benchTargets(8, 1))
	defer c.StopPool()
	benchmarkDispatch(b, c)
}

func BenchmarkALBDispatchDiscoveredPool(b *testing.B) {
	c := newDiscoveredPoolALB(b, benchTargets(8, 1))
	defer c.StopPool()
	benchmarkDispatch(b, c)
}

func BenchmarkALBDispatchDiscoveredWeighted(b *testing.B) {
	c := newDiscoveredPoolALB(b, benchTargets(8, 3))
	defer c.StopPool()
	benchmarkDispatch(b, c)
}
