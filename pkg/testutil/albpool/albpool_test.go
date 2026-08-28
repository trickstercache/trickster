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

package albpool

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

func TestNew_NilHandlers(t *testing.T) {
	t.Parallel()
	p, targets, statuses := New(-1, nil)
	defer p.Stop()
	if targets != nil || statuses != nil {
		t.Fatalf("expected nil slices for nil handlers, got targets=%v statuses=%v",
			targets, statuses)
	}
}

func TestNew_ProducesParallelSlices(t *testing.T) {
	t.Parallel()
	hs := []http.Handler{StatusHandler(200, "a"), StatusHandler(200, "b")}
	p, targets, statuses := New(-1, hs)
	defer p.Stop()
	if len(targets) != 2 || len(statuses) != 2 {
		t.Fatalf("expected 2 targets and 2 statuses, got %d/%d",
			len(targets), len(statuses))
	}
	for i, st := range statuses {
		if st == nil {
			t.Fatalf("statuses[%d] nil", i)
		}
		if st.Get() != 0 {
			t.Errorf("statuses[%d] expected zero status, got %d", i, st.Get())
		}
	}
}

func TestNewHealthy_PresetsPassing(t *testing.T) {
	t.Parallel()
	hs := []http.Handler{StatusHandler(200, "a"), StatusHandler(200, "b")}
	p, _, statuses := NewHealthy(hs)
	defer p.Stop()
	for i, st := range statuses {
		if got := st.Get(); got != healthcheck.StatusPassing {
			t.Errorf("statuses[%d] = %d; want StatusPassing (%d)",
				i, got, healthcheck.StatusPassing)
		}
	}
}

func TestWaitHealthy_ConvergesAfterSet(t *testing.T) {
	t.Parallel()
	hs := []http.Handler{StatusHandler(200, "a"), StatusHandler(200, "b")}
	p, _, statuses := NewHealthy(hs)
	defer p.Stop()
	WaitHealthy(t, p, len(hs))
	if got := len(p.Targets()); got != len(hs) {
		t.Errorf("after WaitHealthy, len(Targets)=%d want %d", got, len(hs))
	}
	for _, s := range statuses {
		s.Set(healthcheck.StatusInitializing)
	}
	WaitHealthy(t, p, 0)
}

func TestStatusHandler(t *testing.T) {
	t.Parallel()
	h := StatusHandler(http.StatusTeapot, "brew")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusTeapot {
		t.Errorf("code = %d; want 418", w.Code)
	}
	if w.Body.String() != "brew" {
		t.Errorf("body = %q; want %q", w.Body.String(), "brew")
	}
}

func TestNamedHandler(t *testing.T) {
	t.Parallel()
	h := NamedHandler("backend-7")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Body.String() != "backend-7" {
		t.Errorf("body = %q; want %q", w.Body.String(), "backend-7")
	}
}

func TestNewParentGET(t *testing.T) {
	t.Parallel()
	r := NewParentGET(t)
	if r.Method != http.MethodGet {
		t.Errorf("method = %s; want GET", r.Method)
	}
	if r.URL.Host != "trickstercache.org" {
		t.Errorf("host = %s; want trickstercache.org", r.URL.Host)
	}
}

func TestNewParentPOST(t *testing.T) {
	t.Parallel()
	r := NewParentPOST(t, strings.NewReader("payload"))
	if r.Method != http.MethodPost {
		t.Errorf("method = %s; want POST", r.Method)
	}
	if r.Body == nil {
		t.Fatal("body nil")
	}
}

func TestTarget(t *testing.T) {
	t.Parallel()
	h := NamedHandler("t1")
	tgt, st := Target(h)
	if tgt == nil || st == nil {
		t.Fatalf("Target returned nil: tgt=%v st=%v", tgt, st)
	}
	if tgt.HealthStatus() != st {
		t.Error("Target's status pointer does not match returned status")
	}
}

func TestRequireCounterDelta_FailsOnZeroDelta(t *testing.T) {
	t.Parallel()
	stub := &testing.T{}
	RequireCounterDelta(stub, metrics.ALBFanoutAttempts, []string{"requiredelta-test", ""}, 1, func() {
		// no metric increment; expect helper to fail
	})
	if !stub.Failed() {
		t.Error("RequireCounterDelta did not fail when delta was zero but want=1")
	}
}

func TestRequireCounterDelta_PassesOnExactDelta(t *testing.T) {
	t.Parallel()
	stub := &testing.T{}
	RequireCounterDelta(stub, metrics.ALBFanoutAttempts, []string{"requiredelta-pass", ""}, 2, func() {
		metrics.ALBFanoutAttempts.WithLabelValues("requiredelta-pass", "").Add(2)
	})
	if stub.Failed() {
		t.Error("RequireCounterDelta failed when delta matched want")
	}
}

func TestRunHealthFlipRace_ProducesProgress(t *testing.T) {
	t.Parallel()
	hs := []http.Handler{StatusHandler(200, "a"), StatusHandler(200, "b")}
	p, targets, _ := NewHealthy(hs)
	defer p.Stop()
	res := RunHealthFlipRace(t, targets, func() int {
		// trivial work: count live targets each call
		return len(p.Targets())
	}, 200*time.Millisecond, 0)
	if res.FlipperIters == 0 {
		t.Error("flipper made no progress")
	}
	if res.FanoutIters == 0 {
		t.Error("fanout made no progress")
	}
}
