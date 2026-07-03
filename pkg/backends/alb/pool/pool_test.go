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

func TestNewTarget(t *testing.T) {
	s := &healthcheck.Status{}
	tgt := NewTarget(http.NotFoundHandler(), s, nil)
	if tgt.hcStatus != s {
		t.Error("unexpected mismatch")
	}
}

func TestNewPool(t *testing.T) {
	s := &healthcheck.Status{}
	tgt := NewTarget(http.NotFoundHandler(), s, nil)
	if tgt.hcStatus != s {
		t.Error("unexpected mismatch")
	}

	p := New(Targets{tgt}, 1)
	if p == nil {
		t.Error("expected non-nil")
	}

	p2 := p.(*pool)
	if got := len(p2.snapshot()); got != 0 {
		t.Error("expected 0 healthy target", got)
	}

	p.Stop()

	ht := Targets{tgt}
	p2.healthyTargets.Store(&ht)
	lt := ht
	p2.liveTargets.Store(&lt)

	if got := len(p2.snapshot()); got != 1 {
		t.Error("expected 1 healthy target", got)
	}
}

func TestSetHealthyUpdatesTargets(t *testing.T) {
	s := &healthcheck.Status{}
	tgt := NewTarget(http.NotFoundHandler(), s, nil)
	p := New(Targets{tgt}, 1)
	p.Stop()

	h := []http.Handler{http.NotFoundHandler(), http.NotFoundHandler()}
	p.SetHealthy(h)

	if got := len(p.Targets()); got != 2 {
		t.Errorf("Targets: expected 2 got %d", got)
	}
	if got := len(p.(*pool).snapshot()); got != 2 {
		t.Errorf("snapshot: expected 2 got %d", got)
	}
}

func TestStopIdempotent(t *testing.T) {
	s := &healthcheck.Status{}
	tgt := NewTarget(http.NotFoundHandler(), s, nil)
	p := New(Targets{tgt}, 1)
	p.Stop()
	p.Stop() // must not panic
}
