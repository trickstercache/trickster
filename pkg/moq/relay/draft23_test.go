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
	"bytes"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func TestDraft23OverlappingPrefixes(t *testing.T) {
	// from -23 a session's SUBSCRIBE_TRACKS may overlap, and a track matching several gets a PUBLISH for each,
	// naming the SUBSCRIBE_TRACKS it answers
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	sink := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	viewer := h.clientDraftWith(23, sink)
	open := func(m message.Message) *session.Request {
		t.Helper()
		req, err := viewer.Open(context.Background(), m, newRec(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := response(t, req).(*message.RequestOK); !ok {
			t.Fatalf("%s refused", m.Kind())
		}
		return req
	}
	wide := open(&message.SubscribeTracks{Prefix: ns()})
	narrow := open(&message.SubscribeTracks{Prefix: ns("live")})
	open(&message.SubscribeNamespace{Prefix: ns()})
	open(&message.SubscribeNamespace{Prefix: ns("live")})
	published(t, h.client(), "video")
	var ids []uint64
	for range 2 {
		pm := recv(t, sink.pubs, "a PUBLISH").Message().(*message.Publish)
		if !pm.Params.Has(message.ParamSubscribeTracksID) {
			t.Fatalf("a PUBLISH without SUBSCRIBE_TRACKS_ID: %+v", pm.Params)
		}
		ids = append(ids, pm.Params.SubscribeTracksID)
	}
	slices.Sort(ids)
	if want := []uint64{wide.RequestID(), narrow.RequestID()}; !slices.Equal(ids, want) {
		t.Errorf("SUBSCRIBE_TRACKS_IDs %v, want %v", ids, want)
	}
}

func TestDraft23SubscribeTracksFill(t *testing.T) {
	// a -23 SUBSCRIBE_TRACKS carrying FILL_PARAMETERS has each PUBLISH carry them, and the relay opens the fill
	// under the PUBLISH's Request ID
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	pub := published(t, h.client(), "video")
	sendGroup(t, pub, 0, objs("old")...)
	sink := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	viewer := h.clientDraftWith(23, sink)
	var fill message.Params
	fill.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteStart})
	var p message.Params
	p.SetFill(&fill)
	st, err := viewer.Open(context.Background(), &message.SubscribeTracks{Prefix: ns("live"), Params: p}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, st).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	offered := recv(t, sink.pubs, "the relay's PUBLISH")
	if pm := offered.Message().(*message.Publish); pm.Params.Fill == nil {
		t.Fatalf("the PUBLISH carries no fill: %+v", pm.Params)
	}
	recv(t, sink.rec.fetchDone, "the fill")
	sink.rec.mu.Lock()
	defer sink.rec.mu.Unlock()
	if len(sink.rec.payloads) == 0 || sink.rec.payloads[0] != "old" {
		t.Errorf("filled %v", sink.rec.payloads)
	}
}

func TestDraft23RelativeFetch(t *testing.T) {
	// a -23 FETCH names an absolute range, where earlier drafts resolve a relative one against Largest Object
	h := draftHarness(t, 22)
	h.o.store("video", 5, objs("a", "b")...)
	var p message.Params
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterRelativeStart, StartGroup: 1})
	for _, tc := range []struct {
		draft   int
		refused bool
	}{{23, true}, {22, false}} {
		r := newRec()
		req, err := h.clientDraft(tc.draft).Open(context.Background(), &message.Fetch{Track: track("video"), Params: p}, r, r)
		if err != nil {
			t.Fatal(err)
		}
		re, refused := response(t, req).(*message.RequestError)
		if refused != tc.refused || (refused && re.Code != message.RequestInvalidRange) {
			t.Errorf("draft %d: a relative FETCH answered %+v", tc.draft, re)
		}
	}
}

func TestDraft23FetchEnds(t *testing.T) {
	// a -23 FETCH reaching Largest Object is answered with it, and an object known to end its group or the track
	// is followed by End of Group or End of Track; earlier drafts state the end in FETCH_OK and get neither marker
	for _, down := range []int{23, 22} {
		h := draftHarness(t, 22)
		cl := h.clientDraft(down)
		req, r := subscribe(t, cl, "video", message.Params{})
		subscribed(t, req)
		h.o.publish(t, "video", 6, objs("key", "delta")...)
		h.o.publish(t, "video", 7, pubObj{id: 0, payload: []byte("last")}, pubObj{id: 1, status: message.StatusEndOfTrack})
		eventually(t, "the live objects", func() bool { return r.count() == 3 })
		freq, fr := fetch(t, cl, "video", fetchRange(6, 0, 9, 0))
		fok, got := fetched(t, freq, fr)
		if !slices.Equal(got, []string{"key", "delta", "last"}) {
			t.Errorf("draft %d: fetched %v", down, got)
		}
		if fok.EndLocation != (wire.Location{Group: 7, Object: 1}) {
			t.Errorf("draft %d: FETCH_OK ends at %v", down, fok.EndLocation)
		}
		fr.mu.Lock()
		var markers []message.RangeEnd
		for _, fo := range fr.fetched {
			if fo.EndOfRange != message.RangeEndNone {
				markers = append(markers, fo.EndOfRange)
				if fo.Object != math.MaxUint64 {
					t.Errorf("draft %d: a group end at %d", down, fo.Object)
				}
			}
		}
		fr.mu.Unlock()
		want := []message.RangeEnd{message.RangeEndGroup, message.RangeEndTrack}
		if down < 23 {
			want = nil
		}
		if !slices.Equal(markers, want) {
			t.Errorf("draft %d: markers %v, want %v", down, markers, want)
		}
	}
}

