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
	"cmp"
	"math"
	"slices"
	"sort"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// Segment holds rows of one series by column: an epoch per row, and one Column per value field. A
// Segment is read-only; views of it share its memory.
type Segment struct {
	epochs []epoch.Epoch
	cols   []Column
	// the row within cols of the Segment's first row, so a view shares its parent's columns
	from int
}

// Len returns the number of rows in the Segment.
func (s *Segment) Len() int {
	return len(s.epochs)
}

// Epochs returns the Segment's epochs, one per row, which must not be modified.
func (s *Segment) Epochs() []epoch.Epoch {
	return s.epochs
}

// Epoch returns the epoch of row i.
func (s *Segment) Epoch(i int) epoch.Epoch {
	return s.epochs[i]
}

// NumCols returns the number of value columns in the Segment.
func (s *Segment) NumCols() int {
	return len(s.cols)
}

// Col returns value column i, holding exactly the Segment's rows.
func (s *Segment) Col(i int) Column {
	return s.cols[i].slice(s.from, s.from+len(s.epochs))
}

// Size returns the Segment's memory in bytes, counting all the data its columns keep alive.
func (s *Segment) Size() int64 {
	size := int64(8 * len(s.epochs))
	for i := range s.cols {
		c := s.Col(i)
		size += c.Size()
	}
	return size
}

// IsSorted reports whether the Segment's epochs never decrease.
func (s *Segment) IsSorted() bool {
	return slices.IsSorted(s.epochs)
}

// KindAt returns the kind of column c's value at row i.
func (s *Segment) KindAt(c, i int) Kind {
	return s.cols[c].KindAt(s.from + i)
}

// Value returns column c's value at row i, boxed as Column.Value boxes it.
func (s *Segment) Value(c, i int) any {
	return s.cols[c].Value(s.from + i)
}

// Float64 returns column c's value at row i, which must be a KindFloat64.
func (s *Segment) Float64(c, i int) float64 {
	return s.cols[c].Float64(s.from + i)
}

// Int64 returns column c's value at row i, which must be a KindInt64.
func (s *Segment) Int64(c, i int) int64 {
	return s.cols[c].Int64(s.from + i)
}

// Uint64 returns column c's value at row i, which must be a KindUint64.
func (s *Segment) Uint64(c, i int) uint64 {
	return s.cols[c].Uint64(s.from + i)
}

// Bool returns column c's value at row i, which must be a KindBool.
func (s *Segment) Bool(c, i int) bool {
	return s.cols[c].Bool(s.from + i)
}

// Bytes returns column c's value at row i, which must be of a bytes kind; it must not be modified.
func (s *Segment) Bytes(c, i int) []byte {
	return s.cols[c].Bytes(s.from + i)
}

// Text returns column c's value at row i, which must be of a bytes kind, as a string sharing its memory.
func (s *Segment) Text(c, i int) string {
	return s.cols[c].Text(s.from + i)
}

// rows [from, to) of the Segment, sharing its memory
func (s *Segment) slice(from, to int) Segment {
	return Segment{epochs: s.epochs[from:to:to], cols: s.cols, from: s.from + from}
}

// Segments holds a series' rows as Segments in time order: one for a stored series, and more for a
// response that adds rows before or after it without copying them.
type Segments []Segment

// Len returns the number of rows across the Segments.
func (s Segments) Len() int {
	n := 0
	for i := range s {
		n += len(s[i].epochs)
	}
	return n
}

// NumCols returns the number of value columns in the Segments' rows.
func (s Segments) NumCols() int {
	for i := range s {
		if len(s[i].epochs) > 0 {
			return len(s[i].cols)
		}
	}
	return 0
}

// IsSorted reports whether the epochs never decrease, within and across the Segments.
func (s Segments) IsSorted() bool {
	var last epoch.Epoch
	started := false
	for i := range s {
		e := s[i].epochs
		if len(e) == 0 {
			continue
		}
		if (started && e[0] < last) || !slices.IsSorted(e) {
			return false
		}
		last, started = e[len(e)-1], true
	}
	return true
}

// First returns the epoch of the first row, and false when there are no rows.
func (s Segments) First() (epoch.Epoch, bool) {
	for i := range s {
		if len(s[i].epochs) > 0 {
			return s[i].epochs[0], true
		}
	}
	return 0, false
}

// Last returns the epoch of the last row, and false when there are no rows.
func (s Segments) Last() (epoch.Epoch, bool) {
	for _, v := range slices.Backward(s) {
		if e := v.epochs; len(e) > 0 {
			return e[len(e)-1], true
		}
	}
	return 0, false
}

// Size returns the Segments' memory in bytes.
func (s Segments) Size() int64 {
	var size int64
	for i := range s {
		size += s[i].Size()
	}
	return size
}

