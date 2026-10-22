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
)

// MaxMessageLen bounds a control message body, whose length field is 16 bits
const MaxMessageLen = 1<<16 - 1

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
	var lb [2]byte
	if _, err = io.ReadFull(r, lb[:]); err != nil {
		return 0, nil, unexpected(err)
	}
	n := int(binary.BigEndian.Uint16(lb[:]))
	body := buf[:0]
	if cap(body) < n {
		body = make([]byte, n)
	}
	body = body[:n]
	if _, err = io.ReadFull(r, body); err != nil {
		return 0, nil, unexpected(err)
	}
	return typ, body, nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
