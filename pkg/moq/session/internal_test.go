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

package session

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func TestRequiredReleasedAtHandOff(t *testing.T) {
	// a dependent request waits for the one it requires to be handed off, not merely to arrive (§9.2)
	synctest.Test(t, func(t *testing.T) {
		c, _ := codec.ForDraft(17)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := &Session{codec: c, ctx: ctx, opts: Options{Role: RoleServer}}
		done := make(chan bool, 1)
		go func() {
			arrived, _ := s.awaitRequired(2, 1)
			done <- arrived
		}()
		synctest.Wait()
		if err := s.acceptPeerID(0); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("released when the required request arrived, before it was handed off")
		default:
		}
		s.handedOff(0)
		if !<-done {
			t.Error("released as timed out")
		}
		if arrived, _ := s.awaitRequired(4, 2); !arrived {
			t.Error("a request already handed off releases at once")
		}
		go func() {
			arrived, _ := s.awaitRequired(8, 1)
			done <- arrived
		}()
		time.Sleep(requiredRequestWait)
		synctest.Wait()
		if <-done {
			t.Error("a required request never handed off times the dependent out")
		}
		if len(s.requires) != 0 {
			t.Errorf("a timed out waiter is forgotten: %v", s.requires)
		}
		later, _ := codec.ForDraft(18)
		s.codec = later
		s.handedOff(10)
		if s.handed.has(5) {
			t.Error("a draft without required request IDs tracks no hand-offs")
		}
	})
}

func TestIDWindow(t *testing.T) {
	var w idWindow
	for _, n := range []uint64{0, 1, 3, 2, 5} {
		if !w.add(n) {
			t.Fatalf("%d should be new", n)
		}
	}
	for _, n := range []uint64{0, 3, 5} {
		if w.add(n) {
			t.Errorf("%d is a duplicate", n)
		}
	}
	if w.low != 4 || len(w.seen) != 1 {
		t.Errorf("low %d, seen %v", w.low, w.seen)
	}
	// a peer that skips IDs cannot grow the window without bound
	var g idWindow
	for i := range uint64(maxOutOfOrderIDs + 1) {
		g.add(10 + i)
	}
	if len(g.seen) > maxOutOfOrderIDs || g.low < 10 {
		t.Errorf("window grew to %d with low %d", len(g.seen), g.low)
	}
	if g.add(3) {
		t.Error("IDs below the compacted window are duplicates")
	}
}

func TestTokenCache(t *testing.T) {
	c := tokenCache{max: 64}
	reg := wire.Token{AliasType: wire.TokenRegister, Alias: 1, Type: 2, Value: []byte("abc")}
	got, err := c.apply(reg, false)
	if err != nil || got.AliasType != wire.TokenUseValue || string(got.Value) != "abc" {
		t.Fatalf("register resolved to %+v %v", got, err)
	}
	reg.Value[0] = 'x'
	if got, err = c.apply(wire.Token{AliasType: wire.TokenUseAlias, Alias: 1}, false); err != nil ||
		string(got.Value) != "abc" || got.Type != 2 {
		t.Errorf("a registered value is kept apart from the message buffer: %+v %v", got, err)
	}
	code := func(err error) message.SessionCode {
		se, _ := errors.AsType[*message.SessionError](err)
		if se == nil {
			return 0xffff
		}
		return se.Code
	}
	if _, err = c.apply(wire.Token{AliasType: wire.TokenRegister, Alias: 1}, false); code(err) != message.CodeDuplicateAuthTokenAlias {
		t.Errorf("duplicate alias: %v", err)
	}
	if _, err = c.apply(wire.Token{AliasType: wire.TokenUseAlias, Alias: 9}, false); code(err) != message.CodeUnknownAuthTokenAlias {
		t.Errorf("unknown alias: %v", err)
	}
	big := wire.Token{AliasType: wire.TokenRegister, Alias: 2, Value: make([]byte, 60)}
	if _, err = c.apply(big, false); code(err) != message.CodeAuthTokenCacheOverflow {
		t.Errorf("overflow outside SETUP: %v", err)
	}
	if got, err = c.apply(big, true); err != nil || got.AliasType != wire.TokenUseValue {
		t.Errorf("an overflowing SETUP registration is used once: %+v %v", got, err)
	}
	if _, err = c.apply(wire.Token{AliasType: wire.TokenUseAlias, Alias: 2}, false); err == nil {
		t.Error("an overflowed SETUP registration is not kept")
	}
	if _, err = c.apply(wire.Token{AliasType: wire.TokenDelete, Alias: 1}, false); err != nil || c.size != 0 {
		t.Errorf("delete: %v size %d", err, c.size)
	}
	if _, err = c.apply(wire.Token{AliasType: wire.TokenDelete, Alias: 1}, false); code(err) != message.CodeUnknownAuthTokenAlias {
		t.Errorf("deleting an unknown alias: %v", err)
	}
	if _, err = c.apply(wire.Token{AliasType: 7}, false); code(err) != message.CodeMalformedAuthToken {
		t.Errorf("unknown alias type: %v", err)
	}
	if got, err = c.apply(wire.Token{AliasType: wire.TokenUseValue, Value: []byte("v")}, false); err != nil ||
		string(got.Value) != "v" {
		t.Error("use-value passes through")
	}
}

