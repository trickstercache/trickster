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

package relay_test

import (
	"slices"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
)

func filtered(f message.LocationFilter) message.Params {
	var p message.Params
	p.SetLocationFilter(f)
	return p
}

func TestJoinReplaysCurrentGroup(t *testing.T) {
	h := newHarness(t, relay.Options{LiveEdgeGroups: 5}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 0, 1)
	// joining after both groups ended, as a player does a catalog: the newest group is replayed from the
	// subscription's start, older groups are left to a fill or a FETCH, and a Next Object start replays nothing
	tests := []struct {
		name string
		p    message.Params
		want []string
	}{
		{"unfiltered", message.Params{}, groupPayloads(1)},
		{"current group", filtered(message.LocationFilter{Type: message.FilterRelativeStart, StartGroup: 1}),
			groupPayloads(1)},
		{"mid-group", filtered(message.LocationFilter{Type: message.FilterAbsoluteStart, StartGroup: 1, StartObject: 2}),
			[]string{"g1-o2"}},
		{"next object", filtered(message.LocationFilter{Type: message.FilterNextObject}), nil},
	}
	for _, tc := range tests {
		jreq, jr := subscribe(t, cl, "video", tc.p)
		subscribed(t, jreq)
		eventually(t, tc.name+" replay", func() bool { return jr.count() == len(tc.want) })
		time.Sleep(20 * time.Millisecond)
		if got := payloads(jr.received()); !slices.Equal(got, tc.want) {
			t.Errorf("%s: replayed %v, want %v", tc.name, got, tc.want)
		}
	}
	if n := len(h.o.fetchAsks()); n != 0 {
		t.Errorf("a replay is served from the live edge, not fetched (%d fetches)", n)
	}
}

func TestJoinReplayContinuesLive(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	out := h.o.stream("video", 0)[0]
	if err := writeObject(out, pubObj{id: 0, payload: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first object", func() bool { return r.count() == 1 })
	jreq, jr := subscribe(t, cl, "video", message.Params{})
	subscribed(t, jreq)
	if err := writeObject(out, pubObj{id: 1, payload: []byte("b")}); err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	// the open group's replay and its live objects share one stream, which begins the subgroup
	eventually(t, "the joiner's objects", func() bool { return jr.count() == 2 })
	jr.mu.Lock()
	defer jr.mu.Unlock()
	if jr.streams != 1 || !jr.headers[0].FirstObject {
		t.Errorf("streams %d, header %+v", jr.streams, jr.headers[0])
	}
}

func TestJoinReplaysDatagrams(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	h.o.datagrams = true
	cl := h.client()
	req, r := subscribe(t, cl, "audio", message.Params{})
	subscribed(t, req)
	h.o.publish(t, "audio", 0, objs("a", "b")...)
	recv(t, r.datagrams, "the first datagram")
	recv(t, r.datagrams, "the second datagram")
	jreq, jr := subscribe(t, cl, "audio", message.Params{})
	subscribed(t, jreq)
	got := []string{recv(t, jr.datagrams, "a replayed datagram").payload, recv(t, jr.datagrams, "a replayed datagram").payload}
	slices.Sort(got)
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("replayed %v", got)
	}
}
