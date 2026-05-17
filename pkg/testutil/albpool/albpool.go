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

// Package albpool provides shared test helpers for constructing ALB pools,
// targets, and parent requests. Helpers do not register cleanup; callers own
// pool lifetime via defer p.Stop(). Helpers never call t.Fatal from inside a
// handler or spawned goroutine; that pattern interacts badly with goleak.
package albpool

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

// New constructs a pool with the given healthyFloor and one target per handler.
// Each target's status starts at the zero value (StatusUnchecked = 0); callers
// that want targets dispatch-ready should use NewHealthy or invoke Set on the
// returned statuses.
func New(healthyFloor int, hs []http.Handler) (pool.Pool,
	[]*pool.Target, []*healthcheck.Status,
) {
	var targets []*pool.Target
	var statuses []*healthcheck.Status
	if len(hs) > 0 {
		targets = make([]*pool.Target, 0, len(hs))
		statuses = make([]*healthcheck.Status, 0, len(hs))
		for _, h := range hs {
			hst := &healthcheck.Status{}
			statuses = append(statuses, hst)
			targets = append(targets, pool.NewTarget(h, hst, nil))
		}
	}
	p := pool.New(targets, healthyFloor)
	return p, targets, statuses
}

// NewHealthy builds a pool with healthyFloor=-1 and pre-sets every target's
// status to StatusPassing. It replaces the
//
//	p, _, st := albpool.New(-1, hs)
//	for _, s := range st { s.Set(0) }
//	time.Sleep(250 * time.Millisecond)
//
// boilerplate. Callers should still WaitHealthy if dispatch needs the live
// list to converge, or invoke p.SetHealthy to bypass refresh entirely.
func NewHealthy(handlers []http.Handler) (pool.Pool,
	[]*pool.Target, []*healthcheck.Status,
) {
	p, targets, statuses := New(-1, handlers)
	for _, s := range statuses {
		s.Set(healthcheck.StatusPassing)
	}
	return p, targets, statuses
}

// WaitHealthy polls p.Targets() until its length equals want or the 2s
// deadline elapses. On timeout it calls t.Fatalf. Replaces the
// time.Sleep(250 * time.Millisecond) idiom used to wait for health
// propagation.
func WaitHealthy(t testing.TB, p pool.Pool, want int) {
	t.Helper()
	const (
		deadline = 2 * time.Second
		interval = 2 * time.Millisecond
	)
	end := time.Now().Add(deadline)
	for {
		got := len(p.Targets())
		if got == want {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("waited %s for %d healthy, got %d", deadline, want, got)
			return
		}
		time.Sleep(interval)
	}
}

// StatusHandler returns an http.Handler that writes the given status code and
// body. Replaces the inline statusHandler closures in fr/, nlm/, fanout/.
func StatusHandler(code int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	})
}

// NamedHandler returns an http.Handler that writes name as the response body
// with a 200 status. Useful when tests need to assert which target served a
// request.
func NamedHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(name))
	})
}

// NewParentGET returns a GET request against the stock parent URL used by ALB
// tests. Fatal if request construction fails.
func NewParentGET(t testing.TB) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "https://trickstercache.org/", nil)
	if err != nil {
		t.Fatalf("albpool.NewParentGET: %v", err)
	}
	return r
}

// NewParentPOST returns a POST request against the stock parent URL with the
// given body. Fatal if request construction fails.
func NewParentPOST(t testing.TB, body io.Reader) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, "https://trickstercache.org/", body)
	if err != nil {
		t.Fatalf("albpool.NewParentPOST: %v", err)
	}
	return r
}

// Target builds a single *pool.Target backed by a fresh status. Replaces the
// mkTarget / mkFlapTarget helpers re-implemented across fanout tests.
func Target(h http.Handler) (*pool.Target, *healthcheck.Status) {
	hst := &healthcheck.Status{}
	return pool.NewTarget(h, hst, nil), hst
}

// HealthyTarget builds a single *pool.Target with its status pre-set to
// StatusPassing.
func HealthyTarget(h http.Handler) (*pool.Target, *healthcheck.Status) {
	tgt, st := Target(h)
	st.Set(healthcheck.StatusPassing)
	return tgt, st
}

// PanicHandler returns an http.Handler that panics with a canonical
// simulated-upstream string.
func PanicHandler() http.Handler {
	return http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("simulated upstream nil deref")
	})
}

// SizedBodyHandler returns an http.Handler that emits a body of size bytes
// (filled with 'a') under an explicit Content-Length: size header with the
// given status code.
func SizedBodyHandler(code, size int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := make([]byte, size)
		for i := range body {
			body[i] = 'a'
		}
		w.Header().Set("Content-Length", strconv.Itoa(size))
		w.WriteHeader(code)
		_, _ = w.Write(body)
	})
}

// ServeAndWait runs h.ServeHTTP(w, r) in a goroutine, asserts it returns
// within 5s, and re-raises any unrecovered panic via t.Fatalf so callers
// cannot keep asserting after a dead handler.
func ServeAndWait(t testing.TB, h http.Handler, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	done := make(chan struct{})
	var rec any
	go func() {
		defer func() {
			rec = recover()
			close(done)
		}()
		h.ServeHTTP(w, r)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return within 5s")
	}
	if rec != nil {
		t.Fatalf("unrecovered panic in ServeHTTP goroutine: %v", rec)
	}
}

