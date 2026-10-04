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

package l4

import "testing"

type stageVerdicts struct { // answers each stage with a fixed verdict and counts the asks
	peer, flow, datagram Verdict
	datagrams            bool
	asked                int
}

func (s *stageVerdicts) Peer(Flow) Verdict          { s.asked++; return s.peer }
func (s *stageVerdicts) Flow(Flow) Verdict          { s.asked++; return s.flow }
func (s *stageVerdicts) Datagram(Flow, int) Verdict { s.asked++; return s.datagram }
func (s *stageVerdicts) Datagrams() bool            { return s.datagrams }

func TestChain(t *testing.T) {
	if Chain() != nil || Chain(nil, nil) != nil {
		t.Fatal("a chain of nothing admits through an admission")
	}
	one := &stageVerdicts{}
	if Chain(nil, one) != Admission(one) {
		t.Fatal("a lone admission was wrapped")
	}

	// the first refusal decides, and the parts after it are not asked
	first := &stageVerdicts{peer: Reject, flow: Allow}
	second := &stageVerdicts{peer: Allow, flow: Drop, datagram: Drop, datagrams: true}
	c := Chain(first, nil, second)
	if v := c.Peer(Flow{}); v != Reject || second.asked != 0 {
		t.Fatalf("peer: verdict %v, second asked %d times", v, second.asked)
	}
	if v := c.Flow(Flow{}); v != Drop || first.asked != 2 || second.asked != 1 {
		t.Fatalf("flow: verdict %v, asks %d and %d", v, first.asked, second.asked)
	}

	// only a part that judges datagrams is asked about one
	if !c.Datagrams() {
		t.Fatal("a chain with a datagram-judging part does not judge datagrams")
	}
	if v := c.Datagram(Flow{}, 1); v != Drop || first.asked != 2 || second.asked != 2 {
		t.Fatalf("datagram: verdict %v, asks %d and %d", v, first.asked, second.asked)
	}
	if Chain(first, one).Datagrams() {
		t.Fatal("a chain judges datagrams no part asked for")
	}
	if v := Chain(one, &stageVerdicts{}).Peer(Flow{}); v != Allow {
		t.Fatalf("allowing parts refused: %v", v)
	}
}
