/*
 * Copyright 2026 The Trickster Authors
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
package lb_test

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/lbtest"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
)

type events struct {
	mu  sync.Mutex
	got []lb.Event
}

func (e *events) Observe(ev lb.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, ev)
}

func (e *events) ejected() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.got {
		if ev.Kind == lb.EventEjected {
			out = append(out, ev.Member)
		}
	}
	return out
}

func ejecting(t *testing.T, o lb.EjectionOptions, weights ...int) (*lb.Balancer, []*lb.Member, []*lbtest.Health, *events) {
	t.Helper()
	members, healths := lbtest.Members(weights...)
	p, err := lb.NewPool(members, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	ev := &events{}
	return lb.NewBalancer(rr.NewAt(0), lb.BalancerOptions{Pool: p, Ejection: o, Observer: ev}), members, healths, ev
}

// failTo drives picks until n of them have failed to connect to m
func failTo(t *testing.T, b *lb.Balancer, m *lb.Member, n int) {
	t.Helper()
	for range 1000 {
		if n == 0 {
			return
		}
		pk, ok := b.Pick(lb.Flow{})
		if !ok {
			t.Fatal("no pick")
		}
		if pk.Member() == m {
			pk.Done(lb.OutcomeConnectFailed)
			n--
			continue
		}
		pk.Done(lb.OutcomeOK)
	}
	t.Fatalf("%s was not picked often enough to fail %d more times", m.Name(), n)
}

func live(b *lb.Balancer) []string {
	var out []string
	for _, m := range b.Pool().Snapshot().Members {
		out = append(out, m.Name())
	}
	return out
}

// consecutive connect failures eject a member, even under a strategy that otherwise tracks
// nothing; a success in between starts the count again, and other outcomes never count
func TestEjectionNeedsConsecutiveConnectFailures(t *testing.T) {
	b, m, _, ev := ejecting(t, lb.EjectionOptions{Failures: 3, Duration: time.Hour}, 1, 1, 1)
	failTo(t, b, m[1], 2)
	for range 3 {
		pk, _ := b.Pick(lb.Flow{})
		pk.Done(lb.OutcomeOK)
	}
	failTo(t, b, m[1], 2)
	if len(live(b)) != 3 {
		t.Fatalf("ejected after failures that were not consecutive: %v", live(b))
	}
	for range 30 {
		pk, _ := b.Pick(lb.Flow{})
		if pk.Member() == m[0] {
			pk.Done(lb.OutcomeFailed)
		} else {
			pk.Done(lb.OutcomeCanceled)
		}
	}
	if len(live(b)) != 3 {
		t.Fatalf("an outcome other than a connect failure ejected a member: %v", live(b))
	}
	failTo(t, b, m[1], 1)
	if got := live(b); !slices.Equal(got, []string{m[0].Name(), m[2].Name()}) {
		t.Fatalf("after three consecutive connect failures: %v", got)
	}
	if !m[1].Stats().Ejected(time.Now()) || m[0].Stats().Ejected(time.Now()) {
		t.Error("the stats do not say who is ejected")
	}
	if got := ev.ejected(); !slices.Equal(got, []string{m[1].Name()}) {
		t.Errorf("ejection events = %v", got)
	}
	// it takes no more flows
	for range 20 {
		pk, _ := b.Pick(lb.Flow{})
		if pk.Member() == m[1] {
			t.Fatal("an ejected member was picked")
		}
		pk.Done(lb.OutcomeOK)
	}
}

// an ejection with no duration or share configured lasts 30s and spares half the pool
func TestEjectionDefaults(t *testing.T) {
	b, m, _, _ := ejecting(t, lb.EjectionOptions{Failures: 1}, 1, 1, 1, 1)
	for _, member := range m {
		failTo(t, b, member, 1)
	}
	if got := len(live(b)); got != 2 {
		t.Errorf("%d of 4 members live", got)
	}
	var out *lb.Member
	for _, member := range m {
		if member.Stats().Ejected(time.Now()) {
			out = member
		}
	}
	if out == nil || !out.Stats().Ejected(time.Now().Add(lb.DefaultEjectionDuration-time.Second)) ||
		out.Stats().Ejected(time.Now().Add(lb.DefaultEjectionDuration+time.Second)) {
		t.Error("the default ejection does not last the default duration")
	}
}

func TestEjectionIsOffByDefault(t *testing.T) {
	b, m, _, ev := ejecting(t, lb.EjectionOptions{}, 1, 1)
	failTo(t, b, m[0], 50)
	if len(live(b)) != 2 || len(ev.ejected()) != 0 {
		t.Errorf("a balancer with no ejection configured ejected: %v", live(b))
	}
	if m[0].Stats().Failures() != 0 {
		t.Error("a strategy with no needs and no ejection paid for failure counting")
	}
}

// the pool is never emptied: not below one live member, and not past the configured share
func TestEjectionCap(t *testing.T) {
	b, m, healths, _ := ejecting(t, lb.EjectionOptions{Failures: 1, Duration: time.Hour}, 1, 1, 1, 1)
	for _, member := range m {
		failTo(t, b, member, 1)
	}
	if got := len(live(b)); got != 2 {
		t.Errorf("the default cap of half left %d of 4 members live", got)
	}

	all, m2, _, _ := ejecting(t, lb.EjectionOptions{Failures: 1, Duration: time.Hour, MaxPercent: 100}, 1, 1, 1)
	for range 3 {
		for _, member := range m2 {
			if slices.Contains(live(all), member.Name()) && len(live(all)) > 1 {
				failTo(t, all, member, 1)
			}
		}
	}
	if got := len(live(all)); got != 1 {
		t.Errorf("with no percentage cap, %d of 3 members stayed live; the last one always must", got)
	}
	pk, ok := all.Pick(lb.Flow{})
	if !ok {
		t.Fatal("the pool was emptied")
	}
	pk.Done(lb.OutcomeConnectFailed)
	if len(live(all)) != 1 {
		t.Error("the last live member was ejected")
	}

	// a member the health check has already taken out does not count as live
	two, m3, h3, _ := ejecting(t, lb.EjectionOptions{Failures: 1, Duration: time.Hour, MaxPercent: 100}, 1, 1)
	h3[0].Set(-1)
	failTo(t, two, m3[1], 1)
	if got := live(two); len(got) != 1 {
		t.Errorf("ejected the only member its health check had left: %v", got)
	}
	_ = healths
}

// an ejected member returns when its time is up, through whichever pool is current by then,
// and only if its health check still has it up
func TestEjectionEnds(t *testing.T) {
	b, m, healths, _ := ejecting(t, lb.EjectionOptions{Failures: 1, Duration: 60 * time.Millisecond}, 1, 1, 1)
	failTo(t, b, m[0], 1)
	failTo(t, b, m[2], 0)
	if len(live(b)) != 2 {
		t.Fatalf("live = %v", live(b))
	}
	// membership is swapped while the member is out: the new pool starts without it
	swapped, err := lb.NewPool(m, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer swapped.Stop()
	b.SetPool(swapped)
	if len(live(b)) != 2 {
		t.Fatalf("a new pool forgot the ejection: %v", live(b))
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(live(b)) != 3 {
		if time.Now().After(deadline) {
			t.Fatal("the ejected member never returned to the current pool")
		}
		time.Sleep(5 * time.Millisecond)
	}

	failTo(t, b, m[1], 1)
	healths[1].Set(-1)
	time.Sleep(120 * time.Millisecond)
	if slices.Contains(live(b), m[1].Name()) {
		t.Error("a member returned from ejection although its health check has it down")
	}
	// with no pool left by the time an ejection ends, there is nothing to refresh
	failTo(t, b, m[2], 1)
	b.SetPool(nil)
	time.Sleep(120 * time.Millisecond)
}

// a flow that fails after its member has left the pool ejects nobody
func TestEjectionIgnoresADepartedMember(t *testing.T) {
	b, m, _, ev := ejecting(t, lb.EjectionOptions{Failures: 1, Duration: time.Hour}, 1, 1, 1)
	var held lb.Pick
	for held.Member() != m[0] {
		held.Done(lb.OutcomeOK)
		held, _ = b.Pick(lb.Flow{})
	}
	without, err := lb.NewPool(m[1:], 1)
	if err != nil {
		t.Fatal(err)
	}
	defer without.Stop()
	b.SetPool(without)
	held.Done(lb.OutcomeConnectFailed)
	if len(ev.ejected()) != 0 || len(live(b)) != 2 {
		t.Errorf("ejected %v from a pool it had left; live = %v", ev.ejected(), live(b))
	}
	// nor does one that fails once the pool is stopped, or gone
	held, _ = b.Pick(lb.Flow{})
	without.Stop()
	held.Done(lb.OutcomeConnectFailed)
	held, _ = b.Pick(lb.Flow{})
	b.SetPool(nil)
	held.Done(lb.OutcomeConnectFailed)
	if len(ev.ejected()) != 0 {
		t.Errorf("ejected %v from a stopped or absent pool", ev.ejected())
	}
}
