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
	"errors"
	"io"
	"math"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func stream(b []byte) *bufio.Reader {
	return bufio.NewReader(bytes.NewReader(b))
}

func TestClassifyStream(t *testing.T) {
	c := mustCodec(t, 22)
	tests := []struct {
		t    uint64
		want StreamKind
	}{
		{StreamTypeControl, StreamControl},
		{StreamTypeFetch, StreamFetch},
		{StreamTypePadding, StreamPadding},
		{0x10, StreamSubgroup},
		{0x7d, StreamSubgroup},
		{0x16, StreamUnknown}, // subgroup ID mode 0b11
		{0x0f, StreamUnknown}, // bit 4 clear
		{0x90, StreamUnknown}, // 128 or above
		{0x2f01, StreamUnknown},
	}
	for _, tc := range tests {
		if got := c.ClassifyStream(tc.t); got != tc.want {
			t.Errorf("%#x: got %v, want %v", tc.t, got, tc.want)
		}
	}
}

func TestSubgroupStream(t *testing.T) {
	c := mustCodec(t, 22)
	for _, h := range []message.SubgroupHeader{
		{TrackAlias: 3, Group: 1 << 33, Mode: message.SubgroupIDExplicit, Subgroup: 9, Priority: 7, FirstObject: true},
		{TrackAlias: 3, Group: 2, Mode: message.SubgroupIDZero, DefaultPriority: true, HasProperties: true, EndOfGroup: true},
		{TrackAlias: 3, Group: 2, Mode: message.SubgroupIDFirstObject, Subgroup: 5},
	} {
		w := c.NewSubgroupWriter(h)
		b := c.AppendSubgroupHeader(nil, &h)
		first := uint64(0)
		if h.Mode == message.SubgroupIDFirstObject {
			first = h.Subgroup
		}
		objs := []message.SubgroupObject{
			{ID: first, PayloadLen: 4},
			{ID: first + 1, PayloadLen: 2},
			{ID: first + 10, PayloadLen: 0, Status: message.StatusEndOfGroup},
		}
		if h.HasProperties {
			objs[1].Properties = message.Properties{0x3e, 0x02}
		}
		var err error
		for i := range objs {
			if b, err = w.AppendObject(b, &objs[i]); err != nil {
				t.Fatal(err)
			}
			b = append(b, make([]byte, objs[i].PayloadLen)...)
		}
		r := stream(b)
		typ, err := wire.ReadVarintFrom(r)
		if err != nil || c.ClassifyStream(typ) != StreamSubgroup {
			t.Fatalf("stream type %#x %v", typ, err)
		}
		sr, err := c.ReadSubgroupHeader(typ, r)
		if err != nil {
			t.Fatal(err)
		}
		got := *sr.Header()
		want := h
		if !c.Has(CapFirstObjectBit) {
			want.FirstObject = false
		}
		if got.Mode != message.SubgroupIDFirstObject && got != want {
			t.Errorf("header %+v, want %+v", got, want)
		}
		for i := range objs {
			o, err := sr.Next()
			if err != nil {
				t.Fatalf("object %d: %v", i, err)
			}
			if o.ID != objs[i].ID || o.PayloadLen != objs[i].PayloadLen || o.Status != objs[i].Status ||
				!bytes.Equal(o.Properties, objs[i].Properties) {
				t.Errorf("object %d: %+v, want %+v", i, o, objs[i])
			}
			if _, err := io.CopyN(io.Discard, r, int64(o.PayloadLen)); err != nil {
				t.Fatal(err)
			}
		}
		if h.Mode == message.SubgroupIDFirstObject && sr.Header().Subgroup != first {
			t.Errorf("subgroup from first object %d", sr.Header().Subgroup)
		}
		if _, err := sr.Next(); !errors.Is(err, io.EOF) {
			t.Errorf("a clean end should be io.EOF, got %v", err)
		}
	}
}

