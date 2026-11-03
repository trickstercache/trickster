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
	"bytes"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func TestDraft23Vectors(t *testing.T) {
	// byte layouts from the -23 message figures: a varint Length, PAUSED for FORWARD, REQUEST_OK for FETCH_OK,
	// Error Details, NAMESPACE parameters and FILL_PARAMETERS as a bare count
	var forward, paused message.Params
	forward.SetForward(true)
	forward.SetSubscriberPriority(200)
	forward.SetLocationFilter(message.LocationFilter{Type: message.FilterNextObject})
	paused.SetForward(false)
	var fill message.Params
	fill.SetFillTimeout(100)
	var withFill message.Params
	withFill.SetFill(&fill)
	var pub message.Params
	pub.SetFill(&message.Params{})
	pub.SetSubscribeTracksID(5)
	var largest message.Params
	largest.SetLargestObject(wire.Location{Group: 2, Object: 3})
	tests := []struct {
		m    message.Message
		want string
	}{
		// PAUSED (0x11) 0, SUBSCRIBER_PRIORITY (delta 0x0f) 0xc8, LOCATION_FILTER (delta 1) Next Object
		{&message.Subscribe{Track: track("v", "live"), Params: forward}, "03 10 00 01 04 6c697665 01 76 03 11 00 0f c8 01 05"},
		{&message.Subscribe{Track: track("v", "live"), Params: paused}, "03 0c 00 01 04 6c697665 01 76 01 11 01"},
		// FILL_PARAMETERS (0x23) holding one parameter, FILL_TIMEOUT 100, with no length around it
		{&message.Subscribe{Track: track("v", "live"), Params: withFill}, "03 0e 00 01 04 6c697665 01 76 01 23 01 0a 64"},
		// MAX_CONTROL_MESSAGE_SIZE (delta 2 from MOQT_IMPLEMENTATION), an odd type whose three bytes hold 2^20
		{
			&message.Setup{Path: "/a", HasPath: true, Implementation: "t", MaxControlMessageSize: 1 << 20},
			"af00 0c 01 02 2f61 06 01 74 02 03 d00000",
		},
		// FETCH_OK is a REQUEST_OK whose LARGEST_OBJECT ends the response; End Of Track has no field
		{
			&message.FetchOK{EndOfTrack: true, EndLocation: wire.Location{Group: 2, Object: 3}, Params: largest},
			"07 04 01 09 02 03",
		},
		// a REDIRECT detail (0x01) holding an empty URI, the namespace "a" and the track "v"
		{
			&message.RequestError{Code: message.RequestRedirect, Redirect: &message.Redirect{Track: track("v", "a")}},
			"05 0b 34 00 00 01 06 00 01 01 61 01 76",
		},
		// two UNSUPPORTED_PROPERTY details (0x02, then delta 0), each a three-byte varint
		{
			&message.RequestError{Code: message.RequestUnsupportedExtension, UnsupportedProperties: []uint64{0x4000, 0x4002}},
			"05 0b 33 00 00 02 c04000 00 c04002",
		},
		// a detail goes only with its own code
		{&message.RequestError{Code: message.RequestDoesNotExist, UnsupportedProperties: []uint64{0x4000}}, "05 03 10 00 00"},
		{&message.Namespace{Suffix: ns("a")}, "08 04 01 01 61 00"},
		// FILL_PARAMETERS with no parameters, then SUBSCRIBE_TRACKS_ID (delta 0x13) 5
		{
			&message.Publish{RequestID: 1, Track: track("v", "a"), TrackAlias: 2, Params: pub},
			"1d 0c 01 01 01 61 01 76 02 02 23 00 13 05",
		},
		// UNAVAILABLE is new in -23
		{&message.RequestError{Code: message.RequestUnavailable}, "05 03 0a 00 00"},
		{&message.PublishDone{Code: message.DoneExpiredAuthToken}, "0b 03 07 00 00"},
	}
	c := mustCodec(t, 23)
	for _, tc := range tests {
		got, err := c.AppendMessage(nil, tc.m)
		if err != nil {
			t.Fatalf("%s: %v", tc.m.Kind(), err)
		}
		if want := hexBytes(t, tc.want); !bytes.Equal(got, want) {
			t.Errorf("%s:\n got %x\nwant %x", tc.m.Kind(), got, want)
		}
		typ, body, err := c.ReadMessage(stream(got), nil)
		if err != nil {
			t.Fatalf("%s: frame: %v", tc.m.Kind(), err)
		}
		if _, err := c.ParseMessage(typ, body); err != nil {
			t.Errorf("%s: the vector does not decode: %v", tc.m.Kind(), err)
		}
	}
}

