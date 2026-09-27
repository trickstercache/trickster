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

package sticky

import (
	"net/netip"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/flow"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const flowClient = "192.0.2.1:50000"

// flows returns the stream and native persistence that a sticky block configures on an ALB of
// its own
func flows(t *testing.T, doc string) *Flows {
	t.Helper()
	o := &options.Options{}
	require.NoError(t, yaml.Unmarshal([]byte(doc), o))
	require.NoError(t, o.Initialize())
	require.NoError(t, o.Validate())
	name := t.Name()
	t.Cleanup(func() { ForgetTablesExcept(func(n string, _ *Table) bool { return n != name }) })
	p := NewFlows(name, o)
	require.NotNil(t, p)
	return p
}

func balancerOf(t *testing.T, members ...*lb.Member) *lb.Balancer {
	t.Helper()
	p, err := lb.NewPool(members, 0)
	require.NoError(t, err)
	t.Cleanup(p.Stop)
	return lb.NewBalancer(rr.NewAt(0), lb.BalancerOptions{Pool: p})
}

func streamFlow(client string) flow.Flow {
	return flow.Flow{Client: netip.MustParseAddrPort(client)}
}

// pickFlow begins a session for the client's flow and picks its path from bal, as an adapter does
func pickFlow(p *Flows, bal lb.Picker, client string) (*FlowSession, lb.LeafPick, bool) {
	s := new(FlowSession)
	p.Begin(s, p.StreamKey(streamFlow(client)), time.Now().UnixNano())
	pk, ok := lb.PickLeafFunc(bal, func(depth int, level lb.Picker, via *lb.Member) lb.Flow {
		return s.Flow(depth, level, via, lb.Flow{})
	})
	s.Picked(pk)
	return s, pk, ok
}

func TestNewFlows(t *testing.T) {
	require.Nil(t, NewFlows(albName, nil))
	for _, mode := range []string{options.ModeCookie, options.ModeHeader} {
		require.Nil(t, NewFlows(albName, &options.Options{Mode: mode}), "%s mode keeps no table", mode)
	}
	var none *Flows
	require.Nil(t, none.Table())
	p := flows(t, "{}")
	require.NotNil(t, p.Table(), "the default mode keeps flows in a table")
	// a table-mode ALB keeps its requests and its flows in one table
	h := persistence(t, "mode: table")
	require.Same(t, h.Table(), flows(t, "mode: table").Table())
	// options that were never initialized still key an IPv6 client by its /64
	bare := NewFlows(t.Name()+"-bare", &options.Options{})
	t.Cleanup(func() { ForgetTablesExcept(func(n string, _ *Table) bool { return n != t.Name()+"-bare" }) })
	require.Equal(t, bare.SessionKey("", netip.MustParseAddr("2001:db8::1")),
		bare.SessionKey("", netip.MustParseAddr("2001:db8::2")))
}

func TestFlowKeysFollowTheTableKey(t *testing.T) {
	sni := flows(t, "table: {key: sni}")
	f := streamFlow(flowClient)
	require.False(t, sni.StreamKey(f).OK, "a flow with no server name has an sni key")
	f.ServerName = "shop.example.com"
	require.True(t, sni.StreamKey(f).OK)
	user := flows(t, "table: {key: user}")
	require.True(t, user.SessionKey("alice", netip.Addr{}).OK)
	require.False(t, user.SessionKey("", netip.MustParseAddr("192.0.2.1")).OK)
	require.False(t, user.StreamKey(f).OK, "a flow carries no user")
}

func TestFlowsPinOnSettleAndHonorAfter(t *testing.T) {
	p := flows(t, "{}")
	a, b := lb.NewMember(lb.MemberOptions{Name: "a"}), lb.NewMember(lb.MemberOptions{Name: "b"})
	bal := balancerOf(t, a, b)
	// a first flow is a miss, and is pinned only once its member is reached
	s, pk, ok := pickFlow(p, bal, flowClient)
	require.True(t, ok)
	first := pk.Member()
	pk.Done(lb.OutcomeOK)
	require.Zero(t, p.Table().Len(), "a flow was pinned before its member was reached")
	s.Settle()
	s.Settle()
	require.Equal(t, 1, p.Table().Len())
	require.Equal(t, 1.0, count(t, ResultMiss), "a flow is counted once")
	// every later flow from the client lands on the same member, however the rotation turns
	for range 4 {
		s, pk, ok = pickFlow(p, bal, "192.0.2.1:61000")
		require.True(t, ok)
		require.Same(t, first, pk.Member())
		require.True(t, pk.Level(0).Pinned())
		require.True(t, s.OnPins())
		pk.Done(lb.OutcomeOK)
		s.Settle()
	}
	require.Equal(t, 4.0, count(t, ResultHit))
	// another client is a separate session
	s, pk, _ = pickFlow(p, bal, "192.0.2.2:50000")
	require.False(t, pk.Level(0).Pinned())
	pk.Done(lb.OutcomeOK)
	s.Settle()
	require.Equal(t, 2, p.Table().Len())
}

// pinTo pins the client's flows to the member, as an earlier flow would have
func pinTo(t *testing.T, p *Flows, client string, m *lb.Member) uint64 {
	t.Helper()
	key := p.StreamKey(streamFlow(client))
	require.True(t, p.Table().Put(key.Hash, Path{Hashes: [lb.MaxPickDepth]uint64{m.Hash()}, Depth: 1},
		time.Now().UnixNano()))
	return key.Hash
}

func TestFlowsRefuseWhenThePinnedMemberIsUnavailable(t *testing.T) {
	p := flows(t, "{on_unavailable: reject}")
	a := lb.NewMember(lb.MemberOptions{Name: "a", Health: fixedHealth(-1)})
	b := lb.NewMember(lb.MemberOptions{Name: "b"})
	bal := balancerOf(t, a, b)
	pinTo(t, p, flowClient, a)
	// the pinned member is down but still in the pool: the pick moves, and the adapter refuses it
	s, pk, ok := pickFlow(p, bal, flowClient)
	require.True(t, ok)
	require.Same(t, b, pk.Member())
	require.True(t, s.Rejects())
	require.True(t, s.Unavailable())
	pk.Done(lb.OutcomeCanceled)
	s.Refuse()
	s.Refuse()
	s.Settle()
	require.Equal(t, 1.0, count(t, ResultRejected), "a refused flow is counted once")
	require.Zero(t, count(t, ResultRepick))
	// a balancer with no pool has no member to refuse for
	bal.SetPool(nil)
	s, _, ok = pickFlow(p, bal, flowClient)
	require.False(t, ok)
	require.False(t, s.Unavailable())
	// a pin to a member that has left the pool is not a refusal: there is no session to keep
	bal.SetPool(balancerOf(t, b).Pool())
	s, pk, ok = pickFlow(p, bal, flowClient)
	require.True(t, ok)
	require.False(t, s.Unavailable())
	pk.Done(lb.OutcomeOK)
}

func TestFlowsMoveWhenThePinnedMemberIsUnavailable(t *testing.T) {
	p := flows(t, "{}")
	a := lb.NewMember(lb.MemberOptions{Name: "a", Health: fixedHealth(-1)})
	b := lb.NewMember(lb.MemberOptions{Name: "b"})
	bal := balancerOf(t, a, b)
	key := pinTo(t, p, flowClient, a)
	s, pk, ok := pickFlow(p, bal, flowClient)
	require.True(t, ok)
	require.Same(t, b, pk.Member())
	require.False(t, s.Rejects())
	pk.Done(lb.OutcomeOK)
	s.Settle()
	require.Equal(t, 1.0, count(t, ResultRepick))
	moved, ok := p.Table().Get(key, time.Now().UnixNano())
	require.True(t, ok)
	require.Equal(t, b.Hash(), moved.Hashes[0], "the moved session was not pinned where it went")
}

func TestFlowSessionLevels(t *testing.T) {
	p := flows(t, "{}")
	a, x := lb.NewMember(lb.MemberOptions{Name: "a"}), lb.NewMember(lb.MemberOptions{Name: "x"})
	var s FlowSession
	p.Begin(&s, p.StreamKey(streamFlow(flowClient)), time.Now().UnixNano())
	s.Pins = Path{Hashes: [lb.MaxPickDepth]uint64{a.Hash(), 0x2222}, Depth: 2}
	f := s.Flow(0, nil, nil, lb.Flow{Key: 9, HasKey: true})
	require.Equal(t, lb.Flow{Key: 9, HasKey: true, Pin: a.Hash(), HasPin: true}, f)
	f = s.Flow(1, nil, a, lb.Flow{})
	require.Equal(t, uint64(0x2222), f.Pin, "the next level is pinned below its pinned member")
	f = s.Flow(1, nil, x, lb.Flow{})
	require.False(t, f.HasPin, "a flow moved at the first level was pinned below it")
	require.Equal(t, lb.Flow{Key: 1}, s.Flow(lb.MaxPickDepth, nil, a, lb.Flow{Key: 1}))
	require.Equal(t, lb.Flow{Key: 1}, s.Flow(-1, nil, nil, lb.Flow{Key: 1}))
	// a pin asked of a picker that cannot tell whether its pool has the member is never refused
	s.Flow(0, plainPicker{}, nil, lb.Flow{})
	require.False(t, s.Unavailable())
}

func TestZeroFlowSession(t *testing.T) {
	var s FlowSession
	require.False(t, s.Rejects())
	s.Refuse()
	s.Settle()
	require.False(t, s.OnPins())
	// a session sent nowhere settles nothing
	p := flows(t, "{}")
	p.Begin(&s, p.StreamKey(streamFlow(flowClient)), time.Now().UnixNano())
	s.Settle()
	require.Zero(t, count(t, ResultMiss))
	require.Zero(t, p.Table().Len())
	// a flow with nothing to key on is a miss, and is never pinned
	var keyless FlowSession
	p.Begin(&keyless, p.StreamKey(flow.Flow{}), time.Now().UnixNano())
	keyless.Record(0, 1)
	keyless.Settle()
	require.Equal(t, 1.0, count(t, ResultMiss))
	require.Zero(t, p.Table().Len())
}

// fixedHealth is a member's health that never changes
type fixedHealth int32

func (h fixedHealth) Get() int32 { return int32(h) }

// plainPicker is a picker with nothing to say about pins
type plainPicker struct{}

func (plainPicker) Needs() lb.Needs { return 0 }

func (plainPicker) Pick(lb.Flow) (lb.Pick, bool) { return lb.Pick{}, false }

func TestFlowSessionStranded(t *testing.T) {
	p := flows(t, "{}")
	a, x := lb.NewMember(lb.MemberOptions{Name: "a"}), lb.NewMember(lb.MemberOptions{Name: "x"})
	var s FlowSession
	p.Begin(&s, p.StreamKey(streamFlow(flowClient)), time.Now().UnixNano())
	s.Record(0, a.Hash())
	require.False(t, s.Stranded(), "a session with no pins followed none")
	pinTo(t, p, flowClient, a)
	p.Begin(&s, p.StreamKey(streamFlow(flowClient)), time.Now().UnixNano())
	require.False(t, s.Stranded(), "a pick that never left the first level followed no pin down")
	s.Record(0, a.Hash())
	require.True(t, s.Stranded())
	s.Record(0, x.Hash())
	require.False(t, s.Stranded(), "a pick sent away from its pin was stranded by the strategy")
}
