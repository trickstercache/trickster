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

// Package header encodes and decodes the fixed frame header that precedes
// every object a disk cache stores, making each stored object self-describing
package header

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

const (
	// Size is the length in bytes of the fixed portion of a frame header
	Size = 44
	// MaxKeyLen is the longest cache key a frame header can carry
	MaxKeyLen = 4096
	// PrefixLen is how many leading bytes of a frame always hold its whole header and key
	PrefixLen = Size + MaxKeyLen
	// Version is the frame layout version this package reads and writes
	Version = 1

	offVersion    = 4
	offFlags      = 5
	offKeyLen     = 6
	offExpiration = 8
	offLastWrite  = 16
	offMetaLen    = 24
	offBodyLen    = 28
	offPayloadCRC = 36
	offHeaderCRC  = 40
)

var (
	magic = [4]byte{'T', 'K', 'B', 'F'}
	table = crc32.MakeTable(crc32.Castagnoli)
)

var (
	// ErrBadMagic indicates the data is not a frame, or is a frame of another version
	ErrBadMagic = errors.New("not a cache frame")
	// ErrCorrupt indicates a frame whose checksums or lengths do not agree with its content
	ErrCorrupt = errors.New("corrupt cache frame")
	// ErrKeyTooLong indicates a cache key longer than MaxKeyLen
	ErrKeyTooLong = errors.New("cache key too long")
)

// Header describes one stored object. Times are Unix nanoseconds; a zero Expiration never expires.
type Header struct {
	Flags      uint8
	KeyLen     uint16
	Expiration int64
	LastWrite  int64
	MetaLen    uint32
	BodyLen    uint64
	PayloadCRC uint32
}

// PayloadOffset returns the offset of the first payload byte within the frame
func (h *Header) PayloadOffset() int64 {
	return Size + int64(h.KeyLen)
}

// BodyOffset returns the offset of the first body byte within the frame
func (h *Header) BodyOffset() int64 {
	return Size + int64(h.KeyLen) + int64(h.MetaLen)
}

// PayloadLen returns the combined length of the meta and body sections
func (h *Header) PayloadLen() int64 {
	// #nosec G115 -- Parse bounds BodyLen by the frame size, which is an int64
	return int64(h.MetaLen) + int64(h.BodyLen)
}

// FrameSize returns the length of the whole frame the header describes
func (h *Header) FrameSize() int64 {
	return h.PayloadOffset() + h.PayloadLen()
}

// Expired reports whether the object had expired at now, in Unix nanoseconds
func (h *Header) Expired(now int64) bool {
	return h.Expiration != 0 && h.Expiration <= now
}

// Checksum returns the payload checksum of an object's meta and body sections
func Checksum(meta, body []byte) uint32 {
	return crc32.Update(crc32.Update(0, table, meta), table, body)
}

// Append appends the encoded header and key to dst, which allocates only when dst lacks capacity
func Append(dst []byte, key string, h *Header) ([]byte, error) {
	if len(key) > MaxKeyLen {
		return dst, ErrKeyTooLong
	}
	start := len(dst)
	var b [Size]byte
	copy(b[:], magic[:])
	b[offVersion] = Version
	b[offFlags] = h.Flags
	// #nosec G115 -- the key length was bounded by MaxKeyLen above
	binary.LittleEndian.PutUint16(b[offKeyLen:], uint16(len(key)))
	// #nosec G115 -- times keep their two's-complement bit pattern and are decoded the same way
	binary.LittleEndian.PutUint64(b[offExpiration:], uint64(h.Expiration))
	// #nosec G115 -- see the expiration above
	binary.LittleEndian.PutUint64(b[offLastWrite:], uint64(h.LastWrite))
	binary.LittleEndian.PutUint32(b[offMetaLen:], h.MetaLen)
	binary.LittleEndian.PutUint64(b[offBodyLen:], h.BodyLen)
	binary.LittleEndian.PutUint32(b[offPayloadCRC:], h.PayloadCRC)
	dst = append(dst, b[:]...)
	dst = append(dst, key...)
	sum := crc32.Update(crc32.Update(0, table, dst[start:start+offHeaderCRC]), table, dst[start+Size:])
	binary.LittleEndian.PutUint32(dst[start+offHeaderCRC:], sum)
	return dst, nil
}

// Parse decodes the header and key at the front of b, the leading bytes of a frame that is
// size bytes long in all. The returned key aliases b.
func Parse(b []byte, size int64) (Header, []byte, error) {
	var h Header
	if len(b) < Size || [4]byte(b[:4]) != magic || b[offVersion] != Version {
		return h, nil, ErrBadMagic
	}
	h.decode(b)
	end := Size + int(h.KeyLen)
	// lengths are checked against the real size before anything is sized from them
	// #nosec G115 -- a size under the end of the key, and so any negative one, is rejected first
	if h.KeyLen > MaxKeyLen || end > len(b) || size < int64(end) ||
		h.BodyLen > uint64(size) || h.FrameSize() != size {
		return h, nil, ErrCorrupt
	}
	key := b[Size:end]
	sum := crc32.Update(crc32.Update(0, table, b[:offHeaderCRC]), table, key)
	if sum != binary.LittleEndian.Uint32(b[offHeaderCRC:]) {
		return h, nil, ErrCorrupt
	}
	return h, key, nil
}

// Peek decodes the fixed portion of a header from the front of b, checking only that it is
// one. It tells whether a frame is one that was parsed before.
func Peek(b []byte) (Header, bool) {
	var h Header
	if len(b) < Size || [4]byte(b[:4]) != magic || b[offVersion] != Version {
		return h, false
	}
	h.decode(b)
	return h, true
}

func (h *Header) decode(b []byte) {
	h.Flags = b[offFlags]
	h.KeyLen = binary.LittleEndian.Uint16(b[offKeyLen:])
	// #nosec G115 -- reverses the bit-preserving conversion made when encoding
	h.Expiration = int64(binary.LittleEndian.Uint64(b[offExpiration:]))
	// #nosec G115 -- see the expiration above
	h.LastWrite = int64(binary.LittleEndian.Uint64(b[offLastWrite:]))
	h.MetaLen = binary.LittleEndian.Uint32(b[offMetaLen:])
	h.BodyLen = binary.LittleEndian.Uint64(b[offBodyLen:])
	h.PayloadCRC = binary.LittleEndian.Uint32(b[offPayloadCRC:])
}

// Read decodes the header and key of the size-byte frame in r, using buf as scratch space.
// The returned key aliases buf, which should hold PrefixLen bytes to fit any key.
func Read(r io.ReaderAt, size int64, buf []byte) (Header, []byte, error) {
	if int64(len(buf)) > size {
		buf = buf[:size]
	}
	n, err := r.ReadAt(buf, 0)
	if err != nil && (err != io.EOF || n < len(buf)) {
		return Header{}, nil, err
	}
	return Parse(buf[:n], size)
}

// VerifyPayload reports whether payload, a frame's meta and body sections, matches its header
func VerifyPayload(h *Header, payload []byte) error {
	if int64(len(payload)) != h.PayloadLen() || crc32.Checksum(payload, table) != h.PayloadCRC {
		return ErrCorrupt
	}
	return nil
}
