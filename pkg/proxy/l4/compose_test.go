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

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestComposeIdentityAndDatagrams(t *testing.T) {
	if Compose() != nil || Compose(nil, nil) != nil {
		t.Fatal("nothing was composed into an admission")
	}
	one := &stageVerdicts{datagrams: true}
	if Compose(nil, one) != Admission(one) {
		t.Fatal("a lone admission was wrapped")
	}
	if !Compose(one).Datagrams() {
		t.Fatal("a lone datagram admission lost Datagrams")
	}
	held := &fixedHold{d: time.Second}
	if Compose(held) != Admission(held) {
		t.Fatal("a lone holder was wrapped")
	}
	if _, ok := Chain(&stageVerdicts{}, &stageVerdicts{}).(Holder); ok {
		t.Fatal("Chain implements Holder")
	}

	plain := &stageVerdicts{}
	grams := &stageVerdicts{datagram: Drop, datagrams: true}
	c := Compose(plain, nil, grams)
	if !c.Datagrams() {
		t.Fatal("datagrams were not the or of the parts")
	}
	if Compose(plain, &stageVerdicts{}).Datagrams() {
		t.Fatal("compose judges datagrams no part asked for")
	}
	if v := c.Datagram(Flow{}, 1); v != Drop || plain.asked != 0 || grams.asked != 1 {
		t.Fatalf("datagram %v asks %d %d", v, plain.asked, grams.asked)
	}
}

func TestComposeHoldFollowsTheDenyingPart(t *testing.T) {
	acl := &stageVerdicts{peer: Reject}
	lim := &fixedHold{d: 1500 * time.Millisecond}
	c := Compose(acl, lim)
	h, ok := c.(Holder)
	if !ok {
		t.Fatal("composed admission has no hold")
	}
	if got := h.Hold(Flow{}); got != DefaultDeniedHold || lim.holds.Load() != 0 {
		t.Fatalf("acl hold %v, limiter asked %d", got, lim.holds.Load())
	}

	acl.peer = Allow
	if got := h.Hold(Flow{}); got != lim.d || lim.holds.Load() != 1 || lim.peers.Load() != 0 {
		t.Fatalf("limiter hold %v peers %d holds %d", got, lim.peers.Load(), lim.holds.Load())
	}

	// a holder that is not denying leaves the later denial to the access list
	lim.d = 0
	later := &stageVerdicts{peer: Drop}
	got := Compose(lim, later).(Holder).Hold(Flow{})
	if got != DefaultDeniedHold || lim.peers.Load() != 0 {
		t.Fatalf("later acl hold %v, limiter peer %d", got, lim.peers.Load())
	}
}

func TestComposeConcurrentDeniers(t *testing.T) {
	aclClient := netip.MustParseAddr("192.0.2.1")
	limClient := netip.MustParseAddr("192.0.2.2")
	acl := &clientGate{deny: aclClient}
	lim := &clientHold{deny: limClient, d: 2 * time.Second}
	h := Compose(acl, lim).(Holder)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if got := h.Hold(Flow{Client: netip.AddrPortFrom(aclClient, 1)}); got != DefaultDeniedHold {
				t.Errorf("acl hold %v", got)
			}
		}()
		go func() {
			defer wg.Done()
			if got := h.Hold(Flow{Client: netip.AddrPortFrom(limClient, 1)}); got != lim.d {
				t.Errorf("limiter hold %v", got)
			}
		}()
	}
	wg.Wait()
	if lim.peers.Load() != 0 {
		t.Fatalf("limiter peer was asked %d times from Hold", lim.peers.Load())
	}
}

type fixedHold struct {
	stageVerdicts
	d     time.Duration
	peers atomic.Int32
	holds atomic.Int32
}

func (h *fixedHold) Peer(f Flow) Verdict {
	h.peers.Add(1)
	return h.stageVerdicts.Peer(f)
}

func (h *fixedHold) Hold(Flow) time.Duration {
	h.holds.Add(1)
	return h.d
}

type clientGate struct {
	deny netip.Addr
}

func (g *clientGate) Peer(f Flow) Verdict {
	if f.Client.Addr() == g.deny {
		return Reject
	}
	return Allow
}

func (g *clientGate) Flow(Flow) Verdict          { return Allow }
func (g *clientGate) Datagram(Flow, int) Verdict { return Allow }
func (g *clientGate) Datagrams() bool            { return false }

type clientHold struct {
	deny  netip.Addr
	d     time.Duration
	peers atomic.Int32
}

func (h *clientHold) Peer(f Flow) Verdict {
	h.peers.Add(1)
	if f.Client.Addr() == h.deny {
		return Drop
	}
	return Allow
}

func (h *clientHold) Hold(f Flow) time.Duration {
	if f.Client.Addr() == h.deny {
		return h.d
	}
	return 0
}

func (h *clientHold) Flow(Flow) Verdict          { return Allow }
func (h *clientHold) Datagram(Flow, int) Verdict { return Allow }
func (h *clientHold) Datagrams() bool            { return false }
