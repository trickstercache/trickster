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
package lb

import (
	"math"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestStatsObservePeakAndDecay(t *testing.T) {
	const decay = float64(10 * time.Second)
	ms := float64(time.Millisecond)
	s := &Stats{}
	t0 := int64(1_000_000_000)
	s.observe(40*ms, t0, decay)
	if s.Latency() != 40*time.Millisecond || s.LastSample().UnixNano() != t0 {
		t.Fatalf("first sample: %v at %v", s.Latency(), s.LastSample())
	}
	// a higher sample replaces the average at once
	s.observe(90*ms, t0+1, decay)
	if s.Latency() != 90*time.Millisecond {
		t.Errorf("peak = %v", s.Latency())
	}
	// a lower sample right away barely moves it; one decay later it has moved 1-1/e of the way
	s.observe(10*ms, t0+2, decay)
	if got := s.Latency(); got < 89*time.Millisecond {
		t.Errorf("an immediate low sample moved the average to %v", got)
	}
	s.observe(10*ms, t0+2+int64(decay), decay)
	got := float64(s.Latency())
	if expect := 10*ms + 80*ms/math.E; math.Abs(got-expect) > ms {
		t.Errorf("after one decay = %v, want about %v", time.Duration(got), time.Duration(expect))
	}
	// sub-millisecond samples keep their resolution
	fast := &Stats{}
	fast.observe(250_000, t0, decay)
	if fast.Latency() != 250*time.Microsecond {
		t.Errorf("sub-millisecond sample = %v", fast.Latency())
	}
	// a clock that steps backwards does not inflate the average
	fast.observe(100_000, t0-5, decay)
	if fast.Latency() > 250*time.Microsecond {
		t.Errorf("after a backwards step = %v", fast.Latency())
	}
}

func TestStatsFaded(t *testing.T) {
	s := &Stats{}
	now := time.Unix(100, 0)
	if s.Faded(now, time.Second) != 0 {
		t.Error("no sample fades to something")
	}
	s.observe(float64(8*time.Second), now.UnixNano(), float64(time.Second))
	for name, got := range map[string]time.Duration{
		"at the sample time":     s.Faded(now, 10*time.Second),
		"before the sample time": s.Faded(now.Add(-time.Hour), 10*time.Second),
		"with no decay":          s.Faded(now.Add(time.Minute), 0),
	} {
		if got != 8*time.Second {
			t.Errorf("%s = %v", name, got)
		}
	}
	// one decay later it is near 1/e; it keeps falling, and never turns negative
	got := s.Faded(now.Add(10*time.Second), 10*time.Second)
	eightSeconds := float64(8 * time.Second)
	if want := time.Duration(eightSeconds / math.E); got < want || got > want+200*time.Millisecond {
		t.Errorf("one decay later = %v, want a little above %v", got, want)
	}
	last := got
	for _, after := range []time.Duration{20 * time.Second, time.Minute, time.Hour, 1000 * time.Hour} {
		next := s.Faded(now.Add(after), 10*time.Second)
		if next >= last || next < 0 {
			t.Errorf("after %v = %v, not below %v", after, next, last)
		}
		last = next
	}
	if tenth := s.Faded(now.Add(80*time.Second), 10*time.Second); tenth > 80*time.Millisecond {
		t.Errorf("a 8s penalty is still %v after eight decays", tenth)
	}
}

func TestHashes(t *testing.T) {
	// golden values: a change here moves every client's affinity on every deployed replica
	if got := HashString("tenant-42"); got != 0x5d8d50c585b0dfd7 {
		t.Errorf("HashString = %#x", got)
	}
	if HashBytes([]byte("tenant-42")) != HashString("tenant-42") {
		t.Error("HashBytes and HashString disagree")
	}
	if HashFold("API.Example.COM") != HashString("api.example.com") || HashFold("a") == HashFold("b") {
		t.Error("HashFold does not fold ASCII case")
	}
	if Mix(1) == Mix(2) || Mix(0) != 0 {
		t.Error("unexpected mix")
	}
	v4 := netip.MustParseAddr("192.0.2.7")
	if HashAddr(v4, 64) != HashAddr(netip.MustParseAddr("::ffff:192.0.2.7"), 64) {
		t.Error("an IPv4-mapped address keys differently from its IPv4 form")
	}
	if HashAddr(v4, 64) == HashAddr(netip.MustParseAddr("192.0.2.8"), 64) {
		t.Error("distinct IPv4 addresses share a key")
	}
	a := netip.MustParseAddr("2001:db8:1:2:aaaa:bbbb:cccc:dddd")
	b := netip.MustParseAddr("2001:db8:1:2:1111:2222:3333:4444")
	c := netip.MustParseAddr("2001:db8:1:3:aaaa:bbbb:cccc:dddd")
	if HashAddr(a, 64) != HashAddr(b, 64) {
		t.Error("two addresses in one /64 key differently")
	}
	if HashAddr(a, 64) == HashAddr(c, 64) {
		t.Error("two /64s share a key")
	}
	for _, whole := range []int{0, 128, 200, -1} {
		if HashAddr(a, whole) == HashAddr(b, whole) {
			t.Errorf("prefix %d did not keep the whole address", whole)
		}
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = HashAddr(a, 64) + HashFold("Host") }); allocs != 0 {
		t.Errorf("hashing allocates %v", allocs)
	}
}

func FuzzHashBytes(f *testing.F) {
	f.Add([]byte("client"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		if HashBytes(b) != HashString(string(b)) {
			t.Errorf("HashBytes and HashString disagree on %q", b)
		}
	})
}

func TestLeast(t *testing.T) {
	var pos atomic.Uint64
	if Least(nil, &pos, func(*Member) float64 { return 0 }) != nil {
		t.Error("the least of nothing is something")
	}
	members := []*Member{
		NewMember(MemberOptions{Name: "a", Weight: 1}),
		NewMember(MemberOptions{Name: "b", Weight: 3}),
		NewMember(MemberOptions{Name: "c", Weight: 1}),
	}
	scores := map[*Member]float64{members[0]: 5, members[1]: 2, members[2]: 9}
	score := func(m *Member) float64 { return scores[m] }
	for range 5 {
		if got := Least(members, &pos, score); got != members[1] {
			t.Fatalf("least = %s", got.Name())
		}
	}
	// ties share the picks by weight, however many rounds are played
	scores[members[2]] = 2
	counts := map[*Member]int{}
	for range 40 {
		counts[Least(members, &pos, score)]++
	}
	if counts[members[0]] != 0 || counts[members[1]] != 30 || counts[members[2]] != 10 {
		t.Errorf("tied picks = a:%d b:%d c:%d, want 0, 30, 10",
			counts[members[0]], counts[members[1]], counts[members[2]])
	}
	// a tied set that moves between the two passes still yields a member at the low score
	var calls int
	shifty := func(m *Member) float64 {
		calls++
		if calls > len(members) && m != members[0] {
			return 7
		}
		return 1
	}
	if got := Least(members, &pos, shifty); got != members[0] {
		t.Errorf("after the tie dissolved = %s", got.Name())
	}
	calls = 0
	vanishing := func(*Member) float64 {
		calls++
		if calls > len(members) {
			return 7
		}
		return 1
	}
	if got := Least(members, &pos, vanishing); got != members[0] {
		t.Errorf("after every tie vanished = %s", got.Name())
	}
}