func TestDraft23Decoding(t *testing.T) {
	c := mustCodec(t, 23)
	sub := roundTrip(t, c, &message.Subscribe{Track: track("v", "live"), Params: func() message.Params {
		var p message.Params
		p.SetForward(false)
		return p
	}()}).(*message.Subscribe)
	if !sub.Params.Has(message.ParamForward) || sub.Params.Forward {
		t.Errorf("PAUSED 1 decodes as not forwarding: %+v", sub.Params)
	}
	m, err := c.ParseMessage(0x05, hexBytes(t, "34 00 00 01 06 00 01 01 61 01 76 02 01 00"))
	if err != nil {
		t.Fatal(err)
	}
	re := m.(*message.RequestError)
	if re.Redirect == nil || string(re.Redirect.Track.Name) != "v" || !re.Redirect.Track.Namespace.Equal(ns("a")) {
		t.Errorf("redirect %+v", re.Redirect)
	}
	// an UNSUPPORTED_PROPERTY with another code, and an unknown detail, are ignored
	m, err = c.ParseMessage(0x05, hexBytes(t, "10 00 00 02 c04000 02 05"))
	if err != nil || len(m.(*message.RequestError).UnsupportedProperties) != 0 {
		t.Errorf("details for another code: %+v %v", m, err)
	}
	m, err = c.ParseMessage(0x05, hexBytes(t, "33 00 00 02 c04000 00 c04002"))
	if err != nil || !slices.Equal(m.(*message.RequestError).UnsupportedProperties, []uint64{0x4000, 0x4002}) {
		t.Errorf("unsupported properties %+v %v", m, err)
	}
	m, err = c.ParseMessage(0x2f00, hexBytes(t, "09 01 05"))
	if err != nil || m.(*message.Setup).MaxControlMessageSize != wire.MaxMessageLen {
		t.Errorf("a MAX_CONTROL_MESSAGE_SIZE below the default reads as the default: %+v %v", m, err)
	}
	_, err = c.ParseMessage(0x2f00, hexBytes(t, "09 02 05 00"))
	wantCode(t, err, message.CodeKeyValueFormatting, "a MAX_CONTROL_MESSAGE_SIZE with trailing bytes")
	// draft -22 knows no MAX_CONTROL_MESSAGE_SIZE and keeps it as an unknown option
	m, err = mustCodec(t, 22).ParseMessage(0x2f00, hexBytes(t, "09 01 05"))
	if err != nil || m.(*message.Setup).MaxControlMessageSize != 0 || len(m.(*message.Setup).Unknown) != 1 {
		t.Errorf("draft -22 option 0x09: %+v %v", m, err)
	}
	if k, ok := c.MessageKind(0x07); !ok || k != message.KindRequestOK {
		t.Error("REQUEST_OK code")
	}
	if typ, ok := c.MessageType(message.KindFetchOK); !ok || typ != 0x07 {
		t.Error("FETCH_OK is sent as REQUEST_OK")
	}
	if _, ok := c.MessageKind(0x18); ok {
		t.Error("0x18 is reserved in -23")
	}
}

