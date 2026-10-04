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
)

var (
	// ErrTooLong is returned when a length exceeds the limit the specification sets for its field
	ErrTooLong = errors.New("moq: length exceeds limit")
	// ErrTooMany is returned when a count cannot fit in the input that remains
	ErrTooMany = errors.New("moq: count exceeds input")
)

// Reader decodes values from a byte slice as it advances through it. The first failure is kept and later
// reads return zero values, so a decoder checks Err once at the end; slices it returns alias the input.
type Reader struct {
	b   []byte
	off int
	err error
}

// NewReader returns a Reader over b
func NewReader(b []byte) Reader {
	return Reader{b: b}
}

// Err returns the first error the Reader met
func (r *Reader) Err() error {
	return r.err
}

// Fail records err unless an earlier error is already recorded
func (r *Reader) Fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

// Remaining returns the number of unread bytes
func (r *Reader) Remaining() int {
	if r.err != nil {
		return 0
	}
	return len(r.b) - r.off
}

// Offset returns the number of bytes consumed
func (r *Reader) Offset() int {
	return r.off
}

// Varint reads a variable-length integer
func (r *Reader) Varint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := ReadVarint(r.b[r.off:])
	if n == 0 {
		r.err = ErrShort
		return 0
	}
	r.off += n
	return v
}

// Uint8 reads one byte
func (r *Reader) Uint8() uint8 {
	if r.err != nil {
		return 0
	}
	if r.off >= len(r.b) {
		r.err = ErrShort
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

// Uint16 reads a big-endian 16-bit integer
func (r *Reader) Uint16() uint16 {
	if r.err != nil {
		return 0
	}
	if len(r.b)-r.off < 2 {
		r.err = ErrShort
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

// Bytes reads exactly n bytes
func (r *Reader) Bytes(n uint64) []byte {
	if r.err != nil {
		return nil
	}
	rem := len(r.b) - r.off
	if n > uint64(rem) { // #nosec G115 -- rem is never negative
		r.err = ErrShort
		return nil
	}
	end := r.off + int(n) // #nosec G115 -- n is at most rem, which is an int
	v := r.b[r.off:end:end]
	r.off = end
	return v
}

// LenBytes reads a varint length then that many bytes, failing with ErrTooLong past maxLen
func (r *Reader) LenBytes(maxLen uint64) []byte {
	n := r.Varint()
	if r.err != nil {
		return nil
	}
	if n > maxLen {
		r.err = ErrTooLong
		return nil
	}
	return r.Bytes(n)
}

// Rest returns every unread byte and consumes them
func (r *Reader) Rest() []byte {
	if r.err != nil {
		return nil
	}
	v := r.b[r.off:len(r.b):len(r.b)]
	r.off = len(r.b)
	return v
}

// Count reads a varint count of items that are each at least one byte long, failing with ErrTooMany when
// the input left cannot hold that many; it bounds every loop a hostile count could otherwise spin
func (r *Reader) Count() int {
	n := r.Varint()
	if r.err != nil {
		return 0
	}
	if n > uint64(len(r.b)-r.off) { // #nosec G115 -- the remainder is never negative
		r.err = ErrTooMany
		return 0
	}
	return int(n) // #nosec G115 -- n is at most the remainder, which is an int
}

// Done fails with ErrTrailing when bytes remain, and returns the Reader's error
func (r *Reader) Done() error {
	if r.err == nil && r.off != len(r.b) {
		r.err = ErrTrailing
	}
	return r.err
}

// AppendBytes appends a varint length followed by p
func AppendBytes(b, p []byte) []byte {
	return append(AppendVarint(b, uint64(len(p))), p...)
}

// AppendString appends a varint length followed by s
func AppendString(b []byte, s string) []byte {
	return append(AppendVarint(b, uint64(len(s))), s...)
}