// RunPostBodyFanoutRace builds a `slots`-upstream pool whose stubs verify
// the parent body arrives intact, then drives `callers` parallel POSTs
// against the handler returned by mkHandler(p). decorateResp is invoked
// in each upstream stub before WriteHeader (eg to set Last-Modified).
// Run under -race.
func RunPostBodyFanoutRace(
	t testing.TB,
	mkHandler func(p pool.Pool) http.Handler,
	body string,
	slots, callers int,
	decorateResp func(w http.ResponseWriter),
) {
	t.Helper()
	mkStub := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if string(b) != body {
				t.Errorf("%s: truncated body, got %d bytes want %d", name, len(b), len(body))
			}
			if decorateResp != nil {
				decorateResp(w)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(name))
		})
	}
	hs := make([]http.Handler, slots)
	for i := range hs {
		hs[i] = mkStub(fmt.Sprintf("slot-%d", i))
	}
	p, _, _ := NewHealthy(hs)
	defer p.Stop()
	WaitHealthy(t, p, slots)
	h := mkHandler(p)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			r, err := http.NewRequest(http.MethodPost,
				"https://trickstercache.org/api/v1/query_range", strings.NewReader(body))
			if err != nil {
				t.Errorf("NewRequest: %v", err)
				return
			}
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Errorf("status %d", w.Code)
			}
		})
	}
	wg.Wait()
}

// RequireFanoutAttemptDelta runs fn and asserts that the
// metrics.ALBFanoutAttempts counter for (mech, variant) advanced by want.
func RequireFanoutAttemptDelta(t testing.TB, mech, variant string, want float64, fn func()) {
	t.Helper()
	before := testutil.ToFloat64(metrics.ALBFanoutAttempts.WithLabelValues(mech, variant))
	fn()
	after := testutil.ToFloat64(metrics.ALBFanoutAttempts.WithLabelValues(mech, variant))
	if got := after - before; got != want {
		t.Errorf("ALBFanoutAttempts{mech=%q,variant=%q} delta = %v, want %v",
			mech, variant, got, want)
	}
}

// RequireFanoutFailureDelta runs fn and asserts that the
// metrics.ALBFanoutFailures counter for (mech, variant, reason) advanced by
// want.
func RequireFanoutFailureDelta(t testing.TB, mech, variant, reason string, want float64, fn func()) {
	t.Helper()
	RequireCounterDelta(t, metrics.ALBFanoutFailures, []string{mech, variant, reason}, want, fn)
}

// RequireCounterDelta runs fn and asserts the named CounterVec advanced by
// want for the given labels. Generalises the *FanoutDelta helpers for
// callers that want to assert on any Prometheus counter.
func RequireCounterDelta(t testing.TB, vec *prometheus.CounterVec, labels []string, want float64, fn func()) {
	t.Helper()
	before := testutil.ToFloat64(vec.WithLabelValues(labels...))
	fn()
	after := testutil.ToFloat64(vec.WithLabelValues(labels...))
	if got := after - before; got != want {
		t.Errorf("counter delta = %v, want %v (labels=%v)", got, want, labels)
	}
}

// HealthFlipResult reports the iteration counts and the cumulative count of
// successful fanout slots observed during RunHealthFlipRace.
type HealthFlipResult struct {
	FlipperIters   int64
	FanoutIters    int64
	SucceededSlots int64
}

// RunHealthFlipRace toggles every target's health between Failing and Passing
// in one goroutine while invoking fanoutFn in another, until fanoutIters
// reaches earlyExitFanouts or the deadline elapses. fanoutFn must return the
// number of slots in the fanout that succeeded. Intended for -race coverage
// of the dispatch-vs-health-flap interleave; the caller is responsible for
// asserting invariants on the returned counts.
func RunHealthFlipRace(
	t testing.TB,
	targets pool.Targets,
	fanoutFn func() (slotsSucceeded int),
	deadline time.Duration,
	earlyExitFanouts int64,
) HealthFlipResult {
	t.Helper()
	stop := make(chan struct{})
	var flipperIters, fanoutIters, succeededSlots atomic.Int64
	var wg sync.WaitGroup

	wg.Go(func() {
		toggle := healthcheck.StatusFailing
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, tgt := range targets {
				if hs := tgt.HealthStatus(); hs != nil {
					hs.Set(toggle)
				}
			}
			if toggle == healthcheck.StatusFailing {
				toggle = healthcheck.StatusPassing
			} else {
				toggle = healthcheck.StatusFailing
			}
			flipperIters.Add(1)
		}
	})

	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			succeededSlots.Add(int64(fanoutFn()))
			fanoutIters.Add(1)
		}
	})

	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			close(stop)
			wg.Wait()
			return HealthFlipResult{
				FlipperIters:   flipperIters.Load(),
				FanoutIters:    fanoutIters.Load(),
				SucceededSlots: succeededSlots.Load(),
			}
		default:
			if earlyExitFanouts > 0 && fanoutIters.Load() >= earlyExitFanouts {
				close(stop)
				wg.Wait()
				return HealthFlipResult{
					FlipperIters:   flipperIters.Load(),
					FanoutIters:    fanoutIters.Load(),
					SucceededSlots: succeededSlots.Load(),
				}
			}
			time.Sleep(time.Millisecond)
		}
	}
}
