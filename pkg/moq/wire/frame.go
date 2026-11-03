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

package wire

import (
	"encoding/binary"
	"errors"
	"io"
	"slices"
)

// MaxMessageLen bounds a control message body: its length field is 16 bits through draft -22, and from -23 it is
// the default MAX_CONTROL_MESSAGE_SIZE, which this implementation never raises
const MaxMessageLen = 1<<16 - 1

const (
	// varintLenSlot is the width reserved for a varint control message length, which fits bodies of 128 to 16383
	// bytes in place; MaxMessageLen needs at most one byte more
	varintLenSlot = 2
	// bodyChunk is how much of a control message body is allocated ahead of the bytes that fill it
	bodyChunk = MaxMessageLen + 1
)

// ErrMessageTooLong is returned when a control message body exceeds MaxMessageLen
var ErrMessageTooLong = errors.New("moq: control message too long")

// StreamReader is a buffered stream that messages and objects are read from
type StreamReader interface {
	io.Reader
	io.ByteReader
}

// BeginMessage appends a control message type and a placeholder length, returning where the length sits
func BeginMessage(b []byte, typ uint64) ([]byte, int) {
	b = AppendVarint(b, typ)
	return append(b, 0, 0), len(b)
}

// EndMessage writes the length of the body appended since BeginMessage returned at
func EndMessage(b []byte, at int) ([]byte, error) {
	n := len(b) - at - 2
	if n > MaxMessageLen {
		return b, ErrMessageTooLong
	}
	binary.BigEndian.PutUint16(b[at:], uint16(n)) // #nosec G115 -- n is at most MaxMessageLen
	return b, nil
}

// BeginVarintMessage appends a control message type and a placeholder varint length, returning where it sits
func BeginVarintMessage(b []byte, typ uint64) ([]byte, int) {
	b = AppendVarint(b, typ)
	return append(b, 0, 0), len(b)
}

// EndVarintMessage writes the shortest length of the body appended since BeginVarintMessage returned at. Most bodies
// fit the reserved slot in place; one under 128 bytes moves back a byte, and one over 16383 moves forward a byte.
func EndVarintMessage(b []byte, at int) ([]byte, error) {
	n := len(b) - at - varintLenSlot
	if n > MaxMessageLen {
		return b, ErrMessageTooLong
	}
	v := uint64(n) // #nosec G115 -- a length is never negative
	ln := VarintLen(v)
	switch {
	case ln < varintLenSlot:
		copy(b[at+ln:], b[at+varintLenSlot:])
		b = b[:len(b)-(varintLenSlot-ln)]
	case ln > varintLenSlot:
		// ln is varintLenSlot+1, since MaxMessageLen fits three bytes
		b = append(b, 0)
		copy(b[at+ln:], b[at+varintLenSlot:len(b)-1])
	}
	appendVarintLen(b[at:at], v, ln)
	return b, nil
}

// ReadVarintFrom reads one variable-length integer from r; io.EOF before its first byte is returned as is,
// and an end within it as io.ErrUnexpectedEOF
func ReadVarintFrom(r io.ByteReader) (uint64, error) {
	b0, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	if b0 < 0x80 {
		return uint64(b0), nil
	}
	var tmp [MaxVarintLen]byte
	tmp[0] = b0
	n := VarintLenFromFirstByte(b0)
	for i := 1; i < n; i++ {
		if tmp[i], err = r.ReadByte(); err != nil {
			return 0, unexpected(err)
		}
	}
	v, _ := ReadVarint(tmp[:n])
	return v, nil
}

// ReadMessage reads one control message, its type and its body. The body is read into buf when it fits there,
// and into a new slice otherwise. io.EOF before the type is returned as is.
func ReadMessage(r StreamReader, buf []byte) (uint64, []byte, error) {
	typ, err := ReadVarintFrom(r)
	if err != nil {
		return 0, nil, err
	}
	body, err := ReadMessageBody(r, buf)
	return typ, body, err
}

// ReadMessageBody reads a 16-bit length and the control message body it frames, after the message's type
func ReadMessageBody(r StreamReader, buf []byte) ([]byte, error) {
	var lb [2]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return nil, unexpected(err)
	}
	return readBody(r, buf, int(binary.BigEndian.Uint16(lb[:])))
}

// ReadVarintMessageBody reads a varint length and the control message body it frames, after the message's type,
// failing with ErrMessageTooLong when the length exceeds maxLen
func ReadVarintMessageBody(r StreamReader, buf []byte, maxLen uint64) ([]byte, error) {
	n, err := ReadVarintFrom(r)
	if err != nil {
		return nil, unexpected(err)
	}
	if n > maxLen {
		return nil, ErrMessageTooLong
	}
	return readBody(r, buf, int(n)) // #nosec G115 -- bounded by maxLen
}

func readBody(r StreamReader, buf []byte, n int) ([]byte, error) {
	body := buf[:0]
	if cap(body) < n && n <= bodyChunk {
		body = make([]byte, n)
	}
	if cap(body) >= n {
		body = body[:n]
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, unexpected(err)
		}
		return body, nil
	}
	// a body past the default size grows with the bytes received, so a peer cannot hold its declared size by stalling
	for len(body) < n {
		step := min(n-len(body), max(len(body), bodyChunk))
		body = slices.Grow(body, step)
		at := len(body)
		body = body[:at+step]
		if _, err := io.ReadFull(r, body[at:]); err != nil {
			return nil, unexpected(err)
		}
	}
	return body, nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
