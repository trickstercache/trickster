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

package rr

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// nextTarget must skip targets whose hcStatus dropped below the pool's
// healthyFloor since the snapshot was taken. Without the dispatch-time check,
// rr would route to a member the pool already considers unavailable.
func TestNextTargetSkipsStaleFailingTarget(t *testing.T) {
	var hits1, hits2 atomic.Int64
	h1 := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { hits1.Add(1) })
	h2 := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { hits2.Add(1) })

	p, _, sts := albpool.New(1, []http.Handler{h1, h2})

	sts[0].Set(healthcheck.StatusPassing)
	sts[1].Set(healthcheck.StatusPassing)

	// Allow the initial pool refresh to land both targets in the healthy snapshot.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(p.HealthyTargets()) != 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if l := len(p.HealthyTargets()); l != 2 {
		t.Fatalf("expected snapshot of 2 healthy, got %d", l)
	}

	// Stop the pool so no auto-refresh can repair a stale snapshot. This
	// pins the test to the exact race window the dispatch-time re-check
	// is meant to close.
	p.Stop()

	// Flip target 1 to Failing. The snapshot is now permanently stale until
	// the dispatch-time re-check kicks in.
	sts[1].Set(healthcheck.StatusFailing)

	rr := &handler{}
	rr.SetPool(p)
	const reqs = 50
	for range reqs {
		w := httptest.NewRecorder()
		rr.ServeHTTP(w, nil)
	}

	if got := hits2.Load(); got != 0 {
		t.Errorf("failing target received %d requests; expected 0", got)
	}
	if got := hits1.Load(); got != int64(reqs) {
		t.Errorf("healthy target received %d requests; expected %d", got, reqs)
	}
}
