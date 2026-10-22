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

package codec

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func mustCodec(t testing.TB, draft int) *Codec {
	t.Helper()
	c, ok := ForDraft(draft)
	if !ok {
		t.Fatalf("draft %d not supported", draft)
	}
	return c
}

func ns(fields ...string) wire.Namespace {
	out := make(wire.Namespace, len(fields))
	for i, f := range fields {
		out[i] = []byte(f)
	}
	return out
}

func track(name string, fields ...string) message.FullTrackName {
	return message.FullTrackName{Namespace: ns(fields...), Name: []byte(name)}
}

func roundTrip(t *testing.T, c *Codec, m message.Message) message.Message {
	t.Helper()
	// encode, decode back through stream framing, and require the re-encoding to match
	b, err := c.AppendMessage(nil, m)
	if err != nil {
		t.Fatalf("%s: encode: %v", m.Kind(), err)
	}
	typ, body, err := wire.ReadMessage(bufio.NewReader(bytes.NewReader(b)), nil)
	if err != nil {
		t.Fatalf("%s: frame: %v", m.Kind(), err)
	}
	got, err := c.ParseMessage(typ, body)
	if err != nil {
		t.Fatalf("%s: decode: %v", m.Kind(), err)
	}
	if got.Kind() != m.Kind() {
		t.Fatalf("decoded %s, want %s", got.Kind(), m.Kind())
	}
	again, err := c.AppendMessage(nil, got)
	if err != nil {
		t.Fatalf("%s: re-encode: %v", m.Kind(), err)
	}
	if !bytes.Equal(b, again) {
		t.Fatalf("%s: round trip changed the encoding\n%x\n%x", m.Kind(), b, again)
	}
	return got
}

func fullParams() message.Params {
	var p message.Params
	p.SetObjectDeliveryTimeout(5000)
	p.SetSubgroupDeliveryTimeout(1 << 30)
	p.SetRendezvousTimeout(250)
	p.SetForward(true)
	p.SetSubscriberPriority(200)
	p.SetLocationFilter(message.LocationFilter{
		Type: message.FilterAbsoluteRange, StartGroup: 1 << 20,
		StartObject: 3, EndGroupDelta: 9, EndObject: 300,
	})
	p.SetGroupOrder(message.GroupOrderDescending)
	p.SetNewGroupRequest(42)
	p.SetIncludeProperties(false)
	fill := &message.Params{}
	fill.SetFillTimeout(100)
	fill.SetLocationFilter(message.LocationFilter{Type: message.FilterRelativeStart, StartGroup: 1})
	fill.SetGroupOrder(message.GroupOrderAscending)
	p.SetFill(fill)
	p.AuthTokens = []wire.Token{
		{AliasType: wire.TokenRegister, Alias: 1, Type: 0, Value: []byte("token-one")},
		{AliasType: wire.TokenUseAlias, Alias: 1},
	}
	p.RangeFilters = []message.RangeFilter{
		{Type: message.ParamSubgroupFilter, SetID: 0, Ranges: []wire.Range{{Start: 0, End: 2}}},
		{
			Type: message.ParamObjectPropertyFilter, SetID: 1, PropertyType: 0x10,
			Ranges: []wire.Range{{Start: 5, Open: true}},
		},
		{Type: message.ParamPriorityFilter, SetID: 1, Ranges: []wire.Range{{Start: 0, End: 64}}},
	}
	return p
}

func paramsFor(c *Codec, p message.Params) message.Params {
	// drops what a draft cannot carry and respells a filter it cannot express, so one fixture serves every draft
	p.Set &= c.KnownParams()
	if !c.Has(CapRangeFilters) {
		p.RangeFilters = nil
	}
	if !c.Has(CapFillFetch) {
		p.Fill = nil
	}
	if c.Has(CapLegacyLocationFilter) {
		switch f := &p.LocationFilter; f.Type {
		case message.FilterAbsoluteRange:
			f.Type, f.EndObject = message.FilterAbsoluteStartGroupEnd, 0
		case message.FilterRelativeStart:
			f.StartGroup = 0
		}
	}
	return p
}

func publishOKParams() message.Params {
	var p message.Params
	p.SetForward(true)
	p.SetGroupOrder(message.GroupOrderAscending)
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterNextObject})
	p.SetExpires(1000)
	return p
}

