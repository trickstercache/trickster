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

// Targets must drop a target whose status flipped below the floor after the
// cached snapshot was last refreshed. The internal snapshot keeps the stale
// view intact; Targets re-checks against the current atomic status to close
// the race window.
func TestLiveTargetsDropsStaleFailingTarget(t *testing.T) {
	st1 := &healthcheck.Status{}
	st2 := &healthcheck.Status{}
	t1 := NewTarget(http.NotFoundHandler(), st1, nil)
	t2 := NewTarget(http.NotFoundHandler(), st2, nil)

	p := New(Targets{t1, t2}, 1)
	st1.Set(healthcheck.StatusPassing)
	st2.Set(healthcheck.StatusPassing)
	waitForHealthyTargetsLen(t, p, 2, 2*time.Second)

	// Pin the snapshot stale by stopping the pool's refresh goroutines, then
	// flip t2 to Failing.
	p.Stop()
	st2.Set(healthcheck.StatusFailing)

	if got := len(p.(*pool).snapshot()); got != 2 {
		t.Fatalf("snapshot: expected 2 (stale), got %d", got)
	}

	live := p.Targets()
	if len(live) != 1 || live[0] != t1 {
		t.Fatalf("Targets: expected only t1, got %#v", live)
	}
}