func TestDraft23Framing(t *testing.T) {
	c23, c22 := mustCodec(t, 23), mustCodec(t, 22)
	// a Length of 70000, a three-byte varint, exceeds the default MAX_CONTROL_MESSAGE_SIZE
	_, _, err := c23.ReadMessage(stream(hexBytes(t, "03 c11170")), nil)
	wantCode(t, err, message.CodeFieldLengthExceeded, "oversized control message")
	if _, _, err = c23.ReadMessage(stream(hexBytes(t, "03 05 00")), nil); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a cut body: %v", err)
	}
	if _, err = c23.ReadMessageBody(stream(nil), nil); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a cut length: %v", err)
	}
	// -22 reads the same first bytes as a 16-bit length
	typ, body, err := c22.ReadMessage(stream(hexBytes(t, "03 0001 00")), nil)
	if err != nil || typ != 0x03 || len(body) != 1 {
		t.Errorf("-22 framing %d %x %v", typ, body, err)
	}
	if _, err = c23.AppendMessage(nil, &message.RequestError{Reason: strings.Repeat("x", wire.MaxMessageLen)}); !errors.Is(err, wire.ErrMessageTooLong) {
		t.Errorf("an oversized -23 message should not encode: %v", err)
	}
	if _, err = c23.AppendMessage(nil, &message.Subscribe{Track: track("", "live")}); !errors.Is(err, ErrEmptyTrackName) {
		t.Errorf("an empty track name should not encode in -23: %v", err)
	}
	if _, err = c22.AppendMessage(nil, &message.Subscribe{Track: track("", "live")}); err != nil {
		t.Errorf("-22 allows an empty track name: %v", err)
	}
}

func TestDraft23Violations(t *testing.T) {
	c23, c22 := mustCodec(t, 23), mustCodec(t, 22)
	const subscribe, setup, reqErr, namespace, subTracks = 0x03, 0x2f00, 0x05, 0x08, 0x51
	prefix := "00 01 04 6c697665 01 76"
	tests := []struct {
		name string
		typ  uint64
		body string
		code message.SessionCode
	}{
		{"FETCH_OK's code", 0x18, "", message.CodeUnexpectedMessage},
		{"FORWARD is unknown", subscribe, prefix + " 01 10 01", message.CodeInvalidParameter},
		{"PAUSED out of range", subscribe, prefix + " 01 11 02", message.CodeInvalidParameter},
		{"parameter not allowed", subscribe, prefix + " 01 08 05", message.CodeInvalidParameter},
		{"duplicate parameter", subscribe, prefix + " 02 11 01 00 01", message.CodeInvalidParameter},
		{"group order out of range", subscribe, prefix + " 01 22 03", message.CodeInvalidParameter},
		{"unknown filter type", subscribe, prefix + " 01 21 06", message.CodeInvalidParameter},
		{"filter end overflows", subscribe, prefix + " 01 21 03 ffffffffffffffffff 00 01", message.CodeDeltaEncodingOverflow},
		{"parameter type overflows", subscribe, prefix + " 02 11 01 ffffffffffffffffff 00", message.CodeDeltaEncodingOverflow},
		{"bad token", subscribe, prefix + " 01 03 01 09", message.CodeMalformedAuthToken},
		{"bad setup token", setup, "03 01 09", message.CodeMalformedAuthToken},
		{"trailing bytes", subscribe, prefix + " 00 ff", message.CodeMalformedMessage},
		{"truncated", subscribe, "00 01", message.CodeMalformedMessage},
		{"empty namespace field", subscribe, "00 01 00 00 00", message.CodeInvalidTrackNamespace},
		{"empty track name", subscribe, "00 01 04 6c697665 00 00", message.CodeProtocolViolation},
		{"reason too long", reqErr, "00 00 8401", message.CodeFieldLengthExceeded},
		{"redirect without its detail", reqErr, "34 00 00", message.CodeMalformedMessage},
		{"malformed redirect detail", reqErr, "34 00 00 01 01 05", message.CodeKeyValueFormatting},
		{"malformed details", reqErr, "10 00 00 01 05", message.CodeMalformedMessage},
		{"NAMESPACE carries no parameters yet", namespace, "01 01 61 01 08 05", message.CodeInvalidParameter},
		{"SUBSCRIBE_TRACKS has no RENDEZVOUS_TIMEOUT", subTracks, "00 01 01 61 01 04 05", message.CodeInvalidParameter},
		{"fill holds a disallowed parameter", subscribe, prefix + " 01 23 01 11 01", message.CodeInvalidParameter},
	}
	for _, tc := range tests {
		wantCode(t, parseBody(t, c23, tc.typ, tc.body), tc.code, tc.name)
	}
	// the same faults close an earlier draft's session with PROTOCOL_VIOLATION, its tokens with a formatting error
	wantCode(t, parseBody(t, c22, subscribe, prefix+" 00 ff"), message.CodeProtocolViolation, "-22 trailing bytes")
	wantCode(t, parseBody(t, c22, subscribe, prefix+" 01 03 01 09"), message.CodeKeyValueFormatting, "-22 bad token")
	wantCode(t, parseBody(t, c22, subscribe, "00 01 00 00 00"), message.CodeProtocolViolation, "-22 empty field")
	if err := parseBody(t, c22, subscribe, "00 01 04 6c697665 00 00"); err != nil {
		t.Errorf("-22 allows an empty track name: %v", err)
	}
	if err := parseBody(t, c22, subTracks, "00 01 01 61 01 04 05"); err != nil {
		t.Errorf("-22 lets SUBSCRIBE_TRACKS carry RENDEZVOUS_TIMEOUT: %v", err)
	}
	if err := parseBody(t, c22, namespace, "01 01 61 01 08 05"); err == nil {
		t.Error("-22's NAMESPACE has no parameter list")
	}
}

