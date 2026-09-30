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

package dataset

import (
	"encoding/binary"
	"errors"
	"unsafe"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/tinylib/msgp/msgp"
)

// Segments encode as they sit in memory, as little-endian words after an explicit pad, so a decode
// shares words that land aligned and takes time by columns, not rows

var (
	// ErrInvalidSegments indicates encoded Segments are truncated or corrupt.
	ErrInvalidSegments = errors.New("invalid encoded segments")

	littleEndian = binary.NativeEndian.Uint16([]byte{1, 0}) == 1
)

const (
	wordSize  = 8
	colTags   = 1
	colData   = 2
	colExt    = 4
	colNoVals = 8
)

// AppendSegments appends s's encoding to dst. base is the position in dst where the blob a decoder
// will be given starts, from which blocks are aligned.
func AppendSegments(dst []byte, base int, s Segments) ([]byte, error) {
	nonEmpty := 0
	for i := range s {
		if len(s[i].epochs) > 0 {
			nonEmpty++
		}
	}
	dst = binary.AppendUvarint(dst, uint64(nonEmpty)) // #nosec G115 -- a count is never negative
	for i := range s {
		if len(s[i].epochs) == 0 {
			continue
		}
		var err error
		if dst, err = appendSegment(dst, base, &s[i]); err != nil {
			return nil, err
		}
	}
	return dst, nil
}

func appendSegment(dst []byte, base int, seg *Segment) ([]byte, error) {
	rows := len(seg.epochs)
	dst = binary.AppendUvarint(dst, uint64(rows))          // #nosec G115 -- counts are never negative
	dst = binary.AppendUvarint(dst, uint64(len(seg.cols))) // #nosec G115 -- as above
	// #nosec G103 -- an epoch and a uint64 have the same size and alignment
	dst = appendWords(dst, base, unsafe.Slice((*uint64)(unsafe.Pointer(unsafe.SliceData(seg.epochs))), rows))
	for c := range seg.cols {
		col := seg.Col(c)
		var err error
		if dst, err = appendColumn(dst, base, &col); err != nil {
			return nil, err
		}
	}
	return dst, nil
}

func appendColumn(dst []byte, base int, col *Column) ([]byte, error) {
	var flags byte
	if col.tags != nil {
		flags |= colTags
	}
	if col.kind == KindNull && col.tags == nil {
		flags |= colNoVals
	}
	// a view may keep data its rows don't refer to, which isn't written
	lo, hi := col.dataSpan()
	if hi > lo {
		flags |= colData
	}
	if len(col.ext) > 0 {
		flags |= colExt
	}
	dst = append(dst, byte(col.kind), flags)
	if flags&colNoVals == 0 {
		if lo == 0 {
			dst = appendWords(dst, base, col.vals)
		} else {
			dst = appendRebased(dst, base, col, lo)
		}
	}
	if col.tags != nil {
		// #nosec G103 -- a Kind is a byte
		dst = append(dst, unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(col.tags))), len(col.tags))...)
	}
	if flags&colData != 0 {
		dst = binary.AppendUvarint(dst, uint64(hi-lo)) // #nosec G115 -- a length is never negative
		dst = append(dst, col.data[lo:hi]...)
	}
	if flags&colExt != 0 {
		dst = binary.AppendUvarint(dst, uint64(len(col.ext))) // #nosec G115 -- as above
		for _, v := range col.ext {
			var err error
			if dst, err = msgp.AppendIntf(dst, v); err != nil {
				return nil, err
			}
		}
	}
	return dst, nil
}

// dataSpan returns the range of data the column's bytes values refer to
func (c *Column) dataSpan() (int, int) {
	if len(c.data) == 0 {
		return 0, 0
	}
	lo, hi := len(c.data), 0
	for i, v := range c.vals {
		if !c.KindAt(i).IsBytes() {
			continue
		}
		off, n := int(v>>cellOffsetShift), int(v&cellLengthMask)
		if n > 0 {
			lo, hi = min(lo, off), max(hi, off+n)
		}
	}
	if hi <= lo {
		return 0, 0
	}
	return lo, hi
}

// appendRebased appends the column's cells with its bytes values' offsets moved back by lo
func appendRebased(dst []byte, base int, col *Column, lo int) []byte {
	dst = pad(dst, base)
	for i, v := range col.vals {
		if col.KindAt(i).IsBytes() {
			v -= uint64(lo) << cellOffsetShift // #nosec G115 -- lo is never negative
		}
		dst = binary.LittleEndian.AppendUint64(dst, v)
	}
	return dst
}

// pad appends a count of zeros, then the zeros, to align the next write to a word from base
func pad(dst []byte, base int) []byte {
	n := (wordSize - (len(dst)+1-base)%wordSize) % wordSize
	dst = append(dst, byte(n))
	for range n {
		dst = append(dst, 0)
	}
	return dst
}

func appendWords(dst []byte, base int, words []uint64) []byte {
	dst = pad(dst, base)
	if littleEndian {
		// #nosec G103 -- the words are written as the bytes they're held in, on a little-endian host
		return append(dst, unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(words))), len(words)*wordSize)...)
	}
	for _, w := range words {
		dst = binary.LittleEndian.AppendUint64(dst, w)
	}
	return dst
}