func TestRoundTripEveryMessage(t *testing.T) {
	for _, draft := range Drafts() {
		c := mustCodec(t, draft)
		t.Run(c.ALPN(), func(t *testing.T) {
			sub := paramsFor(c, fullParams())
			props := message.Properties{0x04, 0xc0, 0x27, 0x10}
			var ok message.Params
			ok.SetExpires(60000)
			ok.SetLargestObject(wire.Location{Group: 7, Object: 99})
			var upd message.Params
			upd.SetForward(false)
			upd.SetTrackNamespacePrefix(ns("live", "news"))
			upd.RangeFilters = []message.RangeFilter{{Type: message.ParamTrackPropertyFilter, Remove: true}}
			upd = paramsFor(c, upd)
			var notify message.Params
			notify.SetLargestObject(wire.Location{Group: 1})
			notify.SetLocationFilter(message.LocationFilter{Type: message.FilterNextObject})
			var fetch message.Params
			fetch.SetFillTimeout(0)
			fetch.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteStart, StartGroup: 4})
			if c.Has(CapJoiningFetch) {
				// a FETCH of -19 and earlier carries a bounded range in the message
				fetch.SetLocationFilter(message.LocationFilter{
					Type: message.FilterAbsoluteStartGroupEnd, StartGroup: 4, EndGroupDelta: 2,
				})
			}
			fetch = paramsFor(c, fetch)
			var options uint64
			if !c.Has(CapSubscribeTracks) {
				options = message.SubscribeOptionBoth
			}
			msgs := []message.Message{
				&message.Setup{
					Path: "/live?k=v", HasPath: true, Authority: "relay.example.com:443", HasAuthority: true,
					AuthTokens:            []wire.Token{{AliasType: wire.TokenUseValue, Type: 2, Value: []byte("jwt")}},
					MaxAuthTokenCacheSize: 4096, Implementation: "trickster/test", MaxFilterRanges: 16,
					MaxRequestUpdates: 8, Unknown: []wire.KV{{Type: 0x9d, Uint: 1}},
				},
				&message.GoAway{NewSessionURI: "moqt://other.example.com/live", Timeout: 30000},
				&message.RequestOK{Params: ok, TrackProperties: props},
				&message.RequestError{Code: message.RequestDoesNotExist, RetryInterval: 1, Reason: "no such track"},
				&message.RequestError{
					Code: message.RequestRedirect, Reason: "moved",
					Redirect: &message.Redirect{ConnectURI: "moqt://b.example.com", Track: track("v2", "live")},
				},
				&message.RequestUpdate{RequestID: 4, Params: upd},
				&message.Subscribe{RequestID: 2, Track: track("video", "live", "sports"), Params: sub},
				&message.SubscribeOK{TrackAlias: 7, Params: ok, TrackProperties: props},
				&message.Publish{
					RequestID: 3, Track: track("audio", "live"), TrackAlias: 9, Params: message.Params{},
					TrackProperties: props,
				},
				&message.PublishDone{Code: message.DoneTrackEnded, StreamCount: 12, Reason: "done"},
				&message.PublishStateNotify{Params: notify},
				&message.Fetch{RequestID: 6, Track: track("video", "live"), Params: fetch},
				&message.PublishOK{Params: paramsFor(c, publishOKParams())},
				&message.FetchOK{
					EndOfTrack: true, EndLocation: wire.Location{Group: 9, Object: 4},
					TrackProperties: props,
				},
				&message.TrackStatus{RequestID: 8, Track: track("video", "live")},
				&message.PublishNamespace{RequestID: 10, Prefix: ns("live")},
				&message.SubscribeNamespace{RequestID: 12, Prefix: ns(), Options: options},
				&message.Namespace{Suffix: ns("sports")},
				&message.NamespaceDone{Suffix: ns("sports")},
				&message.SubscribeTracks{RequestID: 14, Prefix: ns("live")},
				&message.PublishSkipped{Suffix: ns("sports"), TrackName: []byte("video")},
			}
			if c.Has(CapJoiningFetch) {
				msgs = append(msgs, &message.Fetch{
					RequestID: 16, Joining: &message.JoiningFetch{RequestID: 2, Start: 3, Relative: true},
				}, &message.Fetch{RequestID: 18, Joining: &message.JoiningFetch{RequestID: 2, Start: 9}})
			}
			for _, m := range msgs {
				if _, ok := c.MessageType(m.Kind()); !ok {
					continue
				}
				roundTrip(t, c, m)
			}
		})
	}
}

