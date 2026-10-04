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
	"context"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func TestInvalidFilters(t *testing.T) {
	// a filter that cannot be honored refuses its request, or fails its update and ends that subscription
	h := newHarness(t, relay.Options{LiveEdgeGroups: 5}, nil)
	cl := h.client()
	bad := []message.RangeFilter{{Type: message.ParamPriorityFilter, Ranges: []wire.Range{{Start: 0, End: 300}}}}
	req, _ := subscribe(t, cl, "video", message.Params{RangeFilters: bad})
	refusedWith(t, req, message.RequestInvalidFilter)
	fp := fetchRange(0, 0, 0, 0)
	fp.RangeFilters = bad
	freq, _ := fetch(t, cl, "video", fp)
	refusedWith(t, freq, message.RequestInvalidFilter)

	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	if _, err := req.Update(message.Params{RangeFilters: bad}); err != nil {
		t.Fatal(err)
	}
	if re, ok := recv(t, r.messages, "the update's answer").(*message.RequestError); !ok ||
		re.Code != message.RequestInvalidFilter {
		t.Errorf("the update is refused with INVALID_FILTER, got %+v", re)
	}
	if done, ok := recv(t, r.messages, "the subscription's end").(*message.PublishDone); !ok ||
		done.Code != message.DoneUpdateFailed {
		t.Errorf("the subscription ends with UPDATE_FAILED, got %+v", done)
	}
}

func TestFetchGapLimit(t *testing.T) {
	// each run of missing groups is one upstream fetch, and gaps past max_gap_fetches are reported unknown
	h := newHarness(t, relay.Options{LiveEdgeGroups: 5, MaxGapFetches: 1}, nil)
	h.o.store("video", 0, objs(groupPayloads(0)...)...)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 1)
	h.o.store("video", 2, objs(groupPayloads(2)...)...)
	publishGroups(t, h, r, 3)
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 3, 2))
	fetched(t, freq, fr)
	if asks := h.o.fetchAsks(); len(asks) != 1 || asks[0].start.Group != 0 || asks[0].end.Group != 0 {
		t.Errorf("only the first gap is fetched, got %+v", asks)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	var groups []uint64
	for _, fo := range fr.fetched {
		if fo.EndOfRange != message.RangeEndNone {
			if fo.EndOfRange != message.RangeEndUnknown || fo.Group != 2 {
				t.Errorf("the second gap is reported unknown, got %+v", fo)
			}
			continue
		}
		groups = append(groups, fo.Group)
	}
	if !slices.Equal(groups, []uint64{0, 0, 0, 1, 1, 1, 3, 3, 3}) {
		t.Errorf("served groups %v", groups)
	}
}

func TestFillParameters(t *testing.T) {
	// a fill takes its own group order, priority and range filters, and its range ends at the largest object
	h := newHarness(t, relay.Options{LiveEdgeGroups: 5}, nil)
	cl := h.client()
	first, frec := subscribe(t, cl, "video", message.Params{})
	subscribed(t, first)
	publishGroups(t, h, frec, 0, 1, 2)
	var p message.Params
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterNextObject})
	fill := &message.Params{}
	fill.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteRange, EndGroupDelta: 10})
	fill.SetGroupOrder(message.GroupOrderDescending)
	fill.SetSubscriberPriority(7)
	fill.RangeFilters = []message.RangeFilter{{Type: message.ParamObjectIDFilter, Ranges: []wire.Range{{Start: 1, End: 1}}}}
	p.SetFill(fill)
	req, r := subscribe(t, cl, "video", p)
	subscribed(t, req)
	recv(t, r.fetchDone, "the fill stream")
	r.mu.Lock()
	payloads := slices.Clone(r.payloads)
	r.mu.Unlock()
	if want := []string{"g2-o1", "g1-o1", "g0-o1"}; !slices.Equal(payloads, want) {
		t.Errorf("filled %v, want %v", payloads, want)
	}
}

func TestFetchBeforeAnyObject(t *testing.T) {
	// a track a publisher has opened but sent nothing on has no range to fetch
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	pub := h.session(nil)
	preq, _ := startPublish(t, pub, "video", message.Params{}, nil)
	if m := response(t, preq); m.Kind() != message.KindRequestOK {
		t.Fatalf("the publisher is accepted, got %+v", m)
	}
	viewer := h.session(nil)
	r := newRec()
	freq, err := viewer.Open(context.Background(), &message.Fetch{Track: track("video"), Params: fetchRange(0, 0, 0, 0)},
		r, r)
	if err != nil {
		t.Fatal(err)
	}
	refusedWith(t, freq, message.RequestInvalidRange)
}

func TestSubscribeTracksPrefixOverlap(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	viewer := h.session(&tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()})
	open := func(prefix wire.Namespace) *session.Request {
		t.Helper()
		req, err := viewer.Open(context.Background(), &message.SubscribeTracks{Prefix: prefix}, newRec(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	if _, ok := response(t, open(ns("live"))).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	refusedWith(t, open(ns("live", "studio")), message.RequestPrefixOverlap)
}

func TestPublishLargestObject(t *testing.T) {
	// a PUBLISH naming its largest object tells TRACK_STATUS where the track stands before any object arrives
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	var p message.Params
	p.SetLargestObject(wire.Location{Group: 5, Object: 2})
	preq, _ := startPublish(t, h.session(nil), "video", p, nil)
	if m := response(t, preq); m.Kind() != message.KindRequestOK {
		t.Fatalf("the publisher is accepted, got %+v", m)
	}
	req, err := h.session(nil).Open(context.Background(), &message.TrackStatus{Track: track("video")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ok, isOK := response(t, req).(*message.RequestOK)
	if !isOK || !ok.Params.Has(message.ParamLargestObject) ||
		ok.Params.LargestObject != (wire.Location{Group: 5, Object: 2}) {
		t.Errorf("TRACK_STATUS answered %+v", ok)
	}
}

func TestDatagramStatusAndMalformed(t *testing.T) {
	// a status datagram is kept as a status, and a datagram repeating an object with other bytes makes the track
	// malformed
	h := newHarness(t, relay.Options{}, nil)
	h.o.datagrams = true
	cl := h.client()
	req, r := subscribe(t, cl, "audio", message.Params{})
	subscribed(t, req)
	h.o.publish(t, "audio", 0, pubObj{id: 0, payload: []byte("a")}, pubObj{id: 1, status: message.StatusEndOfGroup})
	for range 2 {
		recv(t, r.datagrams, "a datagram")
	}
	h.o.publish(t, "audio", 0, pubObj{id: 0, payload: []byte("b")})
	if done, ok := recv(t, r.messages, "PUBLISH_DONE").(*message.PublishDone); !ok ||
		done.Code != message.DoneMalformedTrack {
		t.Fatalf("got %+v", done)
	}
}