func TestDraft23FramingCodes(t *testing.T) {
	c := mustCodec(t, 23)
	// a Setup Option type past 2^64-1 overflows its delta
	wantCode(t, parseBody(t, c, 0x2f00, "ffffffffffffffffff 00 01 00"), message.CodeDeltaEncodingOverflow, "option type overflow")
	coded := c.SessionError(message.CodeInvalidField, "x")
	if err := c.wireError(coded); err != coded {
		t.Errorf("a coded error passes through: %v", err)
	}
	wantCode(t, c.objectError(wire.ErrShort), message.CodeMalformedObject, "a cut object")
	wantCode(t, c.wireError(wire.ErrNamespaceTooLong), message.CodeFieldLengthExceeded, "a long name")
}

func TestDraft23CodeTranslation(t *testing.T) {
	c23, c22 := mustCodec(t, 23), mustCodec(t, 22)
	for _, code := range []message.SessionCode{
		message.CodeInvalidStreamHeader, message.CodeMalformedObject, message.CodeInvalidObjectStatus,
		message.CodeTruncatedObject, message.CodeMalformedMessage, message.CodeInvalidField,
		message.CodeInvalidTrackNamespace, message.CodeUnexpectedMessage, message.CodeRoleViolation,
		message.CodeInvalidParameter, message.CodeClosedControlStream, message.CodeDeltaEncodingOverflow,
		message.CodeFieldLengthExceeded,
	} {
		if c23.SessionCode(code) != code || c22.SessionCode(code) != message.CodeProtocolViolation {
			t.Errorf("session code %#x", uint64(code))
		}
	}
	if c22.SessionCode(message.CodeTooManyRequestUpdates) != message.CodeTooManyRequestUpdates {
		t.Error("codes earlier drafts have pass through")
	}
	if c22.RequestCode(message.RequestUnavailable) != message.RequestExcessiveLoad ||
		c23.RequestCode(message.RequestUnavailable) != message.RequestUnavailable {
		t.Error("UNAVAILABLE")
	}
	got, err := c22.AppendMessage(nil, &message.PublishDone{Code: message.DoneExpiredAuthToken})
	if err != nil || !bytes.Equal(got, hexBytes(t, "0b 0003 01 00 00")) {
		t.Errorf("-22 learns an expired token only as UNAUTHORIZED: %x %v", got, err)
	}
	got, err = c22.AppendMessage(nil, &message.RequestError{Code: message.RequestUnavailable})
	if err != nil || !bytes.Equal(got, hexBytes(t, "05 0003 09 00 00")) {
		t.Errorf("-22 learns of no stream credit as EXCESSIVE_LOAD: %x %v", got, err)
	}
	// a FETCH_OK's LARGEST_OBJECT and EXPIRES are -23's, and an earlier draft states the end in the message
	var p message.Params
	p.SetLargestObject(wire.Location{Group: 2, Object: 3})
	p.SetExpires(10)
	got, err = c22.AppendMessage(nil, &message.FetchOK{EndLocation: wire.Location{Group: 2, Object: 3}, Params: p})
	if err != nil || !bytes.Equal(got, hexBytes(t, "18 0004 00 02 03 00")) {
		t.Errorf("-22 FETCH_OK %x %v", got, err)
	}
}