func TestDraft23UnsupportedProperties(t *testing.T) {
	// a refusal for a Mandatory Track Property names each one the relay does not understand
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	props, _ := wire.AppendKVs(nil, []wire.KV{{Type: message.PropMandatoryMin, Uint: 1}, {Type: message.PropMandatoryMin + 2, Uint: 2}})
	c := h.clientDraftWith(23, nil)
	req, err := c.Open(context.Background(), &message.Publish{
		Track: track("cam"), TrackAlias: c.NewTrackAlias(), TrackProperties: props,
	}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	re, ok := response(t, req).(*message.RequestError)
	if !ok || re.Code != message.RequestUnsupportedExtension ||
		!slices.Equal(re.UnsupportedProperties, []uint64{message.PropMandatoryMin, message.PropMandatoryMin + 2}) {
		t.Errorf("refused with %+v", re)
	}
}

func TestDraft23MandatoryTrackStatus(t *testing.T) {
	// a TRACK_STATUS_OK naming a Mandatory Track Property refuses the relay's own TRACK_STATUS and FETCH
	h := draftHarness(t, 22)
	h.o.props, _ = wire.AppendKVs(nil, []wire.KV{{Type: message.PropMandatoryMin + 4, Uint: 1}})
	h.o.store("video", 0, objs("a")...)
	cl := h.clientDraft(23)
	req, err := cl.Open(context.Background(), &message.TrackStatus{Track: track("video")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	re, ok := response(t, req).(*message.RequestError)
	if !ok || re.Code != message.RequestUnsupportedExtension ||
		!slices.Equal(re.UnsupportedProperties, []uint64{message.PropMandatoryMin + 4}) {
		t.Errorf("TRACK_STATUS refused with %+v", re)
	}
	freq, _ := fetch(t, cl, "video", fetchRange(0, 0, 0, 0))
	refusedWith(t, freq, message.RequestUnsupportedExtension)
}

func TestDraft23PropertyRules(t *testing.T) {
	// a -23 upstream's known property outside its scope, or repeated, makes the track malformed; earlier drafts
	// forward it
	gap := func(t *testing.T) message.Properties {
		p, err := wire.AppendKVs(nil, []wire.KV{{Type: message.PropPriorGroupIDGap, Uint: 1}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, up := range []int{23, 22} {
		t.Run(fmt.Sprintf("track properties from -%d", up), func(t *testing.T) {
			h := draftHarness(t, up)
			h.o.props = gap(t)
			req, _ := subscribe(t, h.clientDraft(23), "video", message.Params{})
			re, refused := response(t, req).(*message.RequestError)
			if refused != (up == 23) || (refused && re.Code != message.RequestMalformedTrack) {
				t.Errorf("SUBSCRIBE answered %+v", re)
			}
		})
		t.Run(fmt.Sprintf("object properties from -%d", up), func(t *testing.T) {
			h := draftHarness(t, up)
			h.o.datagrams = true
			req, r := subscribe(t, h.clientDraft(23), "video", message.Params{})
			subscribed(t, req)
			cache, _ := wire.AppendKVs(nil, []wire.KV{{Type: message.PropMaxCacheDuration, Uint: 5}})
			h.o.publish(t, "video", 0, pubObj{id: 0, payload: []byte("a"), props: cache})
			if up < 23 {
				recv(t, r.datagrams, "the datagram")
				return
			}
			if done, ok := recv(t, r.messages, "PUBLISH_DONE").(*message.PublishDone); !ok ||
				done.Code != message.DoneMalformedTrack {
				t.Fatalf("got %+v", done)
			}
		})
	}
	for _, up := range []int{22, 23} {
		t.Run(fmt.Sprintf("a mandatory property on an object from -%d", up), func(t *testing.T) {
			h := draftHarness(t, up)
			h.o.datagrams = true
			req, r := subscribe(t, h.clientDraft(up), "video", message.Params{})
			subscribed(t, req)
			mand, _ := wire.AppendKVs(nil, []wire.KV{{Type: message.PropMandatoryMin, Uint: 1}})
			h.o.publish(t, "video", 0, pubObj{id: 0, payload: []byte("a"), props: mand})
			if done, ok := recv(t, r.messages, "PUBLISH_DONE").(*message.PublishDone); !ok ||
				done.Code != message.DoneMalformedTrack {
				t.Fatalf("got %+v", done)
			}
		})
	}
	t.Run("a -23 publisher's repeated property", func(t *testing.T) {
		h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
		props, _ := wire.AppendKVs(nil, []wire.KV{{Type: message.PropDynamicGroups, Uint: 1}, {Type: message.PropDynamicGroups, Uint: 1}})
		c := h.clientDraftWith(23, nil)
		req, err := c.Open(context.Background(), &message.Publish{
			Track: track("cam"), TrackAlias: c.NewTrackAlias(), TrackProperties: props,
		}, newRec(), nil)
		if err != nil {
			t.Fatal(err)
		}
		refusedWith(t, req, message.RequestMalformedTrack)
	})
}

func TestPausedUpstreamLargest(t *testing.T) {
	// while every source is paused the relay's Largest Object can be stale, so TRACK_STATUS, FETCH and a paused
	// SUBSCRIBE are answered with one refreshed upstream; a forwarding source's needs no refresh
	h := newHarness(t, relay.Options{}, nil)
	h.o.store("video", 1, objs("a")...)
	cl := h.client()
	var paused message.Params
	paused.SetForward(false)
	req, _ := subscribe(t, cl, "video", paused)
	if ok := subscribed(t, req); ok.Params.LargestObject != (wire.Location{Group: 1}) {
		t.Fatalf("SUBSCRIBE_OK %+v", ok.Params)
	}
	// published while the relay's subscription is paused, so the relay sees none of it
	h.o.publish(t, "video", 4, objs("b", "c")...)
	before := h.o.statuses.Load()
	sreq, err := cl.Open(context.Background(), &message.TrackStatus{Track: track("video")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ok, isOK := response(t, sreq).(*message.RequestOK)
	if !isOK || ok.Params.LargestObject != (wire.Location{Group: 4, Object: 1}) {
		t.Errorf("TRACK_STATUS answered %+v", response(t, sreq))
	}
	if h.o.statuses.Load() != before+1 {
		t.Errorf("the relay asked upstream %d times", h.o.statuses.Load()-before)
	}
	later, lr := subscribe(t, cl, "video", paused)
	if ok := subscribed(t, later); ok.Params.LargestObject != (wire.Location{Group: 4, Object: 1}) {
		t.Errorf("a paused SUBSCRIBE_OK %+v", ok.Params)
	}
	h.o.publish(t, "video", 5, objs("d")...)
	var prio message.Params
	prio.SetSubscriberPriority(3)
	if _, err = later.Update(prio); err != nil {
		t.Fatal(err)
	}
	if ok, isOK := recv(t, lr.messages, "the update's answer").(*message.RequestOK); !isOK ||
		ok.Params.LargestObject != (wire.Location{Group: 5}) {
		t.Errorf("a paused subscription's update answered %+v", ok)
	}
	// once a subscriber forwards, the source does, and the relay's own Largest Object is current
	live, _ := subscribe(t, cl, "video", message.Params{})
	subscribed(t, live)
	before = h.o.statuses.Load()
	freq, _ := fetch(t, cl, "video", fetchRange(4, 0, 4, 1))
	if _, isOK := response(t, freq).(*message.FetchOK); !isOK || h.o.statuses.Load() != before {
		t.Errorf("a forwarding track's FETCH asked upstream %d times", h.o.statuses.Load()-before)
	}
}

type prefixAuth struct{ allowed wire.Namespace }

func (a prefixAuth) Authorize(op relay.Operation, ns wire.Namespace, _ []wire.Token) bool {
	return op == relay.OpPublish || ns.HasPrefix(a.allowed)
}

func announce(t *testing.T, h *harness, prefix wire.Namespace) *session.Request {
	t.Helper()
	req, err := h.session(newOrigin()).Open(context.Background(), &message.PublishNamespace{Prefix: prefix}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, req).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH_NAMESPACE refused")
	}
	return req
}

func nsEvent(t *testing.T, r *rec, what string) string {
	t.Helper()
	switch m := recv(t, r.messages, what).(type) {
	case *message.Namespace:
		return "+" + string(bytes.Join(m.Suffix, []byte("/")))
	case *message.NamespaceDone:
		return "-" + string(bytes.Join(m.Suffix, []byte("/")))
	case *message.RequestOK:
		return "ok"
	case *message.RequestError:
		return fmt.Sprintf("error %#x", uint64(m.Code))
	default:
		return m.Kind().String()
	}
}

func TestSubscribeNamespacePrefixUpdate(t *testing.T) {
	// a new prefix withdraws what it no longer covers before its answer and reports what it newly covers after
	// it, each suffix relative to the prefix in force; what both cover stays reported
	for _, d := range []int{23, 22, 18} {
		t.Run(strconv.Itoa(d), func(t *testing.T) {
			h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
			announce(t, h, ns("live", "a"))
			announce(t, h, ns("news", "c"))
			r := newRec()
			req, err := h.clientDraft(d).Open(context.Background(), &message.SubscribeNamespace{Prefix: ns("live")}, r, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := response(t, req).(*message.RequestOK); !ok {
				t.Fatal("SUBSCRIBE_NAMESPACE refused")
			}
			step := func(prefix wire.Namespace, want ...string) {
				t.Helper()
				var p message.Params
				p.SetTrackNamespacePrefix(prefix)
				if _, err := req.Update(p); err != nil {
					t.Fatal(err)
				}
				for i, w := range want {
					if got := nsEvent(t, r, w); got != w {
						t.Fatalf("event %d after the update to %q: %s, want %s", i, prefix, got, w)
					}
				}
			}
			if got := nsEvent(t, r, "NAMESPACE"); got != "+a" {
				t.Fatalf("got %s", got)
			}
			step(ns(), "ok", "+news/c")
			step(ns("news"), "-live/a", "ok")
			announce(t, h, ns("news", "d"))
			if got := nsEvent(t, r, "NAMESPACE"); got != "+d" {
				t.Errorf("a namespace announced after the update is relative to it: %s", got)
			}
		})
	}
}

func TestSubscribeNamespacePrefixUpdateRefused(t *testing.T) {
	// an update to a prefix the session may not see, or one overlapping another request through -22, is refused,
	// and the refusal ends the request
	h := ingestHarness(t, relay.Access{Publish: true, Authorizer: prefixAuth{allowed: ns("live")}}, relay.Options{})
	open := func(c *session.Session, prefix wire.Namespace) (*session.Request, *rec) {
		t.Helper()
		r := newRec()
		req, err := c.Open(context.Background(), &message.SubscribeNamespace{Prefix: prefix}, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := response(t, req).(*message.RequestOK); !ok {
			t.Fatal("SUBSCRIBE_NAMESPACE refused")
		}
		return req, r
	}
	update := func(req *session.Request, r *rec, prefix wire.Namespace, want string) {
		t.Helper()
		var p message.Params
		p.SetTrackNamespacePrefix(prefix)
		if _, err := req.Update(p); err != nil {
			t.Fatal(err)
		}
		if got := nsEvent(t, r, want); got != want {
			t.Errorf("update to %q answered %s, want %s", prefix, got, want)
		}
	}
	c22 := h.clientDraft(22)
	open(c22, ns("live", "x"))
	req, r := open(c22, ns("live", "y"))
	update(req, r, ns("live"), fmt.Sprintf("error %#x", uint64(message.RequestPrefixOverlap)))
	recv(t, r.closes, "the refused request's end")
	req, r = open(c22, ns("live", "z"))
	update(req, r, ns("news"), fmt.Sprintf("error %#x", uint64(message.RequestUnauthorized)))
	c23 := h.clientDraft(23)
	open(c23, ns("live", "x"))
	req, r = open(c23, ns("live", "y"))
	update(req, r, ns("live"), "ok")
}

func TestSubscribeTracksPrefixUpdate(t *testing.T) {
	// a SUBSCRIBE_TRACKS's new prefix offers the tracks under it, never one already offered for the request
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	published(t, h.client(), "video")
	sink := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	viewer := h.clientDraftWith(23, sink)
	strec := newRec()
	st, err := viewer.Open(context.Background(), &message.SubscribeTracks{Prefix: ns("live")}, strec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, st).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	recv(t, sink.pubs, "the first PUBLISH")
	for _, prefix := range []wire.Namespace{ns("news"), ns("live")} {
		var p message.Params
		p.SetTrackNamespacePrefix(prefix)
		if _, err = st.Update(p); err != nil {
			t.Fatal(err)
		}
		if m := recv(t, strec.messages, "the update's answer"); m.Kind() != message.KindRequestOK {
			t.Fatalf("update answered %s", m.Kind())
		}
	}
	published(t, h.client(), "audio")
	if pm := recv(t, sink.pubs, "the new track's PUBLISH").Message().(*message.Publish); string(pm.Track.Name) != "audio" {
		t.Errorf("offered %s again", pm.Track.Name)
	}
	select {
	case again := <-sink.pubs:
		t.Errorf("a second PUBLISH for %s", again.Message().(*message.Publish).Track.Name)
	case <-time.After(50 * time.Millisecond):
	}
}

func objectIDFilter(lo, hi uint64) message.Params {
	var p message.Params
	p.RangeFilters = []message.RangeFilter{{Type: message.ParamObjectIDFilter, Ranges: []wire.Range{{Start: lo, End: hi}}}}
	return p
}

func TestUpstreamGroupEndsAfterFilteredObjects(t *testing.T) {
	// an upstream End of Group refers to its prior object, so it is forwarded only after that object was; one after
	// an object the subscriber's filters drop would end the wrong object, or no object at all
	for _, order := range []message.GroupOrder{message.GroupOrderAscending, message.GroupOrderDescending} {
		for _, tc := range []struct {
			name   string
			lo, hi uint64
			want   []string
		}{
			// group 5 holds only Object 2, which the filter drops, so its End of Group is not forwarded
			{"a fully filtered group", 0, 1, []string{"g6-o0", "g6-o1", "end"}},
			// Object 1 of group 6 is dropped, so Object 0 does not end its group
			{"a filtered last object", 0, 0, []string{"g6-o0"}},
		} {
			t.Run(fmt.Sprintf("%s order %d", tc.name, order), func(t *testing.T) {
				h := draftHarness(t, 23)
				h.o.endGroups = true
				h.o.store("video", 5, pubObj{id: 2, payload: []byte("g5-o2")})
				h.o.store("video", 6, pubObj{id: 0, payload: []byte("g6-o0")}, pubObj{id: 1, payload: []byte("g6-o1")})
				p := objectIDFilter(tc.lo, tc.hi)
				f := message.LocationFilter{Type: message.FilterAbsoluteRange, StartGroup: 5, EndGroupDelta: 1, EndObject: 1}
				p.SetLocationFilter(f)
				p.SetGroupOrder(order)
				freq, fr := fetch(t, h.clientDraft(23), "video", p)
				fetched(t, freq, fr)
				fr.mu.Lock()
				defer fr.mu.Unlock()
				if fr.fetchErr != nil {
					t.Fatalf("the fetch was reset: %v", fr.fetchErr)
				}
				var got []string
				for i, fo := range fr.fetched {
					switch fo.EndOfRange {
					case message.RangeEndNone:
						got = append(got, fr.payloads[i])
					case message.RangeEndGroup, message.RangeEndTrack:
						got = append(got, "end")
					}
				}
				if !slices.Equal(got, tc.want) {
					t.Errorf("fetched %v, want %v", got, tc.want)
				}
			})
		}
	}
}

func TestUpstreamFetchUnsupportedProperty(t *testing.T) {
	// a gap's FETCH_OK naming a Mandatory Track Property, or malformed track properties, resets a fetch that has
	// already delivered its cached groups, rather than leaving an unknown range in it
	mand, _ := wire.AppendKVs(nil, []wire.KV{{Type: message.PropMandatoryMin, Uint: 1}})
	gap, _ := wire.AppendKVs(nil, []wire.KV{{Type: message.PropPriorGroupIDGap, Uint: 1}})
	for _, tc := range []struct {
		name  string
		props message.Properties
	}{{"unsupported", mand}, {"malformed", gap}} {
		t.Run(tc.name, func(t *testing.T) {
			h := draftHarness(t, 23)
			h.o.store("video", 4, objs("old")...)
			cl := h.clientDraft(23)
			req, r := subscribe(t, cl, "video", message.Params{})
			subscribed(t, req)
			h.o.publish(t, "video", 5, objs("live")...)
			eventually(t, "the live object", func() bool { return r.count() == 1 })
			h.o.fetchProps = tc.props
			p := fetchRange(4, 0, 5, 0)
			p.SetGroupOrder(message.GroupOrderDescending)
			freq, fr := fetch(t, cl, "video", p)
			_, got := fetched(t, freq, fr)
			fr.mu.Lock()
			defer fr.mu.Unlock()
			if fr.fetchErr == nil || !slices.Equal(got, []string{"live"}) {
				t.Errorf("fetched %v, reset by %v", got, fr.fetchErr)
			}
		})
	}
}

type statusAnswer struct {
	largest wire.Location
	props   message.Properties
	silent  bool
	refuse  atomic.Bool
	asked   chan struct{}
}

func (a *statusAnswer) HandleGoAway(*session.Session, *message.GoAway) {}

func (a *statusAnswer) HandleRequest(r *session.Request) {
	r.SetHandlers(newRec(), nil)
	if _, ok := r.Message().(*message.TrackStatus); !ok {
		_ = r.Reject(message.RequestNotSupported, "", 0)
		return
	}
	select {
	case a.asked <- struct{}{}:
	default:
	}
	switch {
	case a.silent:
		return
	case a.refuse.Load():
		_ = r.Reject(message.RequestNotSupported, "", 0)
		return
	}
	ok := &message.RequestOK{TrackProperties: a.props}
	ok.Params.SetLargestObject(a.largest)
	_ = r.Send(ok)
	_ = r.CloseSend()
}

func boundedPublisher(t *testing.T, h *harness, a *statusAnswer) *session.Request {
	t.Helper()
	// a forwarding PUBLISH whose LOCATION_FILTER ends at group 0, so it delivers nothing newer
	var p message.Params
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteStartGroupEnd})
	req, _ := startPublish(t, h.session(a), "video", p, nil)
	if _, ok := response(t, req).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH refused")
	}
	sendGroup(t, req, 0, objs("a")...)
	return req
}

func trackStatus(t *testing.T, c *session.Session) message.Message {
	t.Helper()
	req, err := c.Open(context.Background(), &message.TrackStatus{Track: track("video")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return response(t, req)
}

func TestBoundedSourceLargest(t *testing.T) {
	// a source whose filter has ended delivers nothing newer, so the relay asks for the track's Largest Object
	// rather than answering with the last it received
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	a := &statusAnswer{largest: wire.Location{Group: 3}, asked: make(chan struct{}, 4)}
	boundedPublisher(t, h, a)
	eventually(t, "the bounded group", func() bool {
		ok, isOK := trackStatus(t, h.clientDraft(22)).(*message.RequestOK)
		return isOK && ok.Params.Has(message.ParamLargestObject)
	})
	ok, isOK := trackStatus(t, h.clientDraft(23)).(*message.RequestOK)
	if !isOK || ok.Params.LargestObject != (wire.Location{Group: 3}) {
		t.Errorf("TRACK_STATUS answered %+v", ok)
	}
}

func TestRefreshFailures(t *testing.T) {
	t.Run("an unsupported property refuses every waiter", func(t *testing.T) {
		h := draftHarness(t, 22)
		h.o.store("video", 1, objs("a")...)
		var paused message.Params
		paused.SetForward(false)
		req, _ := subscribe(t, h.client(), "video", paused)
		subscribed(t, req)
		h.o.props, _ = wire.AppendKVs(nil, []wire.KV{{Type: message.PropMandatoryMin, Uint: 1}})
		answers := make(chan message.Message, 2)
		for _, d := range []int{23, 22} {
			c := h.clientDraft(d)
			go func() { answers <- trackStatus(t, c) }()
		}
		for range 2 {
			if re, ok := recv(t, answers, "an answer").(*message.RequestError); !ok ||
				re.Code != message.RequestUnsupportedExtension {
				t.Errorf("TRACK_STATUS answered %+v", re)
			}
		}
	})
	t.Run("a refused TRACK_STATUS unpauses the source for a moment", func(t *testing.T) {
		h := draftHarness(t, 22)
		h.o.store("video", 1, objs("a")...)
		var paused message.Params
		paused.SetForward(false)
		req, _ := subscribe(t, h.client(), "video", paused)
		subscribed(t, req)
		h.o.statusRefuse = message.RequestNotSupported
		h.o.store("video", 4, objs("b")...)
		ok, isOK := trackStatus(t, h.clientDraft(23)).(*message.RequestOK)
		if !isOK || ok.Params.LargestObject != (wire.Location{Group: 4}) {
			t.Errorf("TRACK_STATUS answered %+v", ok)
		}
		eventually(t, "the source paused again", func() bool { return len(h.o.subscriptions("video")) == 0 })
	})
	t.Run("an unanswered refresh is a timeout for -23 and the relay's value for -22", func(t *testing.T) {
		h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
		a := &statusAnswer{silent: true, asked: make(chan struct{}, 4)}
		boundedPublisher(t, h, a)
		answers := make(chan message.Message, 3)
		for _, d := range []int{23, 23, 22} {
			c := h.clientDraft(d)
			go func() { answers <- trackStatus(t, c) }()
		}
		var refused, answered int
		for range 3 {
			switch m := recv(t, answers, "an answer").(type) {
			case *message.RequestError:
				if m.Code == message.RequestTimeout {
					refused++
				}
			case *message.RequestOK:
				if m.Params.LargestObject == (wire.Location{}) {
					answered++
				}
			}
		}
		if refused != 2 || answered != 1 || len(a.asked) != 1 {
			t.Errorf("%d timed out, %d answered, %d asked upstream", refused, answered, len(a.asked))
		}
	})
}

func pausedPublisher(t *testing.T, h *harness, a *statusAnswer) (*session.Request, *rec) {
	t.Helper()
	var p message.Params
	p.SetForward(false)
	p.SetLargestObject(wire.Location{Group: 1})
	req, r := startPublish(t, h.session(a), "video", p, nil)
	if _, ok := response(t, req).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH refused")
	}
	return req, r
}

func forwardUpdate(t *testing.T, r *rec, forward bool) {
	t.Helper()
	u, ok := recv(t, r.messages, "a FORWARD update").(*message.RequestUpdate)
	if !ok || !u.Params.Has(message.ParamForward) || u.Params.Forward != forward {
		t.Fatalf("got %+v, want FORWARD %v", u, forward)
	}
}

func answerUpdate(t *testing.T, pub *session.Request, largest wire.Location) {
	t.Helper()
	ok := &message.RequestOK{}
	ok.Params.SetLargestObject(largest)
	if err := pub.Send(ok); err != nil {
		t.Fatal(err)
	}
}

func TestProbedLargest(t *testing.T) {
	t.Run("an unanswered resume is a timeout for every waiter", func(t *testing.T) {
		h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
		a := &statusAnswer{asked: make(chan struct{}, 16)}
		a.refuse.Store(true)
		_, prec := pausedPublisher(t, h, a)
		first, second := h.clientDraft(23), h.clientDraft(23)
		answers := make(chan message.Message, 2)
		go func() { answers <- trackStatus(t, first) }()
		forwardUpdate(t, prec, true)
		// sent while the resume awaits its answer, which this waits for too
		go func() { answers <- trackStatus(t, second) }()
		for range 2 {
			if re, ok := recv(t, answers, "an answer").(*message.RequestError); !ok || re.Code != message.RequestTimeout {
				t.Errorf("TRACK_STATUS answered %+v", re)
			}
		}
	})
	t.Run("a probed publisher is paused again", func(t *testing.T) {
		h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
		a := &statusAnswer{asked: make(chan struct{}, 16)}
		a.refuse.Store(true)
		pub, prec := pausedPublisher(t, h, a)
		c := h.clientDraft(23)
		answers := make(chan message.Message, 1)
		go func() { answers <- trackStatus(t, c) }()
		forwardUpdate(t, prec, true)
		answerUpdate(t, pub, wire.Location{Group: 4})
		if ok, isOK := recv(t, answers, "the answer").(*message.RequestOK); !isOK ||
			ok.Params.LargestObject != (wire.Location{Group: 4}) {
			t.Errorf("TRACK_STATUS answered %+v", ok)
		}
		forwardUpdate(t, prec, false)
	})
	t.Run("a subscriber arriving during a probe keeps the publisher forwarding", func(t *testing.T) {
		h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
		a := &statusAnswer{asked: make(chan struct{}, 16)}
		a.refuse.Store(true)
		pub, prec := pausedPublisher(t, h, a)
		c := h.clientDraft(23)
		answers := make(chan message.Message, 1)
		go func() { answers <- trackStatus(t, c) }()
		forwardUpdate(t, prec, true)
		sreq, _ := subscribe(t, c, "video", message.Params{})
		// the subscriber is most likely held by now; were the probe to end first, the relay pauses and resumes
		time.Sleep(50 * time.Millisecond)
		var last atomic.Bool
		last.Store(true)
		go func() {
			for m := range prec.messages {
				if u, ok := m.(*message.RequestUpdate); ok {
					last.Store(u.Params.Forward)
					_ = pub.Send(&message.RequestOK{})
				}
			}
		}()
		answerUpdate(t, pub, wire.Location{Group: 4})
		subscribed(t, sreq)
		recv(t, answers, "the answer")
		sreq.Cancel(message.ResetCancelled)
		time.Sleep(100 * time.Millisecond)
		if !last.Load() {
			t.Error("the publisher was paused")
		}
	})
}

func TestFutureStartSourceLargest(t *testing.T) {
	// a source whose filter starts past the relay's Largest Object misses what is published before that start, so
	// the relay asks upstream until an object at the start arrives
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	a := &statusAnswer{largest: wire.Location{Group: 7}, asked: make(chan struct{}, 64)}
	var p message.Params
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteStart, StartGroup: 10})
	p.SetLargestObject(wire.Location{Group: 1})
	pub, _ := startPublish(t, h.session(a), "video", p, nil)
	if _, ok := response(t, pub).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH refused")
	}
	for _, d := range []int{23, 22} {
		if ok, isOK := trackStatus(t, h.clientDraft(d)).(*message.RequestOK); !isOK ||
			ok.Params.LargestObject != (wire.Location{Group: 7}) {
			t.Errorf("-%d TRACK_STATUS answered %+v", d, ok)
		}
	}
	sendGroup(t, pub, 10, objs("a")...)
	c := h.clientDraft(23)
	eventually(t, "group 10", func() bool {
		ok, isOK := trackStatus(t, c).(*message.RequestOK)
		return isOK && ok.Params.LargestObject == (wire.Location{Group: 10})
	})
	asked := len(a.asked)
	if ok, isOK := trackStatus(t, c).(*message.RequestOK); !isOK || len(a.asked) != asked {
		t.Errorf("TRACK_STATUS answered %+v after asking upstream again", ok)
	}
}

func viewTracks(t *testing.T, h *harness, draft int) (*tracksSink, *rec) {
	t.Helper()
	return viewTracksConn(t, h, draft, nil, message.Params{})
}

func viewTracksConn(t *testing.T, h *harness, draft int, wrapServer func(*memconn.Conn) session.Conn,
	p message.Params,
) (*tracksSink, *rec) {
	t.Helper()
	sink := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	strec := newRec()
	st, err := h.clientDraftConn(draft, sink, wrapServer).Open(context.Background(),
		&message.SubscribeTracks{Prefix: ns("live"), Params: p}, strec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, st).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	return sink, strec
}

func skippedOnly(t *testing.T, sink *tracksSink, strec *rec) {
	t.Helper()
	if sk, ok := recv(t, strec.messages, "PUBLISH_SKIPPED").(*message.PublishSkipped); !ok || string(sk.TrackName) != "video" {
		t.Fatalf("got %+v", sk)
	}
	select {
	case p := <-sink.pubs:
		t.Errorf("offered %+v", p.Message())
	case m := <-strec.messages:
		t.Errorf("then got %+v", m)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDraft23FreshLargestEverywhere(t *testing.T) {
	// SUBSCRIBE_OK, an update's answer and PUBLISH carry Largest Object too, so for -23 they fail when it cannot be
	// refreshed, while through -22 the relay's own value stands
	var paused message.Params
	paused.SetForward(false)
	setup := func(t *testing.T) (*harness, *session.Request, *rec) {
		// a paused -23 subscription answered from TRACK_STATUS, after which the publisher refuses TRACK_STATUS and
		// never answers a resume
		t.Parallel()
		h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
		a := &statusAnswer{largest: wire.Location{Group: 2}, asked: make(chan struct{}, 16)}
		pausedPublisher(t, h, a)
		req, r := subscribe(t, h.clientDraft(23), "video", paused)
		if ok := subscribed(t, req); ok.Params.LargestObject != (wire.Location{Group: 2}) {
			t.Fatalf("SUBSCRIBE_OK %+v", ok.Params)
		}
		a.refuse.Store(true)
		return h, req, r
	}
	t.Run("SUBSCRIBE", func(t *testing.T) {
		h, _, _ := setup(t)
		late, _ := subscribe(t, h.clientDraft(23), "video", paused)
		refusedWith(t, late, message.RequestTimeout)
		old, _ := subscribe(t, h.clientDraft(22), "video", paused)
		if ok := subscribed(t, old); ok.Params.LargestObject != (wire.Location{Group: 2}) {
			t.Errorf("-22 SUBSCRIBE_OK %+v", ok.Params)
		}
	})
	t.Run("REQUEST_UPDATE", func(t *testing.T) {
		_, req, r := setup(t)
		var prio message.Params
		prio.SetSubscriberPriority(3)
		if _, err := req.Update(prio); err != nil {
			t.Fatal(err)
		}
		if re, ok := recv(t, r.messages, "the update's answer").(*message.RequestError); !ok ||
			re.Code != message.RequestTimeout {
			t.Errorf("the update was answered %+v", re)
		}
		if done, ok := recv(t, r.messages, "PUBLISH_DONE").(*message.PublishDone); !ok ||
			done.Code != message.DoneUpdateFailed {
			t.Errorf("the subscription ended with %+v", done)
		}
	})
	t.Run("PUBLISH", func(t *testing.T) {
		h, _, _ := setup(t)
		sink, strec := viewTracks(t, h, 23)
		skippedOnly(t, sink, strec)
	})
}

func TestUnsupportedRefreshSkipsPublish(t *testing.T) {
	// a track the relay may not process is skipped rather than offered, and is not offered again
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	a := &statusAnswer{largest: wire.Location{Group: 2}, asked: make(chan struct{}, 16)}
	a.props, _ = wire.AppendKVs(nil, []wire.KV{{Type: message.PropMandatoryMin, Uint: 1}})
	pausedPublisher(t, h, a)
	sink, strec := viewTracks(t, h, 22)
	skippedOnly(t, sink, strec)
}

func TestResumeFilterFromFreshLargest(t *testing.T) {
	// a resume's relative filter is resolved against the Largest Object the upstream's answer to the relay's resume
	// brings, which the update's answer and its fill share, so a late object before that start is not forwarded
	for name, f := range map[string]message.LocationFilter{
		"next object":          {Type: message.FilterNextObject},
		"relative start, next": {Type: message.FilterRelativeStart},
	} {
		t.Run(name, func(t *testing.T) {
			h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
			a := &statusAnswer{largest: wire.Location{Group: 1}, asked: make(chan struct{}, 16)}
			pub, prec := pausedPublisher(t, h, a)
			sendGroup(t, pub, 1, objs("a")...)
			var paused message.Params
			paused.SetForward(false)
			req, r := subscribe(t, h.clientDraft(23), "video", paused)
			subscribed(t, req)
			var fill, resume message.Params
			fill.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteStart})
			resume.SetForward(true)
			resume.SetLocationFilter(f)
			resume.SetFill(&fill)
			if _, err := req.Update(resume); err != nil {
				t.Fatal(err)
			}
			forwardUpdate(t, prec, true)
			// the publisher reached group 4 while paused
			answerUpdate(t, pub, wire.Location{Group: 4})
			if ok, isOK := recv(t, r.messages, "the update's answer").(*message.RequestOK); !isOK ||
				ok.Params.LargestObject != (wire.Location{Group: 4}) {
				t.Fatalf("the update was answered %+v", ok)
			}
			sendGroup(t, pub, 3, objs("late")...)
			sendGroup(t, pub, 5, objs("live")...)
			eventually(t, "group 5", func() bool { return len(r.received()) > 0 })
			if got := r.received(); len(got) != 1 || got[0].payload != "live" {
				t.Errorf("forwarded %+v", got)
			}
			recv(t, r.fetchDone, "the fill")
			r.mu.Lock()
			defer r.mu.Unlock()
			if slices.Contains(r.payloads, "live") || !slices.Contains(r.payloads, "a") {
				t.Errorf("filled %v", r.payloads)
			}
		})
	}
}

func recordForward(pub *session.Request, prec *rec, largest wire.Location) <-chan bool {
	// answers every update to pub, reporting the FORWARD state each asks for
	seen := make(chan bool, 16)
	go func() {
		for m := range prec.messages {
			if u, ok := m.(*message.RequestUpdate); ok {
				seen <- u.Params.Forward
				ok := &message.RequestOK{}
				ok.Params.SetLargestObject(largest)
				_ = pub.Send(ok)
			}
		}
	}()
	return seen
}

type slowOpen struct{ *memconn.Conn }

func (c slowOpen) OpenStreamSync(ctx context.Context) (session.Stream, error) {
	// the relay's PUBLISH to this viewer opens late
	time.Sleep(100 * time.Millisecond)
	return c.Conn.OpenStreamSync(ctx)
}

type gatedOpen struct {
	*memconn.Conn
	opening chan struct{}
	gate    chan struct{}
}

func (c gatedOpen) OpenStreamSync(ctx context.Context) (session.Stream, error) {
	// the relay's PUBLISH to this viewer opens once the gate does
	c.opening <- struct{}{}
	<-c.gate
	return c.Conn.OpenStreamSync(ctx)
}

func TestOfferSnapshotThroughOpen(t *testing.T) {
	// an offer's subscription starts at the Largest Object its PUBLISH advertises, so groups finished while the
	// PUBLISH opens are forwarded live, once each, and the fill ends at that Largest Object
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	a := &statusAnswer{asked: make(chan struct{}, 16)}
	a.refuse.Store(true)
	pub, prec := pausedPublisher(t, h, a)
	sendGroup(t, pub, 1, objs("a")...)
	recordForward(pub, prec, wire.Location{Group: 4})
	g := gatedOpen{opening: make(chan struct{}, 4), gate: make(chan struct{})}
	var fill, p message.Params
	fill.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteStart})
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterNextObject})
	p.SetFill(&fill)
	sink, _ := viewTracksConn(t, h, 23, func(c *memconn.Conn) session.Conn { g.Conn = c; return g }, p)
	recv(t, g.opening, "the PUBLISH opening")
	sendGroup(t, pub, 5, objs("g5")...)
	sendGroup(t, pub, 6, objs("g6")...)
	c := h.clientDraft(22)
	eventually(t, "group 6", func() bool {
		ok, isOK := trackStatus(t, c).(*message.RequestOK)
		return isOK && ok.Params.LargestObject == (wire.Location{Group: 6})
	})
	close(g.gate)
	pm := recv(t, sink.pubs, "the relay's PUBLISH").Message().(*message.Publish)
	if pm.Params.LargestObject != (wire.Location{Group: 4}) {
		t.Errorf("the PUBLISH carries %+v", pm.Params)
	}
	eventually(t, "groups 5 and 6", func() bool { return len(sink.rec.received()) >= 2 })
	recv(t, sink.rec.fetchDone, "the fill")
	time.Sleep(50 * time.Millisecond)
	if got := sink.rec.received(); len(got) != 2 || got[0].payload != "g5" || got[1].payload != "g6" {
		t.Errorf("forwarded %+v", got)
	}
	sink.rec.mu.Lock()
	defer sink.rec.mu.Unlock()
	var filled []string
	for i, fo := range sink.rec.fetched {
		if fo.EndOfRange == message.RangeEndNone {
			filled = append(filled, sink.rec.payloads[i])
		}
	}
	if !slices.Equal(filled, []string{"a"}) {
		t.Errorf("filled %v", filled)
	}
}