// View returns the rows within the inclusive range [start, end], sharing their memory. The Segments
// must be sorted.
func (s Segments) View(start, end epoch.Epoch) Segments {
	var out Segments
	whole := true
	for i := range s {
		e := s[i].epochs
		if len(e) == 0 {
			// an empty segment is harmless in s, and left out of a copy
			continue
		}
		from := sort.Search(len(e), func(j int) bool { return e[j] >= start })
		to := from + sort.Search(len(e)-from, func(j int) bool { return e[from+j] > end })
		if from == 0 && to == len(e) {
			if !whole {
				out = append(out, s[i])
			}
			continue
		}
		if whole {
			// the first segment that changes starts the copy of the list
			out = make(Segments, 0, len(s))
			for j := range i {
				if len(s[j].epochs) > 0 {
					out = append(out, s[j])
				}
			}
			whole = false
		}
		if from < to {
			out = append(out, s[i].slice(from, to))
		}
	}
	if whole {
		return s
	}
	return out
}

// Compact returns the rows as at most one Segment: s itself when it has no more than one, and
// otherwise a copy of all of them.
func (s Segments) Compact() Segments {
	nonEmpty, last := 0, 0
	for i := range s {
		if len(s[i].epochs) > 0 {
			nonEmpty++
			last = i
		}
	}
	switch nonEmpty {
	case 0:
		return nil
	case 1:
		if len(s) == 1 {
			return s
		}
		return Segments{s[last]}
	}
	return s.Clone()
}

// Clone returns a copy of the rows as one Segment, sharing no memory with s but boxed KindExt values.
func (s Segments) Clone() Segments {
	n := s.Len()
	if n == 0 {
		return nil
	}
	w := newSegmentWriter(n, s.NumCols(), s)
	for i := range s {
		w.copyRun(&s[i], 0, len(s[i].epochs))
	}
	return Segments{w.finish()}
}

// Sorted returns the rows sorted by epoch, keeping their order within an epoch: s itself when it is
// sorted already, and otherwise a sorted copy.
func (s Segments) Sorted() Segments {
	if s.IsSorted() {
		return s
	}
	refs := s.refs()
	slices.SortStableFunc(refs, func(a, b rowRef) int {
		return cmp.Compare(s[a.seg].epochs[a.row], s[b.seg].epochs[b.row])
	})
	return s.gather(refs)
}

// Filter returns the rows keep reports true for: s itself when it keeps them all, and otherwise a
// copy of the kept rows in one new Segment.
func (s Segments) Filter(keep func(seg *Segment, i int) bool) Segments {
	var refs []rowRef
	dropped := false
	for k := range s {
		seg := &s[k]
		for i := range seg.epochs {
			if !keep(seg, i) {
				if !dropped {
					// the rows kept before the first dropped one are collected only now
					refs = s.refsBefore(k, i)
					dropped = true
				}
				continue
			}
			if dropped {
				// #nosec G115 -- segment and row counts are far below 2^31
				refs = append(refs, rowRef{seg: int32(k), row: int32(i)})
			}
		}
	}
	if !dropped {
		return s
	}
	return s.gather(refs)
}

// refsBefore returns references to the rows before row i of segment k
func (s Segments) refsBefore(k, i int) []rowRef {
	refs := make([]rowRef, 0, s.Len())
	for j := range k {
		for r := range s[j].epochs {
			// #nosec G115 -- segment and row counts are far below 2^31
			refs = append(refs, rowRef{seg: int32(j), row: int32(r)})
		}
	}
	for r := range i {
		refs = append(refs, rowRef{seg: int32(k), row: int32(r)}) // #nosec G115 -- as above
	}
	return refs
}

// rowRef locates a row within Segments
type rowRef struct {
	seg int32
	row int32
}

// refs returns a reference to each row, in order
func (s Segments) refs() []rowRef {
	refs := make([]rowRef, 0, s.Len())
	for i := range s {
		for j := range s[i].epochs {
			// #nosec G115 -- segment and row counts are far below 2^31
			refs = append(refs, rowRef{seg: int32(i), row: int32(j)})
		}
	}
	return refs
}

// gather copies the referenced rows, in order, into one new Segment
func (s Segments) gather(refs []rowRef) Segments {
	if len(refs) == 0 {
		return nil
	}
	w := newSegmentWriter(len(refs), s.NumCols(), s)
	w.copyRefs(s, refs)
	return Segments{w.finish()}
}

// segmentWriter builds one new Segment from rows copied or given value by value
type segmentWriter struct {
	epochs []epoch.Epoch
	cols   []columnWriter
}

