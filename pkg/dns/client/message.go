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
	"strings"
)

const (
	headerLen      = 12
	maxSectionLen  = 65535
	packBufSize    = 512
	minRecordLen   = 12
	flagResponse   = 1 << 15
	flagAuthorit   = 1 << 10
	flagTruncated  = 1 << 9
	flagRecurseReq = 1 << 8
	flagRecurseOK  = 1 << 7
	opcodeShift    = 11
	opcodeMask     = 0xF
	rcodeMask      = 0xF
)

// Question is a single entry in a message's question section
type Question struct {
	Name  string
	Type  Type
	Class Class
}

func (q *Question) pack(dst []byte) ([]byte, error) {
	dst, err := packName(dst, q.Name)
	if err != nil {
		return nil, err
	}
	dst = binary.BigEndian.AppendUint16(dst, uint16(q.Type))
	return binary.BigEndian.AppendUint16(dst, uint16(q.Class)), nil
}

func unpackQuestion(msg []byte, off int) (Question, int, error) {
	name, off, err := unpackName(msg, off)
	if err != nil {
		return Question{}, 0, err
	}
	if off+4 > len(msg) {
		return Question{}, 0, ErrShortMessage
	}
	return Question{
		Name:  name,
		Type:  Type(binary.BigEndian.Uint16(msg[off : off+2])),
		Class: Class(binary.BigEndian.Uint16(msg[off+2 : off+4])),
	}, off + 4, nil
}

// Msg is a DNS message. Only the question and answer sections are modeled;
// authority and additional records are not read, as nothing here consumes them.
type Msg struct {
	ID                 uint16
	Opcode             uint8
	RCode              RCode
	Response           bool
	Authoritative      bool
	Truncated          bool
	RecursionDesired   bool
	RecursionAvailable bool
	Questions          []Question
	Answers            []Record
	// UDPSize, when non-zero, packs an EDNS0 OPT record advertising the
	// sender's UDP reassembly buffer, raising the 512-byte response ceiling
	UDPSize uint16
}

// Pack encodes the message into its wire format
func (m *Msg) Pack() ([]byte, error) {
	qdCount := len(m.Questions)
	anCount := len(m.Answers)
	if qdCount > maxSectionLen || anCount > maxSectionLen {
		return nil, ErrLongMessage
	}
	var arCount uint16
	if m.UDPSize > 0 {
		arCount = 1
	}
	b := make([]byte, headerLen, packBufSize)
	binary.BigEndian.PutUint16(b[0:2], m.ID)
	binary.BigEndian.PutUint16(b[2:4], m.flags())
	binary.BigEndian.PutUint16(b[4:6], uint16(qdCount))
	binary.BigEndian.PutUint16(b[6:8], uint16(anCount))
	binary.BigEndian.PutUint16(b[10:12], arCount)
	var err error
	for i := range m.Questions {
		if b, err = m.Questions[i].pack(b); err != nil {
			return nil, err
		}
	}
	for _, rr := range m.Answers {
		if b, err = packRecord(b, rr); err != nil {
			return nil, err
		}
	}
	if arCount > 0 {
		b = packOPT(b, m.UDPSize)
	}
	return b, nil
}

// Unpack decodes a wire-format message. A short answer section is tolerated
// when the truncated bit is set, since the caller retries such answers on TCP.
func (m *Msg) Unpack(b []byte) error {
	if len(b) < headerLen {
		return ErrShortMessage
	}
	m.ID = binary.BigEndian.Uint16(b[0:2])
	m.setFlags(binary.BigEndian.Uint16(b[2:4]))
	qdCount := int(binary.BigEndian.Uint16(b[4:6]))
	anCount := int(binary.BigEndian.Uint16(b[6:8]))
	off := headerLen
	m.Questions = make([]Question, 0, sectionCap(qdCount, len(b)))
	for range qdCount {
		q, next, err := unpackQuestion(b, off)
		if err != nil {
			return err
		}
		m.Questions = append(m.Questions, q)
		off = next
	}
	m.Answers = make([]Record, 0, sectionCap(anCount, len(b)))
	for range anCount {
		rr, next, err := unpackRecord(b, off)
		if err != nil {
			if m.Truncated {
				return nil
			}
			return err
		}
		m.Answers = append(m.Answers, rr)
		off = next
	}
	return nil
}

// sectionCap bounds a section's preallocation by what the message could
// actually hold, so a bogus count cannot drive a large allocation
func sectionCap(count, msgLen int) int {
	return min(count, msgLen/minRecordLen+1)
}

func (m *Msg) flags() uint16 {
	f := uint16(m.Opcode&opcodeMask)<<opcodeShift | uint16(m.RCode)&rcodeMask
	if m.Response {
		f |= flagResponse
	}
	if m.Authoritative {
		f |= flagAuthorit
	}
	if m.Truncated {
		f |= flagTruncated
	}
	if m.RecursionDesired {
		f |= flagRecurseReq
	}
	if m.RecursionAvailable {
		f |= flagRecurseOK
	}
	return f
}

func (m *Msg) setFlags(f uint16) {
	m.Response = f&flagResponse != 0
	m.Opcode = uint8(f >> opcodeShift & opcodeMask)
	m.Authoritative = f&flagAuthorit != 0
	m.Truncated = f&flagTruncated != 0
	m.RecursionDesired = f&flagRecurseReq != 0
	m.RecursionAvailable = f&flagRecurseOK != 0
	m.RCode = RCode(f & rcodeMask)
}

// packOPT appends the EDNS0 pseudo-record, which carries the advertised UDP
// buffer size in the class field of a record on the root name
func packOPT(dst []byte, udpSize uint16) []byte {
	dst = append(dst, 0)
	dst = binary.BigEndian.AppendUint16(dst, uint16(TypeOPT))
	dst = binary.BigEndian.AppendUint16(dst, udpSize)
	dst = binary.BigEndian.AppendUint32(dst, 0)
	return binary.BigEndian.AppendUint16(dst, 0)
}

// answers reports whether m is a reply to the question asked in q
func (m *Msg) answers(q *Msg) bool {
	if !m.Response || m.ID != q.ID || len(m.Questions) != len(q.Questions) {
		return false
	}
	for i, asked := range q.Questions {
		got := m.Questions[i]
		if got.Type != asked.Type || got.Class != asked.Class ||
			!strings.EqualFold(got.Name, asked.Name) {
			return false
		}
	}
	return true
}