func TestDraft23ParamsByMessage(t *testing.T) {
	c23, c22 := mustCodec(t, 23), mustCodec(t, 22)
	if !c23.KnownParams().Has(message.ParamSubscribeTracksID) || c22.KnownParams().Has(message.ParamSubscribeTracksID) {
		t.Error("SUBSCRIBE_TRACKS_ID is -23's")
	}
	if c22.allowedParams(message.KindFetchOK) != 0 || !c23.allowedParams(message.KindFetchOK).Has(message.ParamLargestObject) {
		t.Error("FETCH_OK parameters")
	}
	if c22.allowedParams(message.KindPublish).Has(message.ParamFillParameters) ||
		!c23.allowedParams(message.KindPublish).Has(message.ParamFillParameters) {
		t.Error("PUBLISH carries FILL_PARAMETERS from -23")
	}
	st23, st22 := c23.allowedParams(message.KindSubscribeTracks), c22.allowedParams(message.KindSubscribeTracks)
	if st23.Has(message.ParamRendezvousTimeout) || st23.Has(message.ParamNewGroupRequest) ||
		!st23.Has(message.ParamFillParameters) || !st22.Has(message.ParamRendezvousTimeout) ||
		st22.Has(message.ParamFillParameters) {
		t.Error("SUBSCRIBE_TRACKS parameters")
	}
	var p message.Params
	p.SetSubscribeTracksID(3)
	if _, err := c22.AppendMessage(nil, &message.Publish{Track: track("v", "a"), Params: p}); !errors.Is(err, ErrParamNotAllowed) {
		t.Errorf("-22 cannot carry SUBSCRIBE_TRACKS_ID: %v", err)
	}
}

func TestDraft23FetchStream(t *testing.T) {
	c := mustCodec(t, 23)
	objs := []message.FetchObject{
		{Group: 5, Object: 0, Priority: 10, PayloadLen: 3},
		{Group: 5, Object: 3, Priority: 10},                        // in-group delta 3 - 0 - 2
		{EndOfRange: message.RangeEndUnknown, Group: 5, Object: 7}, // delta 0, object 7 - 3 - 1
		{Group: 5, Object: 8, Priority: 10},                        // the marker's object plus one
		{EndOfRange: message.RangeEndGone, Group: 7, Object: 2},    // group delta 2 with no added one, object whole
		{EndOfRange: message.RangeEndGroup, Group: 7, Object: math.MaxUint64},
		{Group: 8, Object: 0, Priority: 10}, // after End of Group the object is whole
		{EndOfRange: message.RangeEndTrack, Group: 8, Object: math.MaxUint64},
	}
	got := fetchRoundTrip(t, c, message.GroupOrderAscending, objs)
	want := hexBytes(t, "05 0b 1c 05 00 0a 03 000000 04 01 00 810c 00 03 00 00 840c 02 02 8080 0c 00 00 00 8081")
	if !bytes.Equal(got, want) {
		t.Errorf("-23 fetch stream\n got %x\nwant %x", got, want)
	}
	fetchRoundTrip(t, c, message.GroupOrderDescending, []message.FetchObject{
		{Group: 9, Object: 0, Priority: 1},
		{EndOfRange: message.RangeEndUnknown, Group: 9, Object: 5},
		{EndOfRange: message.RangeEndNonExistent, Group: 7, Object: 1},
		{Group: 6, Object: 0, Priority: 1},
		{Group: 6, Object: 9, Priority: 1},
	})
	// a marker may open a stream, whole
	fetchRoundTrip(t, c, message.GroupOrderAscending, []message.FetchObject{
		{EndOfRange: message.RangeEndTimedOut, Group: 3, Object: 4},
		{EndOfRange: message.RangeEndUnknown, Group: 4, Object: 0},
	})
}

