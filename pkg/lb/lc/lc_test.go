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
package lc_test

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/lbtest"
	"github.com/trickstercache/trickster/v2/pkg/lb/lc"
)

func TestConformance(t *testing.T) {
	lbtest.Run(t, lc.New, lbtest.Options{})
}

func TestWeights(t *testing.T) {
	lbtest.RunWeighted(t, lc.New, lbtest.WeightOptions{})
}

func BenchmarkSelect(b *testing.B) {
	lbtest.Bench(b, lc.New)
}

func newBalancer(t *testing.T, weights ...int) (*lb.Balancer, []*lb.Member) {
	t.Helper()
	members, _ := lbtest.Members(weights...)
	p, err := lb.NewPool(members, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return lb.NewBalancer(lc.New(), lb.BalancerOptions{Pool: p}), members
}

func TestIdentity(t *testing.T) {
	s := lc.New()
	if s.Name() != lc.Name || s.Needs() != lb.NeedInflight {
		t.Errorf("name %q needs %b", s.Name(), s.Needs())
	}
}

// the member with the fewest flows in flight for its weight takes the next one
func TestPrefersTheLeastLoaded(t *testing.T) {
	b, members := newBalancer(t, 1, 1, 1)
	var held []lb.Pick
	for range 9 {
		pk, _ := b.Pick(lb.Flow{})
		held = append(held, pk)
	}
	for _, m := range members {
		if got := m.Stats().Inflight(); got != 3 {
			t.Fatalf("%s holds %d of 9 held flows, want 3", m.Name(), got)
		}
	}
	// free one member entirely: it takes every new flow until it has caught up
	var freed *lb.Member
	for _, pk := range held {
		if freed == nil {
			freed = pk.Member()
		}
		if pk.Member() == freed {
			pk.Done(lb.OutcomeOK)
		}
	}
	for i := range 3 {
		pk, _ := b.Pick(lb.Flow{})
		if pk.Member() != freed {
			t.Fatalf("pick %d went to %s, not the idle member", i, pk.Member().Name())
		}
	}
}

// a weight is a capacity: a member three times the weight holds three times the flows
func TestWeightIsCapacity(t *testing.T) {
	b, members := newBalancer(t, 1, 3)
	for range 40 {
		b.Pick(lb.Flow{})
	}
	if light, heavy := members[0].Stats().Inflight(), members[1].Stats().Inflight(); light != 10 || heavy != 30 {
		t.Errorf("held flows = %d and %d, want 10 and 30", light, heavy)
	}
}
