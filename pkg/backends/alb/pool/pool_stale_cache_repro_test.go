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
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

// H1: simulate a dead refresh worker. Construct a pool but never start the
// refresh goroutines, mimicking what happens if listenStatusUpdates or
// checkHealth panicked and exited. Seed a populated cache and flip a target
// Failing. If Targets() still returns the Failing one, repro.
func TestRepro_H1_DeadRefreshWorker(t *testing.T) {
	const n = 3
	targets := make(Targets, n)
	statuses := make([]*healthcheck.Status, n)
	for i := range n {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		statuses[i] = st
		targets[i] = NewTarget(http.NotFoundHandler(), st, nil)
	}
	p := &pool{
		targets:      targets,
		done:         make(chan struct{}),
		statusCh:     make(chan bool, 1),
		ch:           make(chan bool, 1),
		healthyFloor: 1,
	}
	all := append(Targets(nil), targets...)
	p.healthyTargets.Store(&all)
	p.liveTargets.Store(&all)
	hh := make([]http.Handler, n)
	for i, tt := range targets {
		hh[i] = tt.handler
	}
	p.healthyHandlers.Store(&hh)

	statuses[0].Set(healthcheck.StatusFailing)

	got := p.Targets()
	for _, tt := range got {
		if tt == targets[0] {
			t.Fatalf("REPRO: Targets() returned a Failing target. got=%d targets", len(got))
		}
	}
	if len(got) != n-1 {
		t.Fatalf("expected %d live targets, got %d", n-1, len(got))
	}
}

// H1b: same as H1 but with a real pool. Mark target 0 Failing, then flip
// target 1 to saturate the refresh path. Assert target 0 stays excluded.
func TestRepro_H1b_RealPoolFlapStorm(t *testing.T) {
	const n = 3
	targets := make(Targets, n)
	statuses := make([]*healthcheck.Status, n)
	for i := range n {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		statuses[i] = st
		targets[i] = NewTarget(http.NotFoundHandler(), st, nil)
	}
	p := New(targets, 1)
	defer p.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.Targets()) == n {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(p.Targets()) != n {
		t.Fatalf("setup: expected %d healthy, got %d", n, len(p.Targets()))
	}

	statuses[0].Set(healthcheck.StatusFailing)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			statuses[1].Set(healthcheck.StatusFailing)
			statuses[1].Set(healthcheck.StatusPassing)
		}
	})
	wg.Wait()

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		ts := p.Targets()
		ok := !slices.Contains(ts, targets[0])
		if ok && len(ts) == n-1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("REPRO: Failing target still present 1s after flap storm")
}

// H2: saturate statusCh while refreshPending is being toggled, then flip
// target 0 Failing and assert it propagates.
func TestRepro_H2_ChannelDrop(t *testing.T) {
	const n = 10
	targets := make(Targets, n)
	statuses := make([]*healthcheck.Status, n)
	for i := range n {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		statuses[i] = st
		targets[i] = NewTarget(http.NotFoundHandler(), st, nil)
	}
	p := New(targets, 1)
	defer p.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.Targets()) == n {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(p.Targets()) != n {
		t.Fatalf("setup: %d", len(p.Targets()))
	}

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for range 50 {
				statuses[idx].Set(healthcheck.StatusFailing)
				statuses[idx].Set(healthcheck.StatusPassing)
			}
		}(i)
	}
	wg.Wait()

	statuses[0].Set(healthcheck.StatusFailing)
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		ts := p.Targets()
		hasFailing := slices.Contains(ts, targets[0])
		if !hasFailing && len(ts) == n-1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("REPRO: channel-drop left Failing target in pool")
}

// H4: 50 targets, persistent failing subset + churn. Assert no Failing
// target ever appears in Targets() output for 1 second.
func TestRepro_H4_ScaleRace(t *testing.T) {
	const n = 50
	targets := make(Targets, n)
	statuses := make([]*healthcheck.Status, n)
	for i := range n {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		statuses[i] = st
		targets[i] = NewTarget(http.NotFoundHandler(), st, nil)
	}
	p := New(targets, 1)
	defer p.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.Targets()) == n {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(p.Targets()) != n {
		t.Fatalf("setup: %d", len(p.Targets()))
	}

	stop := make(chan struct{})
	failingMask := make([]atomic.Bool, n)

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < n; i += 3 {
			statuses[i].Set(healthcheck.StatusFailing)
			failingMask[i].Store(true)
		}
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				for i := 1; i < n; i += 5 {
					if failingMask[i].Load() {
						continue
					}
					statuses[i].Set(healthcheck.StatusPassing)
				}
			}
		}
	})

	time.Sleep(50 * time.Millisecond)

	var iterations atomic.Int64
	asserter := time.NewTimer(1 * time.Second)
	defer asserter.Stop()
loop:
	for {
		select {
		case <-asserter.C:
			break loop
		default:
		}
		ts := p.Targets()
		iterations.Add(1)
		for _, tt := range ts {
			if tt.hcStatus.Get() == healthcheck.StatusFailing {
				close(stop)
				wg.Wait()
				t.Fatalf("REPRO: Targets() returned Failing target after %d iters", iterations.Load())
			}
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("H4: completed %d Targets() calls without seeing a Failing target", iterations.Load())
}