func TestDraft23FetchMarkersOlderDrafts(t *testing.T) {
	// a draft without Gone learns of an unknown range, and one without group ends learns nothing
	w := mustCodec(t, 22).NewFetchWriter(message.GroupOrderAscending)
	b, err := w.AppendObject(nil, &message.FetchObject{Group: 1, Object: 0, Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	n := len(b)
	if b, err = w.AppendObject(b, &message.FetchObject{EndOfRange: message.RangeEndGroup}); err != nil || len(b) != n {
		t.Errorf("End of Group in -22: %x %v", b[n:], err)
	}
	if b, err = w.AppendObject(b, &message.FetchObject{EndOfRange: message.RangeEndGone, Group: 7, Object: 2}); err != nil ||
		!bytes.Equal(b[n:], hexBytes(t, "810c 07 02")) {
		t.Errorf("Gone in -22: %x %v", b[n:], err)
	}
	w23 := mustCodec(t, 23).NewFetchWriter(message.GroupOrderAscending)
	if _, err = w23.AppendObject(nil, &message.FetchObject{EndOfRange: message.RangeEndGroup}); !errors.Is(err, ErrObjectOrder) {
		t.Errorf("End of Group needs a prior object: %v", err)
	}
	b, _ = w23.AppendObject(nil, &message.FetchObject{Group: 4, Object: 2, Priority: 1})
	for _, bad := range []message.FetchObject{
		{EndOfRange: message.RangeEndUnknown, Group: 4, Object: 2},
		{EndOfRange: message.RangeEndUnknown, Group: 3, Object: 9},
	} {
		if _, err = w23.AppendObject(b, &bad); !errors.Is(err, ErrObjectOrder) {
			t.Errorf("a marker before the prior entry %+v: %v", bad, err)
		}
	}
	d23 := mustCodec(t, 23).NewFetchWriter(message.GroupOrderDescending)
	_, _ = d23.AppendObject(nil, &message.FetchObject{Group: 4, Priority: 1})
	if _, err = d23.AppendObject(nil, &message.FetchObject{EndOfRange: message.RangeEndUnknown, Group: 5}); !errors.Is(err, ErrObjectOrder) {
		t.Errorf("a descending marker after the prior group: %v", err)
	}
}

func TestDraft23FetchViolations(t *testing.T) {
	c23, c22 := mustCodec(t, 23), mustCodec(t, 22)
	tests := []struct {
		name  string
		order message.GroupOrder
		body  string
		code  message.SessionCode
	}{
		{"End of Group first", message.GroupOrderAscending, "8080", message.CodeMalformedObject},
		{"unknown indicator", message.GroupOrderAscending, "8082", message.CodeMalformedObject},
		{"first refers to a prior object", message.GroupOrderAscending, "14 00 05 00", message.CodeMalformedObject},
		{"an object after its group's end", message.GroupOrderAscending, "1c 05 00 0a 00 8080 00 00", message.CodeDeltaEncodingOverflow},
		{"a marker after its group's end", message.GroupOrderAscending, "1c 05 00 0a 00 8080 810c 00 00", message.CodeDeltaEncodingOverflow},
		{"marker object overflows", message.GroupOrderAscending, "1c 05 00 0a 00 810c 00 ffffffffffffffffff", message.CodeDeltaEncodingOverflow},
		{"marker group overflows", message.GroupOrderAscending, "1c 05 00 0a 00 810c ffffffffffffffffff 00", message.CodeDeltaEncodingOverflow},
		{"descending marker underflows", message.GroupOrderDescending, "1c 01 00 0a 00 810c 02 00", message.CodeDeltaEncodingOverflow},
		{"in-group delta overflows", message.GroupOrderAscending, "1c 00 fffffffffffffffffd 0a 00 04 01 00", message.CodeDeltaEncodingOverflow},
		{"missing prior priority", message.GroupOrderAscending, "808c 00 03 04 00", message.CodeMalformedObject},
	}
	for _, tc := range tests {
		r := c23.NewFetchReader(stream(hexBytes(t, tc.body)), tc.order)
		var err error
		for err == nil {
			_, err = r.Next()
		}
		wantCode(t, err, tc.code, tc.name)
	}
	// -22 has none of -23's markers
	for _, body := range []string{"1c 05 00 0a 00 8080", "1c 05 00 0a 00 840c 00 01"} {
		r := c22.NewFetchReader(stream(hexBytes(t, body)), message.GroupOrderAscending)
		_, _ = r.Next()
		_, err := r.Next()
		wantCode(t, err, message.CodeProtocolViolation, "-22 marker "+body)
	}
}

func TestDraft23ObjectCodes(t *testing.T) {
	c23, c22 := mustCodec(t, 23), mustCodec(t, 22)
	_, err := c23.ReadSubgroupHeader(0x06, stream(nil))
	wantCode(t, err, message.CodeInvalidStreamHeader, "subgroup header type")
	_, err = c22.ReadSubgroupHeader(0x06, stream(nil))
	wantCode(t, err, message.CodeProtocolViolation, "-22 subgroup header type")
	sr, err := c23.ReadSubgroupHeader(0x10, stream(hexBytes(t, "01 02 80 00 00 09")))
	if err != nil {
		t.Fatal(err)
	}
	_, err = sr.Next()
	wantCode(t, err, message.CodeInvalidObjectStatus, "object status")
	_, err = c23.ParseDatagram(hexBytes(t, "10 01 02"))
	wantCode(t, err, message.CodeInvalidStreamHeader, "datagram type")
	_, err = c23.ParseDatagram(hexBytes(t, "20 01 02 00 80 09"))
	wantCode(t, err, message.CodeInvalidObjectStatus, "datagram status")
	_, err = c23.ParseDatagram(hexBytes(t, "05 01 02 80 00"))
	wantCode(t, err, message.CodeMalformedObject, "empty datagram properties")
	_, err = c23.ParseDatagram(hexBytes(t, "80"))
	wantCode(t, err, message.CodeMalformedObject, "cut datagram type")
}

func TestAllowedParamsTable(t *testing.T) {
	// the sets computed once per codec equal those derived from its capabilities
	for _, c := range codecs {
		if c.KnownParams() != c.knownParams() {
			t.Errorf("-%d known parameters", c.draft)
		}
		for k := range message.KindLimit {
			if got, want := c.allowedParams(message.Kind(k)), c.draftParams(message.Kind(k)); got != want {
				t.Errorf("-%d %s: %b, want %b", c.draft, message.Kind(k), got, want)
			}
		}
	}
}

func TestFetchOKParamsUnchanged(t *testing.T) {
	// an older draft drops -23's FETCH_OK parameters from what it writes, not from the message
	var p message.Params
	p.SetLargestObject(wire.Location{Group: 2, Object: 3})
	p.SetExpires(10)
	m := &message.FetchOK{EndLocation: wire.Location{Group: 2, Object: 3}, Params: p}
	for _, d := range []int{22, 23} {
		if _, err := mustCodec(t, d).AppendMessage(nil, m); err != nil {
			t.Fatalf("-%d: %v", d, err)
		}
		if !m.Params.Has(message.ParamLargestObject) || !m.Params.Has(message.ParamExpires) {
			t.Fatalf("-%d changed the message's parameters", d)
		}
	}
	c23 := mustCodec(t, 23)
	buf := make([]byte, 0, 64)
	if n := testing.AllocsPerRun(100, func() { _, _ = c23.AppendMessage(buf[:0], m) }); n != 0 {
		t.Errorf("-23 FETCH_OK allocates %.0f times", n)
	}
}