func TestDecodedFields(t *testing.T) {
	c := mustCodec(t, 22)
	got := roundTrip(t, c, &message.Subscribe{
		RequestID: 2, Track: track("video", "live", "sports"),
		Params: fullParams(),
	}).(*message.Subscribe)
	p := got.Params
	if got.RequestID != 2 || string(got.Track.Name) != "video" || !got.Track.Namespace.Equal(ns("live", "sports")) {
		t.Fatalf("track fields %+v", got)
	}
	if !p.Has(message.ParamForward) || !p.Forward || p.SubscriberPriority != 200 ||
		p.GroupOrder != message.GroupOrderDescending || p.NewGroupRequest != 42 || p.IncludePropertiesOr(true) {
		t.Errorf("scalar parameters %+v", p)
	}
	if p.LocationFilter.Type != message.FilterAbsoluteRange || p.LocationFilter.EndObject != 300 {
		t.Errorf("location filter %+v", p.LocationFilter)
	}
	if p.Fill == nil || p.Fill.FillTimeout != 100 || p.Fill.LocationFilter.StartGroup != 1 {
		t.Errorf("fill parameters %+v", p.Fill)
	}
	if len(p.AuthTokens) != 2 || string(p.AuthTokens[0].Value) != "token-one" || len(p.RangeFilters) != 3 {
		t.Errorf("repeated parameters %+v %+v", p.AuthTokens, p.RangeFilters)
	}
	if p.Invalid != nil {
		t.Errorf("valid filters flagged %v", p.Invalid)
	}

	rd := roundTrip(t, c, &message.RequestError{
		Code:     message.RequestRedirect,
		Redirect: &message.Redirect{Track: track("x", "y")},
	}).(*message.RequestError)
	if rd.Redirect == nil || string(rd.Redirect.Track.Name) != "x" {
		t.Errorf("redirect %+v", rd.Redirect)
	}
	empty := roundTrip(t, c, &message.RequestError{Code: message.RequestRedirect}).(*message.RequestError)
	if empty.Redirect == nil || empty.Redirect.ConnectURI != "" {
		t.Error("a redirect without a target encodes an empty one")
	}
	setup := roundTrip(t, c, &message.Setup{Implementation: "x"}).(*message.Setup)
	if setup.HasPath || setup.Implementation != "x" {
		t.Errorf("setup %+v", setup)
	}
}

func hexBytes(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWireVectors(t *testing.T) {
	// byte layouts built from the drafts' message figures, so a codec bug cannot hide behind a symmetric round trip
	var p message.Params
	p.SetForward(true)
	p.SetSubscriberPriority(200)
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterNextObject})
	sub := &message.Subscribe{RequestID: 0, Track: track("v", "live"), Params: p}
	tests := []struct {
		draft int
		m     message.Message
		want  string
	}{
		// type 0x03, length 16; request 0; one field "live"; name "v"; three parameters: FORWARD (0x10) 1,
		// SUBSCRIBER_PRIORITY (delta 0x10) as one raw byte 0xc8, LOCATION_FILTER (delta 1) of type 0x05
		{22, sub, "03 0010 00 01 04 6c697665 01 76 03 10 01 10 c8 01 05"},
		// -21 spells Next Object as a two-field, length-prefixed list {0, 0}
		{21, sub, "03 0012 00 01 04 6c697665 01 76 03 10 01 10 c8 01 02 00 00"},
		// SETUP is type 0x2f00, a two-byte leading-ones varint (prefix 10, so 0xaf00 rather than QUIC's 0x6f00);
		// PATH (odd, length-prefixed) then MOQT_IMPLEMENTATION (delta 6)
		{22, &message.Setup{Path: "/a", HasPath: true, Implementation: "t"}, "af00 0007 01 02 2f61 06 01 74"},
		// GOAWAY: empty URI and a 1000ms timeout, two-byte varint 0x83e8
		{22, &message.GoAway{Timeout: 1000}, "10 0003 00 83e8"},
		// PUBLISH_DONE: TRACK_ENDED, 5 streams, reason "x"
		{22, &message.PublishDone{Code: message.DoneTrackEnded, StreamCount: 5, Reason: "x"}, "0b 0004 02 05 01 78"},
		// FETCH_OK: end of track 1, end {2, 3}, no parameters, no properties
		{22, &message.FetchOK{EndOfTrack: true, EndLocation: wire.Location{Group: 2, Object: 3}}, "18 0004 01 02 03 00"},
		// SUBSCRIBE_NAMESPACE is 0x50 from draft -18; an empty prefix and no parameters
		{20, &message.SubscribeNamespace{RequestID: 1}, "50 0003 01 00 00"},
	}
	for _, tc := range tests {
		c := mustCodec(t, tc.draft)
		got, err := c.AppendMessage(nil, tc.m)
		if err != nil {
			t.Fatalf("%s: %v", tc.m.Kind(), err)
		}
		if want := hexBytes(t, tc.want); !bytes.Equal(got, want) {
			t.Errorf("draft %d %s:\n got %x\nwant %x", tc.draft, tc.m.Kind(), got, want)
		}
		typ, n := wire.ReadVarint(got)
		if _, err := c.ParseMessage(typ, got[n+2:]); err != nil {
			t.Errorf("draft %d %s: the vector does not decode: %v", tc.draft, tc.m.Kind(), err)
		}
	}
}

