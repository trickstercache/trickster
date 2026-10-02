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

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

// Targets drops a member the moment its status falls below the floor, with no wait for any
// background worker; a stopped pool no longer follows its members.
func TestTargetsDropsFailingTargetImmediately(t *testing.T) {
	st1 := &healthcheck.Status{}
	st2 := &healthcheck.Status{}
	t1 := NewTarget(http.NotFoundHandler(), st1, nil)
	t2 := NewTarget(http.NotFoundHandler(), st2, nil)

	p := New(Targets{t1, t2}, 1)
	defer p.Stop()
	st1.Set(healthcheck.StatusPassing)
	st2.Set(healthcheck.StatusPassing)
	if got := len(p.Targets()); got != 2 {
		t.Fatalf("setup: expected 2 healthy targets, got %d", got)
	}

	st2.Set(healthcheck.StatusFailing)
	if live := p.Targets(); len(live) != 1 || live[0] != t1 {
		t.Fatalf("Targets: expected only t1, got %#v", live)
	}

	p.Stop()
	st2.Set(healthcheck.StatusPassing)
	st1.Set(healthcheck.StatusFailing)
	if live := p.Targets(); len(live) != 1 || live[0] != t1 {
		t.Fatalf("a stopped pool republished: %#v", live)
	}
	p.RefreshHealthy()
	if live := p.Targets(); len(live) != 1 || live[0] != t1 {
		t.Fatalf("a stopped pool refreshed: %#v", live)
	}
}
