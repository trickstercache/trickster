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

package pool

import (
	"net/http"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

// Subtests are named by axis so -race or panic output points at the failing scenario.
func TestPoolRace(t *testing.T) {
	t.Run("stop_concurrent", testPoolStopConcurrent)
	t.Run("stop_during_transitions", testPoolStopDuringTransitions)
	t.Run("transition_storm_converges", testPoolTransitionStormConverges)
	t.Run("transition_racing_construction", testPoolTransitionRacingConstruction)
	t.Run("target_without_constructor", testPoolTargetWithoutConstructor)
}

func testPoolStopConcurrent(t *testing.T) {
	const iterations = 200
	for i := range iterations {
		s := &healthcheck.Status{}
		tgt := NewTarget(http.NotFoundHandler(), s, nil)
		p := New(Targets{tgt}, 1)

		var start sync.WaitGroup
		start.Add(1)
		var done sync.WaitGroup
		done.Add(2)

		var panics atomic.Int32
		stop := func() {
			defer done.Done()
			defer func() {
				if r := recover(); r != nil {
					panics.Add(1)
				}
			}()
			start.Wait()
			p.Stop()
		}
		go stop()
		go stop()
		start.Done()
		done.Wait()

		if panics.Load() > 0 {
			t.Fatalf("iteration %d: Stop panicked on concurrent invocation", i)
		}
	}
}

// Stop racing in-flight transitions must neither deadlock nor panic, and nothing may be
// published once it has returned
func testPoolStopDuringTransitions(t *testing.T) {
	for range 200 {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		p := New(Targets{NewTarget(http.NotFoundHandler(), st, nil)}, 1)
		var wg sync.WaitGroup
		wg.Go(func() {
			for range 50 {
				st.Set(healthcheck.StatusFailing)
				st.Set(healthcheck.StatusPassing)
			}
		})
		runtime.Gosched()
		p.Stop()
		frozen := p.Targets()
		wg.Wait()
		st.Set(healthcheck.StatusFailing)
		st.Set(healthcheck.StatusPassing)
		st.Set(healthcheck.StatusFailing)
		if got := p.Targets(); !slices.Equal(got, frozen) {
			t.Fatalf("a stopped pool republished: %d then %d targets", len(frozen), len(got))
		}
	}
}

// concurrent transitions on every member must leave the dispatchable set matching the final
// statuses, with no wait
func testPoolTransitionStormConverges(t *testing.T) {
	const n = 16
	statuses := make([]*healthcheck.Status, n)
	targets := make(Targets, n)
	for i := range n {
		statuses[i] = &healthcheck.Status{}
		targets[i] = NewTarget(http.NotFoundHandler(), statuses[i], nil)
	}
	p := New(targets, 1)
	defer p.Stop()
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			for range 200 {
				statuses[i].Set(healthcheck.StatusPassing)
				statuses[i].Set(healthcheck.StatusFailing)
			}
			if i%2 == 0 {
				statuses[i].Set(healthcheck.StatusPassing)
			}
		})
	}
	// readers never see a nil or duplicated member while the storm runs
	wg.Go(func() {
		for range 2000 {
			seen := make(map[*Target]bool, n)
			for _, tgt := range p.Targets() {
				if tgt == nil || seen[tgt] {
					t.Error("Targets() returned a nil or repeated target")
					return
				}
				seen[tgt] = true
			}
		}
	})
	wg.Wait()
	var want Targets
	for i := 0; i < n; i += 2 {
		want = append(want, targets[i])
	}
	if got := p.Targets(); !slices.Equal(got, want) {
		t.Fatalf("after the storm: %d live targets, want the %d passing ones in pool order", len(got), len(want))
	}
}

// a transition that lands while the pool is being built is not lost
func testPoolTransitionRacingConstruction(t *testing.T) {
	for range 500 {
		st := &healthcheck.Status{}
		tgt := NewTarget(http.NotFoundHandler(), st, nil)
		var wg sync.WaitGroup
		wg.Go(func() { st.Set(healthcheck.StatusPassing) })
		p := New(Targets{tgt}, 1)
		wg.Wait()
		if got := len(p.Targets()); got != 1 {
			p.Stop()
			t.Fatalf("a transition racing construction was lost: %d live targets", got)
		}
		p.Stop()
	}
}

// a target assembled without a constructor has no member yet and may lack a status
func testPoolTargetWithoutConstructor(t *testing.T) {
	st := &healthcheck.Status{}
	st.Set(healthcheck.StatusPassing)
	good := &Target{handler: http.NotFoundHandler(), hcStatus: st}
	bad := &Target{handler: http.NotFoundHandler()}
	p := New(Targets{good, bad, nil}, 1)
	defer p.Stop()
	if got := p.Targets(); len(got) != 1 || got[0] != good {
		t.Fatalf("expected only the target with a status, got %d", len(got))
	}
	if p.ConfiguredLen() != 3 {
		t.Errorf("configured length = %d", p.ConfiguredLen())
	}
}
