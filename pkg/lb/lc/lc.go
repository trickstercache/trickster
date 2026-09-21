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
// Package lc is the least connections strategy: each flow goes to the member with the fewest
// flows in flight for its weight. It suits long-lived work and small pools; it reads every
// member per pick.
package lc

import (
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// Name is the strategy's name.
const Name = "least_connections"

// New returns a least connections selector.
func New() lb.Selector {
	return &selector{}
}

type selector struct {
	// rotation among members that tie, which is all of them while the pool is idle
	pos atomic.Uint64
}

func (s *selector) Name() string { return Name }

func (s *selector) Needs() lb.Needs { return lb.NeedInflight }

func (s *selector) Prepare(snap *lb.Snapshot) lb.Prepared {
	return &prepared{pos: &s.pos, members: snap.Members}
}

type prepared struct {
	pos     *atomic.Uint64
	members []*lb.Member
}

func (p *prepared) Select(lb.Flow) *lb.Member {
	return lb.Least(p.members, p.pos, load)
}

// load is the member's in-flight count per unit of weight; a weight is a capacity
func load(m *lb.Member) float64 {
	return float64(m.Stats().Inflight()) / float64(m.Weight())
}