func TestLegacyLocationFilters(t *testing.T) {
	// drafts -17 to -19 spell four filter types; the relay's neutral types map onto them where they can
	legacy := []message.LocationFilter{
		{Type: message.FilterRelativeStart},
		{Type: message.FilterNextObject},
		{Type: message.FilterAbsoluteStart, StartGroup: 300, StartObject: 1},
		{Type: message.FilterAbsoluteStartGroupEnd, StartGroup: 5, StartObject: 2, EndGroupDelta: 3},
	}
	ranges := []message.LocationFilter{
		{Type: message.FilterAbsoluteStartGroupEnd, StartGroup: 5, StartObject: 2, EndGroupDelta: 3},
		{Type: message.FilterAbsoluteRange, StartGroup: 5, StartObject: 2, EndGroupDelta: 3, EndObject: 7},
		{Type: message.FilterAbsoluteRange, StartGroup: 5, EndObject: 0},
	}
	for _, draft := range []int{17, 18, 19} {
		c := mustCodec(t, draft)
		for _, f := range legacy {
			var p message.Params
			p.SetLocationFilter(f)
			got := roundTrip(t, c, &message.Subscribe{Track: track("t"), Params: p}).(*message.Subscribe)
			if got.Params.LocationFilter != f {
				t.Errorf("draft %d: %+v decoded as %+v", draft, f, got.Params.LocationFilter)
			}
		}
		var p message.Params
		p.SetLocationFilter(message.LocationFilter{Type: message.FilterNone})
		got := roundTrip(t, c, &message.Subscribe{Track: track("t"), Params: p}).(*message.Subscribe)
		if f := got.Params.LocationFilter; f.Type != message.FilterAbsoluteStart || f.StartGroup != 0 || f.StartObject != 0 {
			t.Errorf("draft %d: no filter is an AbsoluteStart from {0, 0}, got %+v", draft, f)
		}
		for _, f := range []message.LocationFilter{
			{Type: message.FilterRelativeStart, StartGroup: 2},
			{Type: message.FilterAbsoluteRange, StartGroup: 1, EndObject: 4},
		} {
			p.SetLocationFilter(f)
			if _, err := c.AppendMessage(nil, &message.Subscribe{Track: track("t"), Params: p}); !errors.Is(err,
				ErrLocationFilter) {
				t.Errorf("draft %d: %+v has no spelling", draft, f)
			}
		}
		// a FETCH range travels as a Standalone Fetch, exact for the bounded forms
		for _, f := range ranges {
			var p message.Params
			p.SetLocationFilter(f)
			got := roundTrip(t, c, &message.Fetch{Track: track("t"), Params: p}).(*message.Fetch)
			if got.Params.LocationFilter != f {
				t.Errorf("draft %d: fetch %+v decoded as %+v", draft, f, got.Params.LocationFilter)
			}
		}
		for _, f := range []message.LocationFilter{
			{Type: message.FilterNextObject},
			{Type: message.FilterAbsoluteRange, EndObject: math.MaxUint64},
		} {
			p.SetLocationFilter(f)
			if _, err := c.AppendMessage(nil, &message.Fetch{Track: track("t"), Params: p}); !errors.Is(err,
				ErrLocationFilter) {
				t.Errorf("draft %d: fetch %+v has no Standalone Fetch spelling", draft, f)
			}
		}
	}
}

