/*
 * Copyright 2018 The Trickster Authors
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

package client

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// packedSeed packs a message that the seed corpus needs, failing the fuzz
// setup rather than the fuzz run when the fixture itself is wrong
func packedSeed(f *testing.F, m *Msg) []byte {
	f.Helper()
	b, err := m.Pack()
	require.NoError(f, err)
	return b
}

func packedRecordSeed(f *testing.F, rr Record) []byte {
	f.Helper()
	b, err := packRecord(nil, rr)
	require.NoError(f, err)
	return b
}

func seedHeader(qdCount, anCount uint16, flags uint16) []byte {
	b := make([]byte, headerLen)
	binary.BigEndian.PutUint16(b[0:2], 0x1234)
	binary.BigEndian.PutUint16(b[2:4], flags)
	binary.BigEndian.PutUint16(b[4:6], qdCount)
	binary.BigEndian.PutUint16(b[6:8], anCount)
	return b
}

// compressedResponse is the shape real servers send: the answer's name is a
// pointer back to the question rather than a repeated set of labels
func compressedResponse() []byte {
	b := seedHeader(1, 1, flagResponse|flagRecurseReq|flagRecurseOK)
	b = append(b, "\x07example\x03com\x00"...)
	b = binary.BigEndian.AppendUint16(b, uint16(TypeA))
	b = binary.BigEndian.AppendUint16(b, uint16(ClassINET))
	b = append(b, 0xc0, headerLen) // pointer to the question's name
	b = binary.BigEndian.AppendUint16(b, uint16(TypeA))
	b = binary.BigEndian.AppendUint16(b, uint16(ClassINET))
	b = binary.BigEndian.AppendUint32(b, 60)
	b = binary.BigEndian.AppendUint16(b, 4)
	return append(b, 10, 0, 0, 1)
}

func seedNames() [][]byte {
	return [][]byte{
		[]byte("\x07example\x03com\x00"),
		[]byte("\x03com\x00\x03foo\xc0\x00"),
		{0},
		{4, 'a', '.', 'b', 0x01, 0},
		{0xc0, 0x00},
		{0xc0, 0xff},
		{0xc0},
		{0x40},
		{0x03, 'a'},
		{0x01, 'a'},
		overlongName(),
	}
}

// overlongName is five maximum-length labels, which overruns the 255-byte
// ceiling on a whole name
func overlongName() []byte {
	var b []byte
	for range 5 {
		b = append(b, maxLabelLen)
		b = append(b, strings.Repeat("a", maxLabelLen)...)
	}
	return append(b, 0)
}

func seedRecords(f *testing.F) [][]byte {
	f.Helper()
	hdr := func(qtype Type) RecordHeader {
		return RecordHeader{Name: "example.com.", Type: qtype, Class: ClassINET, TTL: 60}
	}
	return [][]byte{
		packedRecordSeed(f, &A{Hdr: hdr(TypeA), Addr: netip.MustParseAddr("10.0.0.1")}),
		packedRecordSeed(f, &AAAA{Hdr: hdr(TypeAAAA), Addr: netip.MustParseAddr("2001:db8::1")}),
		packedRecordSeed(f, &SRV{Hdr: hdr(TypeSRV), Priority: 10, Weight: 5,
			Port: 9090, Target: "prom-a.example.com."}),
		packedRecordSeed(f, &Unknown{Hdr: hdr(99), Data: []byte{1, 2, 3}}),
	}
}

// FuzzUnpackName drives name decoding, where compression pointers let a
// hostile message steer the decoder's offset
func FuzzUnpackName(f *testing.F) {
	for _, seed := range seedNames() {
		f.Add(seed, 0)
	}
	f.Add([]byte("\x03com\x00\x03foo\xc0\x00"), 5)
	f.Add([]byte{0}, 9)
	f.Add([]byte{0}, -1)
	f.Add(compressedResponse(), headerLen)

	f.Fuzz(func(t *testing.T, msg []byte, off int) {
		name, next, err := unpackName(msg, off)
		if err != nil {
			return
		}
		require.GreaterOrEqual(t, next, 0)
		require.LessOrEqual(t, next, len(msg),
			"the resume offset must stay inside the message")
		require.True(t, strings.HasSuffix(name, "."),
			"decoded names are always fully qualified")
		require.LessOrEqual(t, len(name), maxNameLen*4,
			"escaping expands a byte to at most four characters")
	})
}

// FuzzUnpackRecord drives resource record decoding for the types the client
// interprets, plus the raw carry-through of every other type
func FuzzUnpackRecord(f *testing.F) {
	for _, seed := range seedRecords(f) {
		f.Add(seed, 0)
	}
	for _, seed := range seedNames() {
		f.Add(seed, 0)
	}
	f.Add(compressedResponse(), headerLen)
	f.Add(compressedResponse(), -1)

	f.Fuzz(func(t *testing.T, msg []byte, off int) {
		rr, next, err := unpackRecord(msg, off)
		if err != nil {
			return
		}
		require.NotNil(t, rr)
		require.GreaterOrEqual(t, next, 0)
		require.LessOrEqual(t, next, len(msg),
			"the resume offset must stay inside the message")
		require.Greater(t, next, off,
			"a decoded record must advance past its own bytes")
		require.True(t, strings.HasSuffix(rr.Header().Name, "."))
	})
}

// FuzzMsgUnpack drives the whole response decoder, which is the code path
// that reads bytes straight off the network
func FuzzMsgUnpack(f *testing.F) {
	full := &Msg{
		ID:                 0xbeef,
		Response:           true,
		RecursionDesired:   true,
		RecursionAvailable: true,
		Questions:          []Question{{Name: "example.com.", Type: TypeA, Class: ClassINET}},
		Answers: []Record{
			&A{
				Hdr:  RecordHeader{Name: "example.com.", Type: TypeA, Class: ClassINET, TTL: 300},
				Addr: netip.MustParseAddr("10.0.0.1"),
			},
			&AAAA{
				Hdr:  RecordHeader{Name: "example.com.", Type: TypeAAAA, Class: ClassINET, TTL: 120},
				Addr: netip.MustParseAddr("2001:db8::1"),
			},
			&SRV{
				Hdr:    RecordHeader{Name: "_prom._tcp.example.com.", Type: TypeSRV, Class: ClassINET, TTL: 90},
				Port:   9090,
				Target: "prom-a.example.com.",
			},
		},
	}
	f.Add(packedSeed(f, full))

	edns := &Msg{
		ID:        1,
		Questions: []Question{{Name: "example.com.", Type: TypeA, Class: ClassINET}},
		UDPSize:   DefaultUDPSize,
	}
	f.Add(packedSeed(f, edns))
	f.Add(compressedResponse())
	f.Add(seedHeader(1, 0, 0))
	f.Add(append(seedHeader(1, 0, 0), 0))
	f.Add(seedHeader(0, 1, 0))
	f.Add(seedHeader(0, 4, flagResponse|flagTruncated))
	f.Add(make([]byte, headerLen-1))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, wire []byte) {
		m := &Msg{}
		if err := m.Unpack(wire); err != nil {
			return
		}
		for _, q := range m.Questions {
			require.True(t, strings.HasSuffix(q.Name, "."))
		}
		for _, rr := range m.Answers {
			require.NotNil(t, rr)
			require.True(t, strings.HasSuffix(rr.Header().Name, "."))
		}
		// re-encoding a decoded message must fail cleanly rather than panic;
		// names carrying escapes are legitimately not re-encodable
		if b, err := m.Pack(); err == nil {
			require.NoError(t, (&Msg{}).Unpack(b))
		}
	})
}