func TestSubgroupWriterRefusals(t *testing.T) {
	c := mustCodec(t, 22)
	w := c.NewSubgroupWriter(message.SubgroupHeader{})
	if _, err := w.AppendObject(nil, &message.SubgroupObject{Properties: message.Properties{1}}); !errors.Is(err, ErrObjectFields) {
		t.Error("properties need the header's properties bit")
	}
	if _, err := w.AppendObject(nil, &message.SubgroupObject{PayloadLen: 1, Status: message.StatusEndOfTrack}); !errors.Is(err, ErrObjectFields) {
		t.Error("only a normal object has a payload")
	}
	if _, err := w.AppendObject(nil, &message.SubgroupObject{ID: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.AppendObject(nil, &message.SubgroupObject{ID: 5}); !errors.Is(err, ErrObjectOrder) {
		t.Error("object IDs must ascend")
	}
	fw := c.NewSubgroupWriter(message.SubgroupHeader{Mode: message.SubgroupIDFirstObject, Subgroup: 3})
	if _, err := fw.AppendObject(nil, &message.SubgroupObject{ID: 4}); !errors.Is(err, ErrObjectFields) {
		t.Error("the first object must carry the subgroup's ID")
	}
}

func TestSubgroupViolations(t *testing.T) {
	c := mustCodec(t, 22)
	if _, err := c.ReadSubgroupHeader(0x16, stream(nil)); err == nil {
		t.Error("an invalid header type should fail")
	}
	cut := [][]byte{{}, {0x01}, {0x01, 0x02}, {0x01, 0x02, 0x03}}
	for _, b := range cut {
		// explicit subgroup and a priority byte make four fields to cut short
		if _, err := c.ReadSubgroupHeader(0x14, stream(b)); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%x: a cut header should be io.ErrUnexpectedEOF, got %v", b, err)
		}
	}
	read := func(flags uint64, body string) error {
		sr, err := c.ReadSubgroupHeader(flags, stream(hexBytes(t, "01 02 "+body)))
		if err != nil {
			return err
		}
		for {
			o, err := sr.Next()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			_ = o
		}
	}
	const props = 0x31 // properties, default priority, bit 4
	tests := []struct {
		name  string
		flags uint64
		body  string
	}{
		{"unknown status", 0x30, "00 00 07"},
		{"properties on a status object", props, "00 02 3e01 00 03"},
		{"object ID overflow", 0x30, "ffffffffffffffffff 01 00 00 01 00"},
		{"cut after delta", 0x30, "00"},
		{"cut payload length", 0x30, "00 80"},
		{"cut status", 0x30, "00 00"},
		{"cut properties", props, "00 05 3e"},
		{"properties too long", props, "00 c0400001"},
	}
	for _, tc := range tests {
		if err := read(tc.flags, tc.body); err == nil {
			t.Errorf("%s: should fail", tc.name)
		}
	}
}

func TestDatagrams(t *testing.T) {
	c := mustCodec(t, 22)
	tests := []message.Datagram{
		{TrackAlias: 1, Group: 2, Object: 3, Priority: 9, Payload: []byte("hello")},
		{TrackAlias: 1, Group: 2, Object: 0, DefaultPriority: true, EndOfGroup: true, Payload: []byte{}},
		{TrackAlias: 1, Group: 2, Object: 4, Properties: message.Properties{0x3c, 0x01}, Payload: []byte("x")},
		{TrackAlias: 1, Group: 2, Object: 5, HasStatus: true, Status: message.StatusEndOfTrack},
		{
			TrackAlias: 1, Group: 2, Object: 6, HasStatus: true, Status: message.StatusNormal,
			Properties: message.Properties{0x3c, 0x01},
		},
	}
	for _, d := range tests {
		b, err := c.AppendDatagram(nil, &d)
		if err != nil {
			t.Fatal(err)
		}
		if c.IsPaddingDatagram(b) {
			t.Error("an object datagram is not padding")
		}
		got, err := c.ParseDatagram(b)
		if err != nil {
			t.Fatalf("%+v: %v", d, err)
		}
		if got.TrackAlias != d.TrackAlias || got.Group != d.Group || got.Object != d.Object ||
			got.Priority != d.Priority || got.DefaultPriority != d.DefaultPriority || got.EndOfGroup != d.EndOfGroup ||
			got.HasStatus != d.HasStatus || got.Status != d.Status || !bytes.Equal(got.Payload, d.Payload) ||
			!bytes.Equal(got.Properties, d.Properties) {
			t.Errorf("got %+v, want %+v", got, d)
		}
	}
	if !c.IsPaddingDatagram(wire.AppendVarint(nil, DatagramTypePadding)) {
		t.Error("padding datagram")
	}
	refused := []message.Datagram{
		{HasStatus: true, EndOfGroup: true},
		{HasStatus: true, Payload: []byte("x")},
		{HasStatus: true, Status: message.StatusEndOfGroup, Properties: message.Properties{1}},
	}
	for _, d := range refused {
		if _, err := c.AppendDatagram(nil, &d); !errors.Is(err, ErrObjectFields) {
			t.Errorf("%+v should not encode", d)
		}
	}
	bad := []string{
		"",
		"22 01 02 03",            // status with end of group
		"10 01 02 03",            // reserved bit 4
		"40 01 02 03",            // undefined bit
		"8000 01 02",             // undefined two-byte type
		"21 01 02 03 00 00",      // properties declared empty
		"20 01 02 03 09",         // unknown status
		"21 01 02 03 02 3c01 03", // properties on an end of group status
		"20 01 02 03 00 ff",      // trailing after status
		"00 01",                  // cut before group
	}
	for _, s := range bad {
		if _, err := c.ParseDatagram(hexBytes(t, s)); err == nil {
			t.Errorf("%q should fail", s)
		}
	}
}

func fetchRoundTrip(t *testing.T, c *Codec, order message.GroupOrder, objs []message.FetchObject) []byte {
	t.Helper()
	w := c.NewFetchWriter(order)
	b := c.AppendFetchHeader(nil, 11)
	var err error
	for i := range objs {
		if b, err = w.AppendObject(b, &objs[i]); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		if objs[i].EndOfRange == message.RangeEndNone {
			b = append(b, make([]byte, objs[i].PayloadLen)...)
		}
	}
	r := stream(b)
	typ, _ := wire.ReadVarintFrom(r)
	if c.ClassifyStream(typ) != StreamFetch {
		t.Fatalf("stream type %#x", typ)
	}
	if id, err := c.ReadFetchHeader(r); err != nil || id != 11 {
		t.Fatalf("request %d %v", id, err)
	}
	fr := c.NewFetchReader(r, order)
	for i := range objs {
		got, err := fr.Next()
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		if got.EndOfRange != objs[i].EndOfRange || got.Group != objs[i].Group || got.Object != objs[i].Object {
			t.Errorf("entry %d: %+v, want %+v", i, got, objs[i])
		}
		if got.EndOfRange == message.RangeEndNone {
			if got.Subgroup != objs[i].Subgroup || got.Datagram != objs[i].Datagram ||
				got.Priority != objs[i].Priority || got.PayloadLen != objs[i].PayloadLen ||
				!bytes.Equal(got.Properties, objs[i].Properties) {
				t.Errorf("entry %d: %+v, want %+v", i, got, objs[i])
			}
			if _, err := io.CopyN(io.Discard, r, int64(got.PayloadLen)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := fr.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("a clean end should be io.EOF, got %v", err)
	}
	return b
}

func TestFetchStreamAscending(t *testing.T) {
	c := mustCodec(t, 22)
	fetchRoundTrip(t, c, message.GroupOrderAscending, []message.FetchObject{
		{Group: 5, Object: 0, Subgroup: 0, Priority: 10, PayloadLen: 3},
		{Group: 5, Object: 1, Subgroup: 0, Priority: 10, PayloadLen: 1},    // inherits everything
		{Group: 5, Object: 4, Subgroup: 1, Priority: 10, PayloadLen: 1},    // object delta, subgroup +1
		{Group: 5, Object: 5, Subgroup: 1, Priority: 20, PayloadLen: 0},    // priority change
		{EndOfRange: message.RangeEndUnknown, Group: 5, Object: 9},         // marker within the group
		{Group: 5, Object: 10, Subgroup: 1, Priority: 20, PayloadLen: 2},   // follows the marker
		{EndOfRange: message.RangeEndNonExistent, Group: 7, Object: 0},     // marker in a later group
		{Group: 8, Object: 1, Subgroup: 7, Priority: 20, PayloadLen: 1},    // group delta, explicit subgroup
		{Group: 9, Object: 2, Datagram: true, Priority: 20, PayloadLen: 1}, // datagram, object = prior + 1
		{Group: 9, Object: 3, Subgroup: 7, Priority: 20, PayloadLen: 1, // subgroup from the last subgroup object
			Properties: message.Properties{0x3e, 0x01}},
		{EndOfRange: message.RangeEndTimedOut, Group: 12, Object: 0},
	})
}

func TestFetchStreamDescending(t *testing.T) {
	c := mustCodec(t, 22)
	fetchRoundTrip(t, c, message.GroupOrderDescending, []message.FetchObject{
		{Group: 9, Object: 0, Priority: 1, PayloadLen: 1},
		{Group: 9, Object: 3, Priority: 1, PayloadLen: 1},
		{Group: 6, Object: 0, Priority: 1, PayloadLen: 1},
		{Group: 5, Object: 1, Priority: 1, PayloadLen: 1},
	})
	// the default order is ascending
	fetchRoundTrip(t, c, message.GroupOrderUnset, []message.FetchObject{
		{Group: 1, Object: 0, Priority: 1},
		{Group: 2, Object: 0, Priority: 1},
	})
}

func TestFetchWriterRefusals(t *testing.T) {
	c := mustCodec(t, 22)
	w := c.NewFetchWriter(message.GroupOrderAscending)
	if _, err := w.AppendObject(nil, &message.FetchObject{Group: 5, Object: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.AppendObject(nil, &message.FetchObject{Group: 5, Object: 5}); !errors.Is(err, ErrObjectOrder) {
		t.Error("objects within a group must ascend")
	}
	if _, err := w.AppendObject(nil, &message.FetchObject{Group: 4}); !errors.Is(err, ErrObjectOrder) {
		t.Error("groups must follow the stream's order")
	}
	if _, err := w.AppendObject(nil, &message.FetchObject{EndOfRange: 9}); !errors.Is(err, ErrObjectFields) {
		t.Error("an unknown marker should not encode")
	}
	d := c.NewFetchWriter(message.GroupOrderDescending)
	_, _ = d.AppendObject(nil, &message.FetchObject{Group: 5})
	if _, err := d.AppendObject(nil, &message.FetchObject{Group: 6}); !errors.Is(err, ErrObjectOrder) {
		t.Error("a descending stream's groups must descend")
	}
	// an object after MaxUint64 in a new group must carry its ID explicitly
	m := c.NewFetchWriter(message.GroupOrderAscending)
	b, _ := m.AppendObject(nil, &message.FetchObject{Group: 1, Object: math.MaxUint64})
	b, err := m.AppendObject(b, &message.FetchObject{Group: 2, Object: 0})
	if err != nil {
		t.Fatal(err)
	}
	r := c.NewFetchReader(stream(b), message.GroupOrderAscending)
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	if o, err := r.Next(); err != nil || o.Group != 2 || o.Object != 0 {
		t.Errorf("got %+v %v", o, err)
	}
}

func TestFetchViolations(t *testing.T) {
	c := mustCodec(t, 22)
	tests := []struct {
		name  string
		order message.GroupOrder
		body  string
	}{
		{"unknown range value", message.GroupOrderAscending, "8d01 00 00"},
		{"first refers to prior group", message.GroupOrderAscending, "14 00 05 00"},
		{"first refers to prior priority", message.GroupOrderAscending, "0c 00 00 00"},
		{"first refers to prior subgroup", message.GroupOrderAscending, "1d 00 00 05 00"},
		{"descending underflow", message.GroupOrderDescending, "1c 02 00 05 00 08 05 00"},
		{"ascending overflow", message.GroupOrderAscending, "1c ffffffffffffffffff 00 05 00 08 00 00"},
		{"subgroup overflow", message.GroupOrderAscending, "1f 00 ffffffffffffffffff 00 05 00 02 00"},
		{"object overflow delta", message.GroupOrderAscending, "1c 00 ffffffffffffffffff 05 00 04 01 00"},
		{"object overflow implicit", message.GroupOrderAscending, "1c 00 ffffffffffffffffff 05 00 00 00"},
		{"cut flags", message.GroupOrderAscending, "80"},
		{"cut marker", message.GroupOrderAscending, "808c"},
		{"cut marker object", message.GroupOrderAscending, "808c 01"},
		{"cut group", message.GroupOrderAscending, "1c"},
		{"cut subgroup", message.GroupOrderAscending, "1f 00"},
		{"cut object", message.GroupOrderAscending, "1c 00"},
		{"cut priority", message.GroupOrderAscending, "1c 00 00"},
		{"cut properties", message.GroupOrderAscending, "3c 00 00 05 03"},
		{"cut payload length", message.GroupOrderAscending, "1c 00 00 05"},
	}
	for _, tc := range tests {
		r := c.NewFetchReader(stream(hexBytes(t, tc.body)), tc.order)
		var err error
		for err == nil {
			_, err = r.Next()
		}
		if errors.Is(err, io.EOF) {
			t.Errorf("%s: should fail", tc.name)
		}
	}
	// after a marker with no prior object, referring to a prior priority is a violation
	r := c.NewFetchReader(stream(hexBytes(t, "808c 00 03 04 00")), message.GroupOrderAscending)
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Next(); err == nil {
		t.Error("an object after a lone marker cannot inherit a priority")
	}
}

func FuzzParseMessage(f *testing.F) {
	c := mustCodec(f, 22)
	seed, _ := c.AppendMessage(nil, &message.Subscribe{Track: track("v", "live"), Params: fullParams()})
	f.Add(uint64(0x03), seed[3:])
	f.Add(uint64(0x2f00), []byte{0x01, 0x02, 0x2f, 0x61})
	f.Fuzz(func(t *testing.T, typ uint64, body []byte) {
		m, err := c.ParseMessage(typ, body)
		if err != nil {
			return
		}
		// anything accepted re-encodes, and re-decodes to the same encoding
		b, err := c.AppendMessage(nil, m)
		if err != nil {
			return
		}
		typ2, n := wire.ReadVarint(b)
		m2, err := c.ParseMessage(typ2, b[n+2:])
		if err != nil {
			t.Fatalf("re-encoded %s does not decode: %v", m.Kind(), err)
		}
		b2, err := c.AppendMessage(nil, m2)
		if err != nil || !bytes.Equal(b, b2) {
			t.Fatalf("%s encoding is not stable", m.Kind())
		}
	})
}

func FuzzFetchReader(f *testing.F) {
	f.Add(hexBytes(f, "1c 00 00 05 00 00 01 00"))
	f.Add(hexBytes(f, "808c 00 03 04 00"))
	c := mustCodec(f, 22)
	f.Fuzz(func(t *testing.T, b []byte) {
		r := c.NewFetchReader(stream(b), message.GroupOrderAscending)
		for range 64 {
			o, err := r.Next()
			if err != nil {
				return
			}
			if o.EndOfRange == message.RangeEndNone && o.PayloadLen > uint64(len(b)) {
				return
			}
		}
	})
}

func FuzzParseDatagram(f *testing.F) {
	f.Add(hexBytes(f, "00 01 02 03 09 68656c6c6f"))
	f.Add(hexBytes(f, "20 01 02 03 04"))
	c := mustCodec(f, 22)
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := c.ParseDatagram(b)
		if err != nil {
			return
		}
		out, err := c.AppendDatagram(nil, &d)
		if err != nil {
			t.Fatalf("accepted datagram does not re-encode: %v", err)
		}
		d2, err := c.ParseDatagram(out)
		if err != nil || !bytes.Equal(d2.Payload, d.Payload) || d2.Object != d.Object || d2.Status != d.Status {
			t.Fatalf("datagram not stable: %v", err)
		}
	})
}