func TestOlderDraftVectors(t *testing.T) {
	var p message.Params
	p.SetForward(true)
	p.SetSubscriberPriority(200)
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterNextObject})
	sub := &message.Subscribe{RequestID: 0, Track: track("v", "live"), Params: p}
	var fetch message.Params
	fetch.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteStartGroupEnd, StartGroup: 1, EndGroupDelta: 2})
	var pubOK message.Params
	pubOK.SetForward(true)
	tests := []struct {
		draft int
		m     message.Message
		want  string
	}{
		// LOCATION_FILTER (delta 1) holds length 1 and Filter Type 0x2, Largest Object
		{19, sub, "03 0011 00 01 04 6c697665 01 76 03 10 01 10 c8 01 01 02"},
		// -17 adds the Required Request ID Delta after the Request ID
		{17, sub, "03 0012 00 00 01 04 6c697665 01 76 03 10 01 10 c8 01 01 02"},
		// -17 SUBSCRIBE_NAMESPACE is 0x11 with Subscribe Options (2, both) before its parameters
		{17, &message.SubscribeNamespace{RequestID: 1, Options: message.SubscribeOptionBoth}, "11 0005 01 00 00 02 00"},
		// a Standalone Fetch: type 1, track, start {1, 0}, end {3, 0} meaning all of group 3
		{19, &message.Fetch{RequestID: 2, Track: track("v", "live"), Params: fetch},
			"16 000f 02 01 01 04 6c697665 01 76 01 00 03 00 00"},
		// a Relative Joining Fetch: type 2, joined request 2, start 1
		{19, &message.Fetch{RequestID: 4, Joining: &message.JoiningFetch{RequestID: 2, Start: 1, Relative: true}},
			"16 0005 04 02 02 01 00"},
		// FETCH_OK codes its end one past the last object, and all of a group as Object 0
		{19, &message.FetchOK{EndLocation: wire.Location{Group: 3, Object: 4}}, "18 0004 00 03 05 00"},
		{18, &message.FetchOK{EndLocation: wire.Location{Group: 3, Object: math.MaxUint64}}, "18 0004 00 03 00 00"},
		// -17 PUBLISH_OK is 0x1E with parameters
		{17, &message.PublishOK{Params: pubOK}, "1e 0003 01 10 01"},
		// -17 numbers TOO_FAR_BEHIND 0x6
		{17, &message.PublishDone{Code: message.DoneTooFarBehind, StreamCount: 5, Reason: "x"}, "0b 0004 06 05 01 78"},
	}
	for _, tc := range tests {
		c := mustCodec(t, tc.draft)
		got, err := c.AppendMessage(nil, tc.m)
		if err != nil {
			t.Fatalf("%s: %v", tc.m.Kind(), err)
		}
		if want := hexBytes(t, tc.want); !bytes.Equal(got, want) {
			t.Errorf("draft %d %s:\n got %x\nwant %x", tc.draft, tc.m.Kind(), got, want)
		}
		typ, n := wire.ReadVarint(got)
		back, err := c.ParseMessage(typ, got[n+2:])
		if err != nil {
			t.Errorf("draft %d %s: the vector does not decode: %v", tc.draft, tc.m.Kind(), err)
			continue
		}
		if pd, ok := back.(*message.PublishDone); ok && pd.Code != message.DoneTooFarBehind {
			t.Errorf("draft 17 TOO_FAR_BEHIND decoded as %v", pd.Code)
		}
		if fok, ok := back.(*message.FetchOK); ok && fok.EndLocation != tc.m.(*message.FetchOK).EndLocation {
			t.Errorf("draft %d FETCH_OK end decoded as %+v", tc.draft, fok.EndLocation)
		}
	}
	// a message a draft does not define neither encodes nor decodes
	c17, c19 := mustCodec(t, 17), mustCodec(t, 19)
	if _, err := c17.AppendMessage(nil, &message.SubscribeTracks{}); !errors.Is(err, ErrUnknownMessage) {
		t.Error("SUBSCRIBE_TRACKS before -18")
	}
	if _, err := c19.AppendMessage(nil, &message.PublishOK{}); !errors.Is(err, ErrUnknownMessage) {
		t.Error("PUBLISH_OK after -17")
	}
	if _, err := c19.ParseMessage(0x11, nil); err == nil {
		t.Error("0x11 is not SUBSCRIBE_NAMESPACE after -17")
	}
	if _, err := c17.ParseMessage(0x50, nil); err == nil {
		t.Error("0x50 is not SUBSCRIBE_NAMESPACE in -17")
	}
	for _, bad := range []struct {
		c    *Codec
		typ  uint64
		body string
	}{
		{c17, 0x11, "01 00 00 03 00"},                            // subscribe options out of range
		{c19, 0x16, "02 07"},                                     // unknown fetch type
		{c19, 0x16, "02 01 01 04 6c697665 01 76 05 00 03 00 00"}, // a fetch ending before it starts
		{c19, 0x03, "00 01 04 6c697665 01 76 01 21 01 09"},       // unknown filter type
	} {
		if _, err := bad.c.ParseMessage(bad.typ, hexBytes(t, bad.body)); err == nil {
			t.Errorf("draft %d %#x %s should not decode", bad.c.Draft(), bad.typ, bad.body)
		}
	}
}

