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

// Package wire implements the binary primitives shared by every supported MoQ Transport draft: the
// leading-ones variable-length integer, framing, key-value pairs and the common structures.
package wire

import (
	"encoding/binary"
	"errors"
	"math/bits"
)

const (
	maxVarint1 = 1<<7 - 1
	maxVarint2 = 1<<14 - 1
	maxVarint3 = 1<<21 - 1
	maxVarint4 = 1<<28 - 1
	maxVarint5 = 1<<35 - 1
	maxVarint6 = 1<<42 - 1
	maxVarint7 = 1<<49 - 1
	maxVarint8 = 1<<56 - 1
	// MaxVarintLen is the longest encoding of a variable-length integer
	MaxVarintLen = 9
)

var (
	// ErrShort is returned when input ends before the value being read does
	ErrShort = errors.New("moq: input too short")
	// ErrTrailing is returned when bytes remain after a structure that must fill its input
	ErrTrailing = errors.New("moq: trailing bytes")
)

// VarintLen returns the length of the shortest encoding of v
func VarintLen(v uint64) int {
	switch {
	case v <= maxVarint1:
		return 1
	case v <= maxVarint2:
		return 2
	case v <= maxVarint3:
		return 3
	case v <= maxVarint4:
		return 4
	case v <= maxVarint5:
		return 5
	case v <= maxVarint6:
		return 6
	case v <= maxVarint7:
		return 7
	case v <= maxVarint8:
		return 8
	}
	return MaxVarintLen
}

// AppendVarint appends the shortest encoding of v. The count of leading one bits in the first byte gives the
// encoding's length; the remaining bits carry the value big-endian.
func AppendVarint(b []byte, v uint64) []byte {
	return appendVarintLen(b, v, VarintLen(v))
}

// AppendVarintNo7 appends v without the 7-byte form, which a peer speaking a draft before -18 cannot decode
func AppendVarintNo7(b []byte, v uint64) []byte {
	n := VarintLen(v)
	if n == 7 {
		n = 8
	}
	return appendVarintLen(b, v, n)
}

func appendVarintLen(b []byte, v uint64, n int) []byte {
	if n == 1 {
		return append(b, byte(v)) // #nosec G115 -- a one-byte encoding holds at most 127
	}
	if n == MaxVarintLen {
		b = append(b, 0xff)
		return binary.BigEndian.AppendUint64(b, v)
	}
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], v)
	start := len(b)
	b = append(b, tmp[8-n:]...)
	// n-1 leading ones then a zero mark the length
	b[start] |= ^byte(0xff >> (n - 1))
	return b
}

// ReadVarint decodes a variable-length integer from the start of b, returning the value and the number of
// bytes it occupied. Non-minimal encodings are accepted; n is 0 when b is too short.
func ReadVarint(b []byte) (v uint64, n int) {
	if len(b) == 0 {
		return 0, 0
	}
	b0 := b[0]
	if b0 < 0x80 {
		return uint64(b0), 1
	}
	n = bits.LeadingZeros8(^b0) + 1
	if len(b) < n {
		return 0, 0
	}
	if n == MaxVarintLen {
		return binary.BigEndian.Uint64(b[1:MaxVarintLen]), n
	}
	v = uint64(b0 & (0xff >> n))
	for _, c := range b[1:n] {
		v = v<<8 | uint64(c)
	}
	return v, n
}

// VarintLenFromFirstByte returns the encoded length a varint beginning with b0 has
func VarintLenFromFirstByte(b0 byte) int {
	if b0 < 0x80 {
		return 1
	}
	return bits.LeadingZeros8(^b0) + 1
}
