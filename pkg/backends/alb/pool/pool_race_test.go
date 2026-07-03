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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

// TestPoolRace consolidates the prior pool_stop_race, pool_stop_refresh_race,
// pool_refresh_panic, and pool_targets_nil_hcstatus tests. Each subtest names
// its axis so -race or panic output still points at the failing scenario.
// Subtests are run sequentially (no t.Parallel) because they share Prometheus
// metric vectors and the pool internal state machine.
func TestPoolRace(t *testing.T) {
	t.Run("stop_double_close", testPoolStopConcurrentDoubleClose)
	t.Run("stop_then_set_healthy_no_refresh_overwrite", testPoolStopThenSetHealthy)
	t.Run("refresh_worker_survives_panic", testPoolRefreshWorkerSurvivesPanic)
	t.Run("targets_cached_path_nil_hcstatus", testPoolTargetsCachedPathNilHCStatus)
}

// concurrent Stop must not panic on a closed channel.
func testPoolStopConcurrentDoubleClose(t *testing.T) {
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
			t.Fatalf("iteration %d: Stop panicked on concurrent invocation (close of closed channel)", i)
		}
	}
}

// scheduleRefresh queued by New() must not fire after Stop, or it would
// overwrite a subsequent SetHealthy and break Stop-then-SetHealthy callers.
func testPoolStopThenSetHealthy(t *testing.T) {
	const iterations = 500
	for i := range iterations {
		s := &healthcheck.Status{}
		tgt := NewTarget(http.NotFoundHandler(), s, nil)
		p := New(Targets{tgt}, 1)
		p.Stop()
		h := []http.Handler{http.NotFoundHandler(), http.NotFoundHandler()}
		p.SetHealthy(h)
		if got := len(p.Targets()); got != 2 {
			t.Fatalf("iteration %d: Targets: expected 2 got %d "+
				"(RefreshHealthy ran after Stop returned)", i, got)
		}
		if got := len(p.(*pool).snapshot()); got != 2 {
			t.Fatalf("iteration %d: snapshot: expected 2 got %d", i, got)
		}
	}
}

// runWithRecover must absorb panics, increment the recover counter, and
// permit subsequent iterations to execute normally.
func testPoolRefreshWorkerSurvivesPanic(t *testing.T) {
	p := &pool{}

	before := counterValue(t, metrics.ALBPoolRefreshPanicRecovered, "checkHealth")

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic escaped runWithRecover: %v", r)
			}
		}()
		p.runWithRecover("checkHealth", func() {
			panic("simulated refresh panic")
		})
	}()

	after := counterValue(t, metrics.ALBPoolRefreshPanicRecovered, "checkHealth")
	if got := after - before; got != 1 {
		t.Fatalf("expected ALBPoolRefreshPanicRecovered{worker=checkHealth} +1, got +%v", got)
	}

	ran := false
	p.runWithRecover("checkHealth", func() {
		ran = true
	})
	if !ran {
		t.Fatal("iteration after recovered panic did not execute")
	}

	lbefore := counterValue(t, metrics.ALBPoolRefreshPanicRecovered, "listenStatusUpdates")
	p.runWithRecover("listenStatusUpdates", func() {
		panic("simulated status panic")
	})
	lafter := counterValue(t, metrics.ALBPoolRefreshPanicRecovered, "listenStatusUpdates")
	if got := lafter - lbefore; got != 1 {
		t.Fatalf("expected ALBPoolRefreshPanicRecovered{worker=listenStatusUpdates} +1, got +%v", got)
	}
}

// Targets() cached path must tolerate a target whose hcStatus is nil, matching
// the snapshot path. Defense-in-depth for future callers that inject targets
// bypassing RefreshHealthy / SetHealthy.
func testPoolTargetsCachedPathNilHCStatus(t *testing.T) {
	st := &healthcheck.Status{}
	st.Set(healthcheck.StatusPassing)
	good := NewTarget(http.NotFoundHandler(), st, nil)
	bad := &Target{handler: http.NotFoundHandler()}

	p := &pool{
		targets:      Targets{good},
		done:         make(chan struct{}),
		statusCh:     make(chan bool, 1),
		ch:           make(chan bool, 1),
		healthyFloor: 1,
	}

	cached := Targets{good, bad}
	p.liveTargets.Store(&cached)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Targets() panicked on nil hcStatus in cached path: %v", r)
		}
	}()
	_ = p.Targets()
}

func counterValue(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	c, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	if m.Counter == nil || m.Counter.Value == nil {
		return 0
	}
	return *m.Counter.Value
}
