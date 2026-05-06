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

func TestCheckHealth(t *testing.T) {
	tgt := &Target{
		hcStatus: &healthcheck.Status{},
	}

	tgt.hcStatus.Set(healthcheck.StatusPassing)

	p := &pool{ch: make(chan bool, 1), done: make(chan struct{}), targets: []*Target{tgt}, healthyFloor: -1}
	go func() {
		p.checkHealth()
	}()
	time.Sleep(150 * time.Millisecond)
	p.scheduleRefresh()
	time.Sleep(150 * time.Millisecond)
	p.Stop()
	time.Sleep(10 * time.Millisecond)

	h := p.healthyHandlers.Load()
	if h == nil {
		t.Error("expected non-nil healthy list")
		return
	}
	l := len(*h)
	if l != 1 {
		t.Errorf("expected %d got %d", 1, l)
	}
}

func TestBurstUpdatesEvictFailingTarget(t *testing.T) {
	st1 := &healthcheck.Status{}
	st2 := &healthcheck.Status{}
	t1 := NewTarget(http.NotFoundHandler(), st1, nil)
	t2 := NewTarget(http.NotFoundHandler(), st2, nil)
	p := New(Targets{t1, t2}, 1)
	defer p.Stop()

	st1.Set(healthcheck.StatusPassing)
	st2.Set(healthcheck.StatusPassing)

	waitForHealthyTargetsLen(t, p, 2, 2*time.Second)

	// Emit a burst of updates that may overrun subscriber channel buffers and
	// drop intermediate notifications. Final state is failing.
	for i := 0; i < 256; i++ {
		st1.Set(healthcheck.StatusPassing)
		st1.Set(healthcheck.StatusFailing)
	}
	st1.Set(healthcheck.StatusFailing)

	waitForHealthyTargetsLen(t, p, 1, 2*time.Second)

	got := p.HealthyTargets()
	if len(got) != 1 || got[0] != t2 {
		t.Fatalf("expected only target 2 to remain healthy, got: %#v", got)
	}
}

func waitForHealthyTargetsLen(t *testing.T, p Pool, expected int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(p.HealthyTargets()) == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d healthy targets; got %d", expected, len(p.HealthyTargets()))
}