type columnWriter struct {
	vals []uint64
	// each value's kind, kept only once a value's kind differs from the first one's
	kinds []Kind
	first Kind
	data  []byte
	ext   []any
	kt    kindTracker
}

// newSegmentWriter sizes a writer for rows rows of cols columns, and each column's data for the
// data the sources hold
func newSegmentWriter(rows, cols int, sources ...Segments) *segmentWriter {
	w := &segmentWriter{epochs: make([]epoch.Epoch, 0, rows), cols: make([]columnWriter, cols)}
	vals := make([]uint64, 0, rows*cols)
	for c := range w.cols {
		w.cols[c].vals = vals[len(vals) : len(vals) : len(vals)+rows]
		vals = vals[:len(vals)+rows]
		size := 0
		for _, src := range sources {
			for i := range src {
				if c < len(src[i].cols) {
					size += len(src[i].cols[c].data)
				}
			}
		}
		if size > 0 {
			w.cols[c].data = make([]byte, 0, min(size, maxColumnData))
		}
	}
	return w
}

// noteKind records that n values of kind k are about to be appended
func (cw *columnWriter) noteKind(k Kind, n int) {
	if n == 0 {
		return
	}
	switch {
	case len(cw.vals) == 0:
		cw.first = k
	case cw.kinds == nil && k != cw.first:
		cw.kinds = make([]Kind, len(cw.vals), cap(cw.vals))
		for i := range cw.kinds {
			cw.kinds[i] = cw.first
		}
	}
	if cw.kinds != nil {
		for range n {
			cw.kinds = append(cw.kinds, k)
		}
	}
	cw.kt.add(k)
}

// appendCell appends a value to column c of the row being written
func (w *segmentWriter) appendCell(c int, k Kind, v uint64, b []byte, x any) {
	cw := &w.cols[c]
	cw.noteKind(k, 1)
	switch {
	case k.IsBytes():
		off := len(cw.data)
		cw.data = append(cw.data, b...)
		v = uint64(off)<<cellOffsetShift | uint64(len(b)) // #nosec G115 -- lengths are never negative
	case k == KindExt:
		v = uint64(len(cw.ext))
		cw.ext = append(cw.ext, x)
	}
	cw.vals = append(cw.vals, v)
}

// appendValue appends a Go value to column c, as cellOf reads it
func (w *segmentWriter) appendValue(c int, v any) {
	k, cell, b := cellOf(v)
	w.appendCell(c, k, cell, b, v)
}

// copyCell appends value i of src to column c
func (w *segmentWriter) copyCell(c int, src *Column, i int) {
	k := src.KindAt(i)
	switch {
	case k.IsBytes():
		w.appendCell(c, k, 0, src.Bytes(i), nil)
	case k == KindExt:
		w.appendCell(c, k, 0, nil, src.Ext(i))
	default:
		w.appendCell(c, k, src.vals[i], nil, nil)
	}
}

// copyRefs appends the referenced rows of s; consecutive rows of one segment are copied as a run
func (w *segmentWriter) copyRefs(s Segments, refs []rowRef) {
	for from := 0; from < len(refs); {
		to := from + 1
		for to < len(refs) && refs[to].seg == refs[from].seg && refs[to].row == refs[to-1].row+1 {
			to++
		}
		r := refs[from]
		w.copyRun(&s[r.seg], int(r.row), int(r.row)+to-from)
		from = to
	}
}

// copyRun appends rows [from, to) of src; a column src lacks is null, and one it adds is dropped
func (w *segmentWriter) copyRun(src *Segment, from, to int) {
	w.epochs = append(w.epochs, src.epochs[from:to]...)
	n := to - from
	for c := range w.cols {
		cw := &w.cols[c]
		if c >= len(src.cols) {
			cw.noteKind(KindNull, n)
			for range n {
				cw.vals = append(cw.vals, 0)
			}
			continue
		}
		col := src.Col(c)
		switch {
		case col.tags != nil || col.kind == KindExt:
		case col.kind.IsBytes():
			if cw.copyBytesRun(&col, from, to) {
				continue
			}
		default:
			// uniform fixed-width values copy in bulk
			cw.noteKind(col.kind, n)
			cw.vals = append(cw.vals, col.vals[from:to]...)
			continue
		}
		for i := from; i < to; i++ {
			w.copyCell(c, &col, i)
		}
	}
}

