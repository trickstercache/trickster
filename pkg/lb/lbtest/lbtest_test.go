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
package lbtest_test

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/lbtest"
)

// byKey is a minimal strategy that declares every need, so the suite's accounting checks run
type byKey struct{}

type byKeyPrepared struct{ members []*lb.Member }

func newByKey() lb.Selector { return &byKey{} }

func (*byKey) Name() string { return "by_key" }

func (*byKey) Needs() lb.Needs { return lb.NeedKey | lb.NeedInflight | lb.NeedLatency }

func (*byKey) Prepare(s *lb.Snapshot) lb.Prepared { return &byKeyPrepared{members: s.Members} }

func (p *byKeyPrepared) Select(f lb.Flow) *lb.Member {
	return p.members[f.Key%uint64(len(p.members))]
}

func TestSuiteAcceptsAConformingSelector(t *testing.T) {
	lbtest.Run(t, newByKey, lbtest.Options{})
}

// byWeight gives each member a span of the key space as wide as its weight
type byWeight struct{}

type byWeightPrepared struct {
	members []*lb.Member
	total   uint64
}

func (byWeight) Name() string { return "by_weight" }

func (byWeight) Needs() lb.Needs { return lb.NeedKey }

func (byWeight) Prepare(s *lb.Snapshot) lb.Prepared {
	p := &byWeightPrepared{members: s.Members}
	for _, m := range s.Members {
		p.total += uint64(m.Weight())
	}
	return p
}

func (p *byWeightPrepared) Select(f lb.Flow) *lb.Member {
	k := f.Key % p.total
	for _, m := range p.members {
		if w := uint64(m.Weight()); k < w {
			return m
		} else {
			k -= w
		}
	}
	return p.members[0]
}

func TestWeightedSuiteAcceptsAStrategyThatHonorsWeights(t *testing.T) {
	lbtest.RunWeighted(t, func() lb.Selector { return byWeight{} }, lbtest.WeightOptions{Keyed: true})
	lbtest.Run(t, func() lb.Selector { return byWeight{} }, lbtest.Options{})
}

func TestHealth(t *testing.T) {
	h := lbtest.NewHealth(1)
	var calls int
	sub := h.OnChange(func(prev, next int32) {
		calls++
		if prev != 1 || next != -1 {
			t.Errorf("transition = %d to %d", prev, next)
		}
	})
	h.Set(1)
	h.Set(-1)
	sub.Unsubscribe()
	sub.Unsubscribe()
	h.Set(1)
	if calls != 1 || h.Get() != 1 {
		t.Errorf("calls = %d, status = %d", calls, h.Get())
	}
}

func BenchmarkSuite(b *testing.B) {
	lbtest.Bench(b, newByKey)
}