func TestOfferHoldsForwarding(t *testing.T) {
	t.Run("a forwarding offer resumes the source and keeps it resumed", func(t *testing.T) {
		t.Parallel()
		h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
		a := &statusAnswer{asked: make(chan struct{}, 16)}
		a.refuse.Store(true)
		pub, prec := pausedPublisher(t, h, a)
		seen := recordForward(pub, prec, wire.Location{Group: 4})
		sink, _ := viewTracksConn(t, h, 23, func(c *memconn.Conn) session.Conn { return slowOpen{c} }, message.Params{})
		pm := recv(t, sink.pubs, "the relay's PUBLISH").Message().(*message.Publish)
		if pm.Params.LargestObject != (wire.Location{Group: 4}) {
			t.Errorf("the PUBLISH carries %+v", pm.Params)
		}
		if !recv(t, seen, "the resume") {
			t.Fatal("paused first")
		}
		select {
		case fwd := <-seen:
			t.Errorf("then FORWARD %v", fwd)
		case <-time.After(100 * time.Millisecond):
		}
	})
	t.Run("a failed offer lets the source pause again", func(t *testing.T) {
		t.Parallel()
		h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
		a := &statusAnswer{asked: make(chan struct{}, 16)}
		a.refuse.Store(true)
		_, prec := pausedPublisher(t, h, a)
		sink, strec := viewTracks(t, h, 23)
		forwardUpdate(t, prec, true)
		skippedOnly(t, sink, strec)
		forwardUpdate(t, prec, false)
	})
}

