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

func testQuestion() Question {
	return Question{Name: "example.com.", Type: TypeA, Class: ClassINET}
}

func TestMsgRoundTrip(t *testing.T) {
	m := &Msg{
		ID:                 0xbeef,
		Response:           true,
		Authoritative:      true,
		Truncated:          true,
		RecursionDesired:   true,
		RecursionAvailable: true,
		Opcode:             2,
		RCode:              RCodeRefused,
		Questions:          []Question{testQuestion()},
		Answers: []Record{&A{
			Hdr:  RecordHeader{Name: "example.com.", Type: TypeA, Class: ClassINET, TTL: 300},
			Addr: netip.MustParseAddr("10.0.0.1"),
		}},
	}
	b, err := m.Pack()
	require.NoError(t, err)

	got := &Msg{}
	require.NoError(t, got.Unpack(b))
	require.Equal(t, m.ID, got.ID)
	require.True(t, got.Response)
	require.True(t, got.Authoritative)
	require.True(t, got.Truncated)
	require.True(t, got.RecursionDesired)
	require.True(t, got.RecursionAvailable)
	require.Equal(t, uint8(2), got.Opcode)
	require.Equal(t, RCodeRefused, got.RCode)
	require.Equal(t, m.Questions, got.Questions)
	require.Len(t, got.Answers, 1)
	require.Equal(t, m.Answers[0], got.Answers[0])
}

func TestMsgPackEDNS0(t *testing.T) {
	m := &Msg{ID: 1, Questions: []Question{testQuestion()}, UDPSize: DefaultUDPSize}
	b, err := m.Pack()
	require.NoError(t, err)
	require.Equal(t, uint16(1), binary.BigEndian.Uint16(b[10:12]),
		"the OPT record is counted in the additional section")
	opt := b[len(b)-11:]
	require.Equal(t, byte(0), opt[0], "OPT is attached to the root name")
	require.Equal(t, uint16(TypeOPT), binary.BigEndian.Uint16(opt[1:3]))
	require.Equal(t, uint16(DefaultUDPSize), binary.BigEndian.Uint16(opt[3:5]),
		"the advertised buffer size rides in the class field")

	// the additional section is not modeled, so it round-trips as absent
	got := &Msg{}
	require.NoError(t, got.Unpack(b))
	require.Zero(t, got.UDPSize)
	require.Empty(t, got.Answers)
}

func TestMsgPackErrors(t *testing.T) {
	m := &Msg{Questions: []Question{{Name: "a..b.", Type: TypeA, Class: ClassINET}}}
	_, err := m.Pack()
	require.ErrorIs(t, err, ErrInvalidName)

	m = &Msg{Answers: []Record{&A{
		Hdr:  RecordHeader{Name: "example.com.", Type: TypeA},
		Addr: netip.MustParseAddr("::1"),
	}}}
	_, err = m.Pack()
	require.ErrorIs(t, err, ErrInvalidRecord)

	m = &Msg{Answers: []Record{&Unknown{
		Hdr:  RecordHeader{Name: "example.com.", Type: 99},
		Data: make([]byte, maxRDataLen+1),
	}}}
	_, err = m.Pack()
	require.ErrorIs(t, err, ErrLongMessage)
}

func TestMsgUnpackErrors(t *testing.T) {
	m := &Msg{}
	require.ErrorIs(t, m.Unpack(make([]byte, headerLen-1)), ErrShortMessage)

	// a question count with no question section behind it
	b := make([]byte, headerLen)
	binary.BigEndian.PutUint16(b[4:6], 1)
	require.ErrorIs(t, m.Unpack(b), ErrShortMessage)

	// a name that unpacks but has no type/class following it
	b = append(b, 0)
	require.ErrorIs(t, m.Unpack(b), ErrShortMessage)

	// an answer count with no answer section behind it
	b = make([]byte, headerLen)
	binary.BigEndian.PutUint16(b[6:8], 1)
	require.ErrorIs(t, m.Unpack(b), ErrShortMessage)
}

// TestMsgUnpackTruncated covers servers that set the truncated bit while
// leaving the section counts describing the full, unsent answer
func TestMsgUnpackTruncated(t *testing.T) {
	b := make([]byte, headerLen)
	binary.BigEndian.PutUint16(b[2:4], flagResponse|flagTruncated)
	binary.BigEndian.PutUint16(b[6:8], 4)
	m := &Msg{}
	require.NoError(t, m.Unpack(b))
	require.True(t, m.Truncated)
	require.Empty(t, m.Answers)
}

func TestMsgAnswers(t *testing.T) {
	q := &Msg{ID: 7, Questions: []Question{testQuestion()}}
	reply := func(mutate func(*Msg)) *Msg {
		m := &Msg{ID: 7, Response: true, Questions: []Question{testQuestion()}}
		if mutate != nil {
			mutate(m)
		}
		return m
	}
	require.True(t, reply(nil).answers(q))
	require.True(t, reply(func(m *Msg) {
		m.Questions[0].Name = strings.ToUpper(m.Questions[0].Name)
	}).answers(q), "question names compare case-insensitively")

	require.False(t, reply(func(m *Msg) { m.Response = false }).answers(q))
	require.False(t, reply(func(m *Msg) { m.ID = 8 }).answers(q))
	require.False(t, reply(func(m *Msg) { m.Questions = nil }).answers(q))
	require.False(t, reply(func(m *Msg) { m.Questions[0].Type = TypeSRV }).answers(q))
	require.False(t, reply(func(m *Msg) { m.Questions[0].Class = 3 }).answers(q))
	require.False(t, reply(func(m *Msg) { m.Questions[0].Name = "other.com." }).answers(q))
}

func TestSectionCap(t *testing.T) {
	require.Equal(t, 2, sectionCap(2, 512))
	require.Equal(t, 5, sectionCap(65535, headerLen*4),
		"a bogus section count cannot drive a large allocation")
}