// AlignedBlob returns b when it starts at an aligned address, and otherwise an aligned copy, so a
// decode of Segments encoded from its start can share their words.
func AlignedBlob(b []byte) []byte {
	if !littleEndian || len(b) == 0 || aligned(b) {
		return b
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

func aligned(b []byte) bool {
	// #nosec G103 -- the address is only tested, never dereferenced
	return uintptr(unsafe.Pointer(unsafe.SliceData(b)))%wordSize == 0
}

// ReadSegments decodes the Segments AppendSegments wrote at pos in blob, and returns the position after
// them. They may share blob's memory, which then must not change.
func ReadSegments(blob []byte, pos int) (Segments, int, error) {
	r := segmentReader{blob: blob, pos: pos}
	n := r.count(2)
	var out Segments
	if n > 0 {
		out = make(Segments, 0, n)
	}
	for range n {
		seg := r.segment()
		if r.err {
			return nil, pos, ErrInvalidSegments
		}
		out = append(out, seg)
	}
	if r.err {
		return nil, pos, ErrInvalidSegments
	}
	return out, r.pos, nil
}

type segmentReader struct {
	blob []byte
	pos  int
	err  bool
}

func (r *segmentReader) fail() {
	r.err, r.pos = true, len(r.blob)
}

func (r *segmentReader) uvarint() uint64 {
	if r.err || r.pos > len(r.blob) {
		r.fail()
		return 0
	}
	v, n := binary.Uvarint(r.blob[r.pos:])
	if n <= 0 {
		r.fail()
		return 0
	}
	r.pos += n
	return v
}

// count reads a count of items taking at least minimum bytes each, so corrupt data can't size an
// allocation past the blob
func (r *segmentReader) count(minimum int) int {
	n := r.uvarint()
	if r.err || n > uint64(len(r.blob)-r.pos)/uint64(max(minimum, 1)) { // #nosec G115 -- never negative
		r.fail()
		return 0
	}
	return int(n) // #nosec G115 -- bounded by the blob's length above
}

func (r *segmentReader) take(n int) []byte {
	if r.err || n < 0 || n > len(r.blob)-r.pos {
		r.fail()
		return nil
	}
	b := r.blob[r.pos : r.pos+n : r.pos+n]
	r.pos += n
	return b
}

// words reads n little-endian words after their pad, sharing the blob's memory where they're aligned
func (r *segmentReader) words(n int) []uint64 {
	if p := r.take(1); p != nil && p[0] < wordSize {
		r.take(int(p[0]))
	} else {
		r.fail()
	}
	if r.err || n > (len(r.blob)-r.pos)/wordSize {
		r.fail()
		return nil
	}
	b := r.take(n * wordSize)
	if r.err || n == 0 {
		return nil
	}
	if littleEndian && aligned(b) {
		// #nosec G103 -- the words are aligned, and little-endian as the host's are
		return unsafe.Slice((*uint64)(unsafe.Pointer(unsafe.SliceData(b))), n)
	}
	out := make([]uint64, n)
	for i := range out {
		out[i] = binary.LittleEndian.Uint64(b[i*wordSize:])
	}
	return out
}

func (r *segmentReader) segment() Segment {
	rows := r.count(wordSize)
	cols := r.count(2)
	if r.err {
		return Segment{}
	}
	e := r.words(rows)
	seg := Segment{cols: make([]Column, cols)}
	if len(e) > 0 {
		// #nosec G103 -- an epoch and a uint64 have the same size and alignment
		seg.epochs = unsafe.Slice((*epoch.Epoch)(unsafe.Pointer(unsafe.SliceData(e))), len(e))
	}
	for c := range seg.cols {
		seg.cols[c] = r.column(rows)
		if r.err {
			return Segment{}
		}
	}
	return seg
}

func (r *segmentReader) column(rows int) Column {
	head := r.take(2)
	if r.err {
		return Column{}
	}
	col := Column{kind: Kind(head[0])}
	flags := head[1]
	if col.kind > KindMixed || flags&^(colTags|colData|colExt|colNoVals) != 0 ||
		(col.kind == KindMixed && flags&colTags == 0) {
		r.fail()
		return Column{}
	}
	if flags&colNoVals != 0 {
		col.vals = make([]uint64, rows)
	} else {
		col.vals = r.words(rows)
		if col.vals == nil {
			col.vals = []uint64{}
		}
	}
	if flags&colTags != 0 {
		t := r.take(rows)
		if r.err {
			return Column{}
		}
		// #nosec G103 -- a Kind is a byte
		col.tags = unsafe.Slice((*Kind)(unsafe.Pointer(unsafe.SliceData(t))), len(t))
	}
	if flags&colData != 0 {
		col.data = r.take(r.count(1))
	}
	if flags&colExt != 0 {
		n := r.count(1)
		if !r.err {
			col.ext = make([]any, n)
		}
		for i := range col.ext {
			if col.ext[i] = r.readIntf(); r.err {
				return Column{}
			}
		}
	}
	if r.err || !col.valid() {
		r.fail()
		return Column{}
	}
	return col
}

// readIntf reads one boxed value at the reader's position
func (r *segmentReader) readIntf() any {
	if r.err || r.pos > len(r.blob) {
		r.fail()
		return nil
	}
	v, rest, err := msgp.ReadIntfBytes(r.blob[r.pos:])
	if err != nil {
		r.fail()
		return nil
	}
	r.pos = len(r.blob) - len(rest)
	return v
}

// valid reports whether every value's kind, offset and index is in range; only kinds that refer
// into data or ext, or vary by value, are checked value by value
func (c *Column) valid() bool {
	if c.tags == nil {
		switch {
		case c.kind >= KindMixed:
			return false
		case !c.kind.IsBytes() && c.kind != KindExt:
			return true
		}
	} else if len(c.tags) != len(c.vals) {
		return false
	}
	for i, v := range c.vals {
		k := c.KindAt(i)
		switch {
		case k >= KindMixed:
			return false
		case k.IsBytes():
			// an empty value never reads data, so its offset isn't checked
			if n := v & cellLengthMask; n > 0 && v>>cellOffsetShift+n > uint64(len(c.data)) {
				return false
			}
		case k == KindExt:
			if v >= uint64(len(c.ext)) {
				return false
			}
		}
	}
	return true
}
