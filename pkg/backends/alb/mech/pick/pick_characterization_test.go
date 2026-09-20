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

package pick

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
)

// sequenceRecorder collects the member index of each dispatch, in order
type sequenceRecorder struct {
	seq []int
}

func (s *sequenceRecorder) handler(i int) http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		s.seq = append(s.seq, i)
	})
}

func recordedTargets(rec *sequenceRecorder, weights []int) pool.Targets {
	targets := make(pool.Targets, len(weights))
	for i, w := range weights {
		targets[i] = pool.NewWeightedTarget(rec.handler(i), passingStatus(), nil, w)
	}
	return targets
}

func sum(weights []int) int {
	var total int
	for _, w := range weights {
		total += w
	}
	return total
}

func serveN(h http.Handler, n int) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	for range n {
		h.ServeHTTP(w, r)
	}
}

// assertEveryWindowExact fails unless every run of total consecutive selections gives each
// member exactly its weight, wherever the run starts
func assertEveryWindowExact(t *testing.T, seq, weights []int) {
	t.Helper()
	total := sum(weights)
	counts := make([]int, len(weights))
	for i, m := range seq {
		counts[m]++
		if i >= total {
			counts[seq[i-total]]--
		}
		if i < total-1 {
			continue
		}
		if !slices.Equal(counts, weights) {
			t.Fatalf("window ending at selection %d apportioned %v, want %v", i, counts, weights)
		}
	}
}

func TestRoundRobinEveryWindowIsExact(t *testing.T) {
	for name, weights := range map[string][]int{
		"uniform":      {1, 1, 1, 1},
		"weighted":     {1, 3, 2},
		"heavy first":  {7, 1, 1},
		"single":       {4},
		"uniform of 2": {2, 2},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &sequenceRecorder{}
			p := pool.New(recordedTargets(rec, weights), 0)
			defer p.Stop()
			h := newRR()
			h.SetPool(p)
			serveN(h, 7*sum(weights)+3)
			assertEveryWindowExact(t, rec.seq, weights)
		})
	}
}

// the order of a rotation, from a fixed start: a heavier member's turns are spread through
// it rather than taken back to back
func TestRoundRobinSequence(t *testing.T) {
	weights := []int{1, 3, 2}
	rec := &sequenceRecorder{}
	p := pool.New(recordedTargets(rec, weights), 0)
	defer p.Stop()
	h := New(names.MechanismRR, rr.NewAt(0))
	h.SetPool(p)
	serveN(h, 2*sum(weights))
	want := []int{1, 2, 1, 1, 2, 0, 1, 2, 1, 1, 2, 0}
	if !slices.Equal(rec.seq, want) {
		t.Errorf("weighted sequence = %v, want %v", rec.seq, want)
	}

	rec = &sequenceRecorder{}
	u := pool.New(recordedTargets(rec, []int{1, 1, 1}), 0)
	defer u.Stop()
	h = New(names.MechanismRR, rr.NewAt(0))
	h.SetPool(u)
	serveN(h, 6)
	want = []int{1, 2, 0, 1, 2, 0}
	if !slices.Equal(rec.seq, want) {
		t.Errorf("uniform sequence = %v, want %v", rec.seq, want)
	}
}

// a pool swapped for one of the same membership mid-rotation must not disturb apportionment:
// the rotation belongs to the mechanism, not to the pool
func TestRoundRobinApportionmentSurvivesConcurrentSetPool(t *testing.T) {
	weights := []int{2, 1, 3}
	rec := &sequenceRecorder{}
	targets := recordedTargets(rec, weights)
	first := pool.New(targets, 0)
	h := newRR()
	h.SetPool(first)

	pools := []pool.Pool{first}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 256 {
			select {
			case <-stop:
				return
			default:
			}
			p := pool.New(targets, 0)
			pools = append(pools, p)
			h.SetPool(p)
			runtime.Gosched()
		}
	})
	serveN(h, 2000*sum(weights))
	close(stop)
	wg.Wait()
	for _, p := range pools {
		p.Stop()
	}
	assertEveryWindowExact(t, rec.seq, weights)
}