func TestLocationFilterSpellings(t *testing.T) {
	filters := []message.LocationFilter{
		{Type: message.FilterNone},
		{Type: message.FilterRelativeStart, StartGroup: 2},
		{Type: message.FilterAbsoluteStart, StartGroup: 300, StartObject: 1},
		{Type: message.FilterAbsoluteStartGroupEnd, StartGroup: 5, StartObject: 0, EndGroupDelta: 2},
		{Type: message.FilterAbsoluteRange, StartGroup: 5, StartObject: 0, EndGroupDelta: 2, EndObject: 7},
		{Type: message.FilterNextObject},
	}
	for _, draft := range Drafts() {
		c := mustCodec(t, draft)
		if c.Has(CapLegacyLocationFilter) {
			continue
		}
		for _, f := range filters {
			var p message.Params
			p.SetLocationFilter(f)
			got := roundTrip(t, c, &message.Fetch{Track: track("t"), Params: p}).(*message.Fetch)
			if got.Params.LocationFilter != f {
				t.Errorf("draft %d: %+v decoded as %+v", draft, f, got.Params.LocationFilter)
			}
		}
		// an open filter from {0, 0} is unfiltered; the length-inferred drafts spell it that way
		var p message.Params
		p.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteStart})
		got := roundTrip(t, c, &message.Fetch{Track: track("t"), Params: p}).(*message.Fetch)
		want := message.FilterAbsoluteStart
		if c.Has(CapLengthInferredLocationFilter) {
			want = message.FilterNone
		}
		if got.Params.LocationFilter.Type != want {
			t.Errorf("draft %d: absolute {0, 0} decoded as %v", draft, got.Params.LocationFilter.Type)
		}
		p.SetLocationFilter(message.LocationFilter{Type: 9})
		if _, err := c.AppendMessage(nil, &message.Fetch{Track: track("t"), Params: p}); !errors.Is(err, ErrLocationFilter) {
			t.Errorf("draft %d: an unknown filter type should not encode", draft)
		}
	}
}

func TestCodecRegistry(t *testing.T) {
	if len(Supported()) != len(Drafts()) || Drafts()[0] != 22 || Drafts()[len(Drafts())-1] != 17 {
		t.Errorf("drafts %v", Drafts())
	}
	c, ok := ForALPN("moqt-21")
	if !ok || c.Draft() != 21 || c.ALPN() != "moqt-21" || !c.Has(CapLengthInferredLocationFilter) {
		t.Fatalf("moqt-21 codec %+v", c)
	}
	if _, ok := ForALPN("moqt-99"); ok {
		t.Error("an unknown ALPN resolved")
	}
	if _, ok := ForDraft(3); ok {
		t.Error("an unknown draft resolved")
	}
	if got := ALPNs([]int{22, 99, 20}); len(got) != 2 || got[0] != "moqt-22" || got[1] != "moqt-20" {
		t.Errorf("ALPNs %v", got)
	}
	if ServerPreference[0] != 22 || ClientOffer[0] != 22 {
		t.Error("preference defaults")
	}
	if _, ok := c.MessageType(message.KindPublishOK); ok {
		t.Error("PUBLISH_OK is not a message from draft -18 on")
	}
	if _, ok := c.MessageKind(0x1e); ok {
		t.Error("0x1e is reserved from draft -19")
	}
	if k, ok := c.MessageKind(0x2f00); !ok || k != message.KindSetup {
		t.Error("SETUP code")
	}
	if _, err := c.AppendMessage(nil, &message.PublishOK{}); !errors.Is(err, ErrUnknownMessage) {
		t.Error("PUBLISH_OK should not encode")
	}
}

func parseBody(t *testing.T, c *Codec, typ uint64, body string) error {
	t.Helper()
	_, err := c.ParseMessage(typ, hexBytes(t, body))
	return err
}

func wantCode(t *testing.T, err error, code message.SessionCode, what string) {
	t.Helper()
	var se *message.SessionError
	if !errors.As(err, &se) || se.Code != code {
		t.Errorf("%s: want session code %#x, got %v", what, uint64(code), err)
	}
}