// copyBytesRun appends rows [from, to) of a uniform bytes column whose values lie in its data in row
// order, copying their bytes in one piece; it reports false, having appended nothing, otherwise
func (cw *columnWriter) copyBytesRun(col *Column, from, to int) bool {
	vals := col.vals[from:to]
	lo, end, used := -1, 0, 0
	for _, v := range vals {
		off, n := int(v>>cellOffsetShift), int(v&cellLengthMask)
		if n == 0 {
			continue
		}
		if lo < 0 {
			lo = off
		} else if off < end {
			return false
		}
		end, used = off+n, used+n
	}
	if lo < 0 {
		lo = 0
	} else if end-lo > used+used/2 {
		// the values are spread thin through the data, so copying the span would waste memory
		return false
	}
	base := len(cw.data)
	cw.data = append(cw.data, col.data[lo:end]...)
	cw.noteKind(col.kind, len(vals))
	// #nosec G115 -- offsets are never negative
	shift, rebase := uint64(lo)<<cellOffsetShift, uint64(base)<<cellOffsetShift
	for _, v := range vals {
		if v&cellLengthMask == 0 {
			// an empty value's offset is never read, so it's set in range
			cw.vals = append(cw.vals, rebase)
			continue
		}
		cw.vals = append(cw.vals, v-shift+rebase)
	}
	return true
}

// appendFloatText appends f to column c as text, as strconv.FormatFloat(f, 'f', -1, 64) writes it
func (w *segmentWriter) appendFloatText(c int, f float64) {
	cw := &w.cols[c]
	cw.noteKind(KindString, 1)
	off := len(cw.data)
	cw.data = strconv.AppendFloat(cw.data, f, 'f', -1, 64)
	cw.vals = append(cw.vals, uint64(off)<<cellOffsetShift|uint64(len(cw.data)-off)) // #nosec G115 -- never negative
}

// copyRow appends row i of src, with its first column's value replaced when first is set
func (w *segmentWriter) copyRow(src *Segment, i int, first *cellValue) {
	if first == nil {
		w.copyRun(src, i, i+1)
		return
	}
	w.epochs = append(w.epochs, src.epochs[i])
	for c := range w.cols {
		switch {
		case c == 0 && first.floatText:
			w.appendFloatText(0, first.f)
		case c == 0:
			w.appendCell(0, first.kind, first.val, first.bytes, first.ext)
		case c < len(src.cols):
			col := src.Col(c)
			w.copyCell(c, &col, i)
		default:
			w.appendCell(c, KindNull, 0, nil, nil)
		}
	}
}

// finish returns the Segment; the writer can't be used afterward
func (w *segmentWriter) finish() Segment {
	seg := Segment{epochs: w.epochs, cols: make([]Column, len(w.cols))}
	for c := range w.cols {
		cw := &w.cols[c]
		kind, tagged := cw.kt.result()
		col := Column{kind: kind, vals: cw.vals, data: cw.data, ext: cw.ext}
		if tagged {
			col.tags = cw.kinds
		}
		seg.cols[c] = col
	}
	return seg
}

// cellValue is one value to write in place of a copied one: a cell, or a float to write as text
type cellValue struct {
	kind      Kind
	val       uint64
	bytes     []byte
	ext       any
	f         float64
	floatText bool
}

// floatTextCell returns the cell of f written as text, as strconv.FormatFloat(f, 'f', -1, 64) writes it
func floatTextCell(f float64) cellValue {
	return cellValue{kind: KindString, f: f, floatText: true}
}

// valueCell returns the cell of a Go value, as cellOf reads it
func valueCell(v any) cellValue {
	k, cell, b := cellOf(v)
	return cellValue{kind: k, val: cell, bytes: b, ext: v}
}

// floatAt returns column c's value at row i as a float: a float's own, a number parsed from text, or
// NaN for any other value
func (s *Segment) floatAt(c, i int) float64 {
	col := &s.cols[c]
	switch i += s.from; col.KindAt(i) {
	case KindFloat64:
		return col.Float64(i)
	case KindString:
		f, err := strconv.ParseFloat(col.Text(i), 64)
		if err != nil {
			return math.NaN()
		}
		return f
	}
	return math.NaN()
}

// mapFirstValue applies fn to each row's first value, writing its value when replace and dropping the
// row unless keep; rows without values are kept. It returns the count dropped
func (s Segments) mapFirstValue(fn func(seg *Segment, i int) (v cellValue, replace, keep bool)) (Segments, int) {
	n := s.Len()
	if n == 0 {
		return s, 0
	}
	w := newSegmentWriter(n, s.NumCols(), s)
	dropped := 0
	for k := range s {
		seg := &s[k]
		for i := range seg.Len() {
			if seg.NumCols() == 0 {
				w.copyRow(seg, i, nil)
				continue
			}
			v, replace, keep := fn(seg, i)
			switch {
			case !keep:
				dropped++
			case replace:
				w.copyRow(seg, i, &v)
			default:
				w.copyRow(seg, i, nil)
			}
		}
	}
	if dropped == n {
		return nil, dropped
	}
	return Segments{w.finish()}, dropped
}
