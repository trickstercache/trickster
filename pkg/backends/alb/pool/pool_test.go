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
	p := New(Targets{tgt}, 1)
	if p == nil {
		t.Fatal("expected non-nil")
	}
	defer p.Stop()
	if got := len(p.Targets()); got != 0 {
		t.Error("expected 0 healthy targets", got)
	}
	if p.ConfiguredLen() != 1 || len(p.ConfiguredTargets()) != 1 {
		t.Error("expected 1 configured target")
	}
	// a transition across the floor is dispatchable by the time Set returns
	s.Set(healthcheck.StatusPassing)
	if got := p.Targets(); len(got) != 1 || got[0] != tgt {
		t.Error("expected the passing target", got)
	}
}

func TestTargetMemberAndAddr(t *testing.T) {
	s := &healthcheck.Status{}
	tgt := NewWeightedTarget(http.NotFoundHandler(), s, nil, 3)
	m := tgt.Member()
	if m == nil || m.Value != tgt || m.Weight() != 3 || m.Health() != s {
		t.Fatalf("member does not describe its target: %+v", m)
	}
	if tgt.Addr() != "" {
		t.Errorf("a target without a backend has no address, got %q", tgt.Addr())
	}
	// a replacement target keeps its predecessor's runtime stats; one built fresh does not
	next := NewWeightedTarget(http.NotFoundHandler(), s, nil, 5).WithStatsOf(tgt)
	if next.Member().Stats() != m.Stats() || next.Member().Weight() != 5 || next.Member().Value != next {
		t.Error("stats were not carried to the replacement target")
	}
	kept := NewTarget(http.NotFoundHandler(), s, nil).WithStats(m.Stats())
	if kept.Member().Stats() != m.Stats() || kept.Member().Value != kept {
		t.Error("stats kept from an earlier target were not adopted")
	}
	if NewTarget(http.NotFoundHandler(), s, nil).WithStats(nil).Member().Stats() == nil {
		t.Error("a target with nothing to adopt lost its own stats")
	}
	if NewTarget(http.NotFoundHandler(), s, nil).WithStatsOf(nil).Member().Stats() == m.Stats() {
		t.Error("unrelated targets share stats")
	}
	// a target without a health status is never dispatchable, and must not panic the pool
	p := New(Targets{NewTarget(http.NotFoundHandler(), nil, nil), tgt}, -1)
	defer p.Stop()
	if got := p.Targets(); len(got) != 1 || got[0] != tgt {
		t.Errorf("expected only the target with a status, got %d", len(got))
	}
}

func TestStopIdempotent(t *testing.T) {
	s := &healthcheck.Status{}
	tgt := NewTarget(http.NotFoundHandler(), s, nil)
	p := New(Targets{tgt}, 1)
	p.Stop()
	p.Stop() // must not panic
}

// the dispatchable set is rebuilt only when a member crosses the floor, and reading it in
// steady state allocates nothing
func TestTargetsRebuiltOnlyOnFloorCrossing(t *testing.T) {
	st1, st2 := &healthcheck.Status{}, &healthcheck.Status{}
	st1.Set(healthcheck.StatusPassing)
	st2.Set(healthcheck.StatusPassing)
	p := New(Targets{NewTarget(http.NotFoundHandler(), st1, nil),
		NewTarget(http.NotFoundHandler(), st2, nil)}, 0)
	defer p.Stop()
	before := p.Targets()
	// Passing, Unchecked: all at or above a floor of 0
	st1.Set(healthcheck.StatusUnchecked)
	st1.Set(healthcheck.StatusPassing)
	if after := p.Targets(); len(after) != 2 || &after[0] != &before[0] {
		t.Error("a transition that did not cross the floor rebuilt the dispatchable set")
	}
	if allocs := testing.AllocsPerRun(1000, func() { _ = p.Targets() }); allocs != 0 {
		t.Errorf("Targets allocates %v in steady state", allocs)
	}
	st1.Set(healthcheck.StatusFailing)
	if after := p.Targets(); len(after) != 1 {
		t.Errorf("expected 1 live target, got %d", len(after))
	}
	// the view is rebuilt once per snapshot, then served as is
	if a, b := p.Targets(), p.Targets(); &a[0] != &b[0] {
		t.Error("Targets rebuilt its view without a new snapshot")
	}
}

// a member listed twice is kept once: the core refuses duplicate names
func TestNewPoolKeepsFirstOfARepeatedName(t *testing.T) {
	st := &healthcheck.Status{}
	st.Set(healthcheck.StatusPassing)
	first := &Target{handler: http.NotFoundHandler(), hcStatus: st, name: "a", weight: 1}
	again := &Target{handler: http.NotFoundHandler(), hcStatus: st, name: "a", weight: 1}
	p := New(Targets{first, again}, 0)
	defer p.Stop()
	if got := p.Targets(); len(got) != 1 || got[0] != first {
		t.Errorf("expected only the first of a repeated name, got %d", len(got))
	}
}