func TestParseViolations(t *testing.T) {
	c22, c21 := mustCodec(t, 22), mustCodec(t, 21)
	const subscribe, setup, fetchOK, pubState = 0x03, 0x2f00, 0x18, 0x22
	prefix := "00 01 04 6c697665 01 76"
	tests := []struct {
		name string
		c    *Codec
		typ  uint64
		body string
		code message.SessionCode
	}{
		{"unknown message type", c22, 0x7f, "", message.CodeProtocolViolation},
		{"unknown parameter", c22, subscribe, prefix + " 01 11 00", message.CodeProtocolViolation},
		{"parameter not allowed", c22, subscribe, prefix + " 01 08 05", message.CodeProtocolViolation},
		{"duplicate parameter", c22, subscribe, prefix + " 02 10 01 00 01", message.CodeProtocolViolation},
		{"forward out of range", c22, subscribe, prefix + " 01 10 02", message.CodeProtocolViolation},
		{"group order out of range", c22, subscribe, prefix + " 01 22 03", message.CodeProtocolViolation},
		{"include properties out of range", c22, subscribe, prefix + " 01 35 05", message.CodeProtocolViolation},
		{"unknown filter type", c22, subscribe, prefix + " 01 21 06", message.CodeProtocolViolation},
		{"filter type aliasing a known type", c22, subscribe, prefix + " 01 21 8105", message.CodeProtocolViolation},
		{"filter end overflows", c22, subscribe, prefix + " 01 21 03 ffffffffffffffffff 00 01", message.CodeProtocolViolation},
		{"too many filter fields", c21, subscribe, prefix + " 01 21 05 01 02 03 04 05", message.CodeProtocolViolation},
		{"truncated filter fields", c21, subscribe, prefix + " 01 21 01 80", message.CodeProtocolViolation},
		{"bad token", c22, subscribe, prefix + " 01 03 01 09", message.CodeKeyValueFormatting},
		{"bad setup token", c22, setup, "03 01 09", message.CodeKeyValueFormatting},
		{"trailing bytes", c22, subscribe, prefix + " 00 ff", message.CodeProtocolViolation},
		{"truncated", c22, subscribe, "00 01", message.CodeProtocolViolation},
		{"parameter type overflows", c22, subscribe, prefix + " 02 10 01 ffffffffffffffffff 00", message.CodeProtocolViolation},
		{"unknown huge parameter", c22, subscribe, prefix + " 01 ffffffffffffffffff 00", message.CodeProtocolViolation},
		{"end of track out of range", c22, fetchOK, "02 00 00 00", message.CodeProtocolViolation},
		{"bad fill scope", c22, subscribe, prefix + " 01 23 02 01 11", message.CodeProtocolViolation},
		{"fill scope trailing", c22, subscribe, prefix + " 01 23 02 00 00", message.CodeProtocolViolation},
		{"fill scope disallows", c22, subscribe, prefix + " 01 23 03 01 10 01", message.CodeProtocolViolation},
		{"malformed setup option", c22, setup, "01 05 61", message.CodeProtocolViolation},
		{"empty namespace field", c22, subscribe, "00 01 00 00 00", message.CodeProtocolViolation},
		{"publish state from subscriber param", c22, pubState, "01 20 05", message.CodeProtocolViolation},
	}
	for _, tc := range tests {
		wantCode(t, parseBody(t, tc.c, tc.typ, tc.body), tc.code, tc.name)
	}
}

func TestRangeFilterRequestFailures(t *testing.T) {
	c := mustCodec(t, 22)
	prefix := "00 01 04 6c697665 01 76"
	tests := []struct {
		name, params string
	}{
		{"bounds overflow", "01 25 0b 00 ffffffffffffffffff 01"},
		{"priority above 255", "01 27 04 00 00 81 00"},
		{"odd property type", "01 28 03 00 01 05"},
		{"repeated type and set", "02 25 02 00 01 00 02 00 03"},
		{"truncated after set", "01 28 01 00"},
	}
	for _, tc := range tests {
		m, err := c.ParseMessage(0x03, hexBytes(t, prefix+" "+tc.params))
		if err != nil {
			t.Errorf("%s: an invalid filter fails the request, not the session: %v", tc.name, err)
			continue
		}
		inv := m.(*message.Subscribe).Params.Invalid
		if inv == nil || inv.Code != message.RequestInvalidFilter {
			t.Errorf("%s: want INVALID_FILTER, got %v", tc.name, inv)
		}
	}
	// a filter inside FILL_PARAMETERS fails the request too
	m, err := c.ParseMessage(0x03, hexBytes(t, prefix+" 01 23 06 01 27 03 00 00 81"))
	if err != nil || m.(*message.Subscribe).Params.Invalid == nil {
		t.Errorf("an invalid fill filter should flag the request, got %v", err)
	}
	if got := (&message.RequestFailure{Code: message.RequestInvalidFilter}).Error(); !strings.Contains(got, "0x36") {
		t.Error(got)
	}
}