func TestOfferEndedWhileOpening(t *testing.T) {
	// a track that turns malformed while its PUBLISH opens ends that subscription once the PUBLISH is sent
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	a := &statusAnswer{asked: make(chan struct{}, 16)}
	a.refuse.Store(true)
	pub, prec := pausedPublisher(t, h, a)
	recordForward(pub, prec, wire.Location{Group: 4})
	g := gatedOpen{opening: make(chan struct{}, 4), gate: make(chan struct{})}
	sink, _ := viewTracksConn(t, h, 23, func(c *memconn.Conn) session.Conn { g.Conn = c; return g }, message.Params{})
	recv(t, g.opening, "the PUBLISH opening")
	mandatory, _ := wire.AppendKVs(nil, []wire.KV{{Type: message.PropMandatoryMin, Uint: 1}})
	d := &message.Datagram{Group: 5, Priority: 1, Payload: []byte("bad"), Properties: mandatory}
	if err := pub.SendDatagram(d); err != nil {
		t.Fatal(err)
	}
	c := h.clientDraft(23)
	// the malformed track is dropped, its publisher's subscription cancelled
	eventually(t, "the malformed track", func() bool {
		_, refused := trackStatus(t, c).(*message.RequestError)
		return refused
	})
	close(g.gate)
	recv(t, sink.pubs, "the relay's PUBLISH")
	if done, ok := recv(t, sink.rec.messages, "PUBLISH_DONE").(*message.PublishDone); !ok ||
		done.Code != message.DoneMalformedTrack {
		t.Errorf("got %+v", done)
	}
}