func TestUrgency(t *testing.T) {
	tests := []struct {
		sub, pub uint8
		want     int8
	}{
		{0, 0, 1}, {63, 200, 2}, {128, 0, 3}, {128, 255, 4}, {200, 10, 5}, {255, 255, 6},
	}
	for _, tc := range tests {
		if got := Urgency(tc.sub, tc.pub); got != tc.want {
			t.Errorf("Urgency(%d, %d) = %d, want %d", tc.sub, tc.pub, got, tc.want)
		}
	}
	if UrgencyControl >= Urgency(0, 0) || UrgencyPadding <= Urgency(255, 255) {
		t.Error("control outranks objects, and padding ranks below them")
	}
	if !Incremental(message.GroupOrderDescending) || Incremental(message.GroupOrderAscending) {
		t.Error("incremental only for descending order")
	}
}

func TestReservedNamespace(t *testing.T) {
	ns := func(f ...string) wire.Namespace {
		out := make(wire.Namespace, len(f))
		for i := range f {
			out[i] = []byte(f[i])
		}
		return out
	}
	msgs := []message.Message{
		&message.Subscribe{Track: message.FullTrackName{Namespace: ns(".session", "x")}},
		&message.Fetch{Track: message.FullTrackName{Namespace: ns(".")}},
		&message.TrackStatus{Track: message.FullTrackName{Namespace: ns(".session")}},
		&message.Publish{Track: message.FullTrackName{Namespace: ns(".")}},
		&message.PublishNamespace{Prefix: ns(".session")},
		&message.SubscribeNamespace{Prefix: ns(".")},
		&message.SubscribeTracks{Prefix: ns(".session")},
	}
	for _, m := range msgs {
		if _, ok := reservedNamespace(m); !ok {
			t.Errorf("%s should be reserved", m.Kind())
		}
	}
	for _, m := range []message.Message{
		&message.Subscribe{Track: message.FullTrackName{Namespace: ns(".sessions")}},
		&message.Subscribe{},
		&message.Namespace{},
	} {
		if _, ok := reservedNamespace(m); ok {
			t.Errorf("%+v should not be reserved", m)
		}
	}
}

func TestTombstones(t *testing.T) {
	s := &Session{tombstones: make(map[uint64]time.Time)}
	old := time.Now().Add(-2 * tombstoneTTL)
	for i := range uint64(maxTombstones) {
		s.tombstones[i] = old
	}
	s.tombstone(maxTombstones + 1)
	if len(s.tombstones) != 1 {
		t.Errorf("expired tombstones are swept when the table fills: %d left", len(s.tombstones))
	}
	now := time.Now()
	for i := range uint64(maxTombstones) {
		s.tombstones[i] = now
	}
	s.tombstone(maxTombstones + 2)
	if _, ok := s.tombstones[maxTombstones+2]; ok {
		t.Error("a full table of live tombstones takes no more")
	}
}

func TestKindSet(t *testing.T) {
	s := kinds(message.KindSubscribe, message.KindFetch)
	if !s.has(message.KindFetch) || s.has(message.KindPublish) {
		t.Error("kind set membership")
	}
	for k, f := range flows {
		if f.first == 0 {
			t.Errorf("%s has no first response", k)
		}
	}
}

func TestEarlyDatagramsBounded(t *testing.T) {
	s := &Session{
		tombstones: make(map[uint64]time.Time), aliases: make(map[uint64]*Request),
		early: make(map[uint64][]earlyDatagram), opts: Options{UnknownAliasWait: time.Second},
	}
	s.tombstones[1] = time.Now()
	if s.routeDatagram(1, []byte("x")); s.earlyCount != 0 {
		t.Error("a datagram for an ended subscription is kept")
	}
	old := time.Now().Add(-2 * time.Second)
	for i := range maxEarlyDatagrams {
		s.early[uint64(i%2)+2] = append(s.early[uint64(i%2)+2], earlyDatagram{arrived: old})
	}
	s.earlyCount = maxEarlyDatagrams
	s.early[2][0].arrived = time.Now()
	s.routeDatagram(4, []byte("y"))
	if s.earlyCount != 2 || len(s.early[2]) != 1 || len(s.early[4]) != 1 || s.early[3] != nil {
		t.Errorf("expired datagrams are swept when the table fills: %d kept in %d aliases", s.earlyCount, len(s.early))
	}
	for i := range maxEarlyDatagrams - 2 {
		s.routeDatagram(5, []byte{byte(i)})
	}
	if s.routeDatagram(6, []byte("z")); s.earlyCount != maxEarlyDatagrams || s.early[6] != nil {
		t.Errorf("a full table of fresh datagrams takes no more: %d", s.earlyCount)
	}
}