func TestEncodeRefusals(t *testing.T) {
	c := mustCodec(t, 22)
	var p message.Params
	p.SetExpires(1)
	if _, err := c.AppendMessage(nil, &message.Subscribe{Params: p}); !errors.Is(err, ErrParamNotAllowed) {
		t.Error("EXPIRES is not a SUBSCRIBE parameter")
	}
	var bad message.Params
	bad.AuthTokens = []wire.Token{{AliasType: 7}}
	if _, err := c.AppendMessage(nil, &message.Subscribe{Params: bad}); err == nil {
		t.Error("a bad token should not encode")
	}
	bad = message.Params{RangeFilters: []message.RangeFilter{{
		Type:   message.ParamSubgroupFilter,
		Ranges: []wire.Range{{Start: 5, End: 1}},
	}}}
	if _, err := c.AppendMessage(nil, &message.Subscribe{Params: bad}); err == nil {
		t.Error("descending ranges should not encode")
	}
	fill := &message.Params{}
	fill.SetForward(true)
	bad = message.Params{}
	bad.SetFill(fill)
	if _, err := c.AppendMessage(nil, &message.Subscribe{Params: bad}); !errors.Is(err, ErrParamNotAllowed) {
		t.Error("FORWARD is not allowed in a fill scope")
	}
	if _, err := c.AppendMessage(nil, &message.Setup{AuthTokens: []wire.Token{{AliasType: 7}}}); err == nil {
		t.Error("a bad setup token should not encode")
	}
	if _, err := c.AppendMessage(nil, &message.RequestError{Reason: strings.Repeat("x", wire.MaxMessageLen)}); !errors.Is(err, wire.ErrMessageTooLong) {
		t.Error("an oversized message should not encode")
	}
	deep := make([]string, wire.MaxNamespaceFields+1)
	for i := range deep {
		deep[i] = "f"
	}
	long := strings.Repeat("n", wire.MaxFullTrackNameLen+1)
	for _, m := range []message.Message{
		&message.Subscribe{Track: track("t", deep...)}, &message.Publish{Track: track(long, "ns")},
		&message.Fetch{Track: track("t", "")}, &message.TrackStatus{Track: track(long, "ns")},
		&message.PublishNamespace{Prefix: ns(deep...)}, &message.SubscribeNamespace{Prefix: ns("")},
		&message.SubscribeTracks{Prefix: ns(deep...)}, &message.Namespace{Suffix: ns(long)},
		&message.NamespaceDone{Suffix: ns("")}, &message.PublishSkipped{Suffix: ns("a"), TrackName: []byte(long)},
		&message.RequestError{Code: message.RequestRedirect, Redirect: &message.Redirect{Track: track("t", deep...)}},
	} {
		if _, err := c.AppendMessage(nil, m); !errors.Is(err, wire.ErrNamespaceTooLong) &&
			!errors.Is(err, wire.ErrEmptyNamespaceField) {
			t.Errorf("%s with a malformed name encoded: %v", m.Kind(), err)
		}
	}
	var empty message.Params
	empty.Mark(message.ParamFillParameters)
	got := roundTrip(t, c, &message.Subscribe{Track: track("t"), Params: empty}).(*message.Subscribe)
	if got.Params.Fill == nil {
		t.Error("an empty FILL_PARAMETERS still requests a fill")
	}
}

func TestSetupOptions(t *testing.T) {
	c := mustCodec(t, 22)
	// a repeated PATH keeps its first value; unknown options are kept and ignored
	body := hexBytes(t, "01 02 2f61 00 02 2f62 809c 01 7a")
	m, err := c.ParseMessage(0x2f00, body)
	if err != nil {
		t.Fatal(err)
	}
	s := m.(*message.Setup)
	if s.Path != "/a" || len(s.Unknown) == 0 {
		t.Errorf("setup %+v", s)
	}
	// duplicated scalar options keep their first values too
	body = hexBytes(t, "04 05 00 06 02 08 02 09 00 0a")
	m, err = c.ParseMessage(0x2f00, body)
	if err != nil {
		t.Fatal(err)
	}
	s = m.(*message.Setup)
	if s.MaxAuthTokenCacheSize != 5 || s.MaxFilterRanges != 8 || s.MaxRequestUpdates != 9 {
		t.Errorf("setup %+v", s)
	}
	body = hexBytes(t, "05 01 61 00 01 62 02 01 74 00 01 75")
	m, err = c.ParseMessage(0x2f00, body)
	if err != nil {
		t.Fatal(err)
	}
	s = m.(*message.Setup)
	if s.Authority != "a" || s.Implementation != "t" {
		t.Errorf("setup %+v", s)
	}
}
