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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

func TestPoolHealthyFloorAccessor(t *testing.T) {
	st := &healthcheck.Status{}
	tgt := NewTarget(http.NotFoundHandler(), st, nil)
	p := New(Targets{tgt}, 1)
	defer p.Stop()

	if got := p.HealthyFloor(); got != 1 {
		t.Errorf("HealthyFloor: expected 1 got %d", got)
	}
}

// TestStaleSnapshotExcludesFailingTarget reproduces the routing bug where a
// mechanism captures pool.HealthyTargets() then a target flips to Failing
// while requests are in flight. The snapshot stays stale; without a
// per-invocation status re-check, the failing target keeps receiving traffic.
func TestStaleSnapshotExcludesFailingTarget(t *testing.T) {
	st1 := &healthcheck.Status{}
	st2 := &healthcheck.Status{}
	t1 := NewTarget(http.NotFoundHandler(), st1, nil)
	t2 := NewTarget(http.NotFoundHandler(), st2, nil)

	p := New(Targets{t1, t2}, 1)
	defer p.Stop()

	st1.Set(healthcheck.StatusPassing)
	st2.Set(healthcheck.StatusPassing)
	waitForHealthyTargetsLen(t, p, 2, 2*time.Second)

	// Mechanism takes a snapshot of healthy targets.
	snapshot := p.HealthyTargets()
	if len(snapshot) != 2 {
		t.Fatalf("snapshot: expected 2 targets, got %d", len(snapshot))
	}

	// A request is now in flight; before it dispatches, t1 flips to Failing.
	st1.Set(healthcheck.StatusFailing)

	// The snapshot is intentionally stale — it still contains t1.
	if len(snapshot) != 2 {
		t.Fatalf("snapshot must remain length 2 after status flip; got %d", len(snapshot))
	}

	// Defense-in-depth check: each goroutine must compare the target's
	// current status against the pool's healthyFloor before invoking.
	floor := p.HealthyFloor()
	var keep []*Target
	for _, tgt := range snapshot {
		if int(tgt.HealthStatus().Get()) >= floor {
			keep = append(keep, tgt)
		}
	}
	if len(keep) != 1 {
		t.Fatalf("expected 1 target after status re-check, got %d", len(keep))
	}
	if keep[0] != t2 {
		t.Fatalf("expected t2 to be the surviving target, got different reference")
	}
}
