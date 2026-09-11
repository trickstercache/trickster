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
	"testing/synctest"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

func TestCheckHealth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tgt := &Target{
			hcStatus: &healthcheck.Status{},
		}

		tgt.hcStatus.Set(healthcheck.StatusPassing)

		p := &pool{ch: make(chan bool, 1), done: make(chan struct{}), targets: []*Target{tgt}, healthyFloor: -1}
		p.workers.Add(1)
		go p.checkHealth()
		defer p.Stop()
		p.scheduleRefresh()
		synctest.Wait()

		h := p.healthyHandlers.Load()
		if h == nil {
			t.Fatal("expected non-nil healthy list")
		}
		if got := len(*h); got != 1 {
			t.Errorf("expected %d got %d", 1, got)
		}
	})
}

func TestBurstUpdatesEvictFailingTarget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st1 := &healthcheck.Status{}
		st2 := &healthcheck.Status{}
		t1 := NewTarget(http.NotFoundHandler(), st1, nil)
		t2 := NewTarget(http.NotFoundHandler(), st2, nil)
		p := New(Targets{t1, t2}, 1)
		defer p.Stop()

		st1.Set(healthcheck.StatusPassing)
		st2.Set(healthcheck.StatusPassing)
		synctest.Wait()
		if got := len(p.Targets()); got != 2 {
			t.Fatalf("setup: expected 2 healthy targets, got %d", got)
		}

		// Emit a burst of updates that may overrun subscriber channel buffers and
		// drop intermediate notifications. Final state is failing.
		for range 256 {
			st1.Set(healthcheck.StatusPassing)
			st1.Set(healthcheck.StatusFailing)
		}
		st1.Set(healthcheck.StatusFailing)
		synctest.Wait()

		got := p.Targets()
		if len(got) != 1 || got[0] != t2 {
			t.Fatalf("expected only target 2 to remain healthy, got: %#v", got)
		}
	})
}
