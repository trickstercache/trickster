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

package l4

import (
	"maps"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

// refusedKey counts the selections an upstream refused
const refusedKey = ""

func pickN(up Upstream, n int) []string {
	seq := make([]string, n)
	for i := range seq {
		if addr, ok := up.Addr(); ok {
			seq[i] = addr
		}
	}
	return seq
}

func tally(seq []string) map[string]int {
	counts := make(map[string]int)
	for _, addr := range seq {
		counts[addr]++
	}
	return counts
}

// assertEveryWindowExact fails unless every run of total consecutive selections matches want,
// wherever the run starts: apportionment is pinned, the rotation's phase is not
func assertEveryWindowExact(t *testing.T, seq []string, want map[string]int) {
	t.Helper()
	var total int
	for _, n := range want {
		total += n
	}
	for start := 0; start+total <= len(seq); start++ {
		if got := tally(seq[start : start+total]); !maps.Equal(got, want) {
			t.Fatalf("selections %d-%d apportioned %v, want %v", start, start+total-1, got, want)
		}
	}
}

func TestPoolUpstreamEveryWindowIsExact(t *testing.T) {
	a := originBackend(t, "a", "10.0.0.1:1")
	b := originBackend(t, "b", "10.0.0.2:1")
	c := originBackend(t, "c", "10.0.0.3:1")
	gone := originBackend(t, "gone", "unresolved.kgw.invalid:1")
	down := originBackend(t, "down", "10.0.0.9:1")

	t.Run("uniform", func(t *testing.T) {
		up := FromBackend(pooledOf(t, "alb", member(a, 1, healthcheck.StatusPassing),
			member(b, 1, healthcheck.StatusPassing), member(c, 1, healthcheck.StatusPassing)))
		assertEveryWindowExact(t, pickN(up, 20),
			map[string]int{"10.0.0.1:1": 1, "10.0.0.2:1": 1, "10.0.0.3:1": 1})
	})
	t.Run("weighted", func(t *testing.T) {
		up := FromBackend(pooledOf(t, "alb", member(a, 1, healthcheck.StatusPassing),
			member(b, 3, healthcheck.StatusPassing), member(c, 2, healthcheck.StatusPassing)))
		assertEveryWindowExact(t, pickN(up, 40),
			map[string]int{"10.0.0.1:1": 1, "10.0.0.2:1": 3, "10.0.0.3:1": 2})
	})
	t.Run("an unhealthy member holds no share", func(t *testing.T) {
		up := FromBackend(pooledOf(t, "alb", member(a, 2, healthcheck.StatusPassing),
			member(down, 5, healthcheck.StatusFailing), member(b, 1, healthcheck.StatusPassing)))
		assertEveryWindowExact(t, pickN(up, 20), map[string]int{"10.0.0.1:1": 2, "10.0.0.2:1": 1})
	})
	t.Run("a refusing member consumes and refuses exactly its weight", func(t *testing.T) {
		up := FromBackend(pooledOf(t, "alb", member(a, 1, healthcheck.StatusPassing),
			member(gone, 3, healthcheck.StatusPassing)))
		assertEveryWindowExact(t, pickN(up, 24), map[string]int{"10.0.0.1:1": 1, refusedKey: 3})
	})
}

func TestPoolUpstreamRefusesWithoutALiveMember(t *testing.T) {
	a := originBackend(t, "a", "10.0.0.1:1")
	b := originBackend(t, "b", "10.0.0.2:1")
	up := FromBackend(pooledOf(t, "alb", member(a, 1, healthcheck.StatusFailing),
		member(b, 2, healthcheck.StatusFailing)))
	for range 6 {
		if addr, ok := up.Addr(); ok {
			t.Fatalf("a pool with no healthy member dialed %s", addr)
		}
	}
}

func TestPoolUpstreamNestedApportionment(t *testing.T) {
	// outer 3:1 over two inner pools, the first of which is itself weighted 2:1
	newOuter := func(t *testing.T, innerStatus int32) Upstream {
		inner1 := pooledOf(t, "inner1",
			member(originBackend(t, "a", "10.1.0.1:1"), 2, innerStatus),
			member(originBackend(t, "b", "10.1.0.2:1"), 1, innerStatus))
		inner2 := pooledOf(t, "inner2",
			member(originBackend(t, "c", "10.2.0.1:1"), 1, healthcheck.StatusPassing))
		return FromBackend(pooledOf(t, "outer", member(inner1, 3, healthcheck.StatusPassing),
			member(inner2, 1, healthcheck.StatusPassing)))
	}
	t.Run("each level apportions by its own weights", func(t *testing.T) {
		// 12 selections hold 9 for inner1, a whole number of its rotations of 3
		got := tally(pickN(newOuter(t, healthcheck.StatusPassing), 36))
		want := map[string]int{"10.1.0.1:1": 18, "10.1.0.2:1": 9, "10.2.0.1:1": 9}
		if !maps.Equal(got, want) {
			t.Errorf("nested apportionment = %v, want %v", got, want)
		}
	})
	t.Run("an inner pool with no live member refuses its share", func(t *testing.T) {
		seq := pickN(newOuter(t, healthcheck.StatusFailing), 16)
		assertEveryWindowExact(t, seq, map[string]int{refusedKey: 3, "10.2.0.1:1": 1})
	})
}
