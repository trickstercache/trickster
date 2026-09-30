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
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// ErrValuesTooLarge indicates a build's bytes values exceeded the 4 GiB a DataSet can hold.
var ErrValuesTooLarge = errors.New("dataset bytes values exceed 4 GiB")

// ColumnLog collects rows of any number of series, in any order, into a few shared, exactly sized
// slabs holding one sorted Segment per series. A ColumnLog is not safe for concurrent use.
type ColumnLog struct {
	policy DuplicatePolicy
	series []logSeries
	stats  []logColumn
	// one entry per committed row
	rowSeries []int32
	rowEpochs []epoch.Epoch
	rowCells  []int32
	// one entry per value, committed or staged; a bytes value's cell holds its chunk and offset
	cells []uint64
	kinds []Kind
	// bytes values in chunks that never move, each its uvarint length and then its bytes; Finish
	// copies them into place, so they needn't be contiguous
	data      [][]byte
	chunkSize int
	dataLen   int
	ext       []any
	// emptied chunks from an earlier build, used before new ones are allocated
	spare [][]byte
	// where the staged row's values begin
	staged, stagedChunks, stagedOff, stagedDataLen, stagedExt int
	finished                                                  bool
	bufs                                                      *logBuffers
}

// the arrays a ColumnLog uses only while building, pooled between builds
type logBuffers struct {
	rowSeries, rowCells, perm []int32
	rowEpochs                 []epoch.Epoch
	cells                     []uint64
	kinds                     []Kind
	chunks                    [][]byte
}

var logBufferPool = sync.Pool{New: func() any { return new(logBuffers) }}

// the most memory a finished build's discarded arrays may hold and still be pooled for the next one
const maxPooledLogBytes = 16 << 20

const (
	minLogChunk = 4 << 10
	maxLogChunk = 1 << 20
)

type logSeries struct {
	cols, stat, rows int
	last             epoch.Epoch
	unordered        bool
	// whether the series took its width from its first row, so shorter rows are padded with nulls
	lazy bool
	// whether an in-order row repeated the last epoch, which Finish resolves by the policy
	dups bool
}

type logColumn struct {
	kt   kindTracker
	exts int
}

// NewColumnLog returns a ColumnLog that applies policy to rows of one series sharing an epoch.
func NewColumnLog(policy DuplicatePolicy) *ColumnLog {
	b := logBufferPool.Get().(*logBuffers)
	return &ColumnLog{
		policy: policy, bufs: b, rowSeries: b.rowSeries, rowEpochs: b.rowEpochs, rowCells: b.rowCells,
		cells: b.cells, kinds: b.kinds, spare: b.chunks,
	}
}

// recycle pools the arrays the finished log used only while building, when they're not too large
func (l *ColumnLog) recycle(perm []int32) {
	b := l.bufs
	if b == nil {
		return
	}
	l.data = append(l.data, l.spare...)
	chunks := l.data
	b.rowSeries, b.rowCells, b.rowEpochs = l.rowSeries[:0], l.rowCells[:0], l.rowEpochs[:0]
	b.cells, b.kinds = l.cells[:0], l.kinds[:0]
	l.bufs, l.rowSeries, l.rowEpochs, l.rowCells, l.cells, l.kinds, l.data, l.spare = nil, nil, nil, nil, nil, nil, nil, nil
	l.staged, l.stagedChunks, l.stagedOff = 0, 0, 0
	size := 4*(cap(b.rowSeries)+cap(b.rowCells)+cap(perm)) + 8*(cap(b.rowEpochs)+cap(b.cells)) + cap(b.kinds)
	for i, c := range chunks {
		size += cap(c)
		chunks[i] = c[:0]
	}
	if size > maxPooledLogBytes {
		return
	}
	b.chunks, b.perm = chunks, perm[:0]
	logBufferPool.Put(b)
}

// Grow reserves room for rows more rows holding cells values and dataBytes bytes of bytes values.
func (l *ColumnLog) Grow(rows, cells, dataBytes int) {
	l.rowSeries = slices.Grow(l.rowSeries, rows)
	l.rowEpochs = slices.Grow(l.rowEpochs, rows)
	l.rowCells = slices.Grow(l.rowCells, rows)
	l.cells = slices.Grow(l.cells, cells)
	l.kinds = slices.Grow(l.kinds, cells)
	if dataBytes > 0 {
		// a byte for each value's length is the common case
		l.data = append(l.data, l.newChunk(dataBytes+cells, dataBytes+cells))
	}
}

// AddSeries adds a series whose rows hold cols values, and returns its index for Commit. A negative
// cols leaves the count to the series' first row.
func (l *ColumnLog) AddSeries(cols int) int {
	l.series = append(l.series, logSeries{cols: cols, stat: len(l.stats)})
	for range cols {
		l.stats = append(l.stats, logColumn{})
	}
	return len(l.series) - 1
}

// Series returns the number of series added.
func (l *ColumnLog) Series() int {
	return len(l.series)
}

// Staged returns the number of values staged for the next row.
func (l *ColumnLog) Staged() int {
	return len(l.kinds) - l.staged
}

// Rows returns the number of rows committed.
func (l *ColumnLog) Rows() int {
	return len(l.rowEpochs)
}

// the smallest room the log's arrays grow to, in elements
const minLogGrowth = 256

// grow returns s with room for n more elements, doubling where append grows a large slice by a
// quarter, so a log's arrays are copied about once rather than four times
func grow[T any](s []T, n int) []T {
	if cap(s)-len(s) >= n {
		return s
	}
	out := make([]T, len(s), max(2*cap(s), len(s)+n, minLogGrowth))
	copy(out, s)
	return out
}

func (l *ColumnLog) addCell(k Kind, v uint64) {
	if len(l.cells) == cap(l.cells) {
		l.cells, l.kinds = grow(l.cells, 1), grow(l.kinds, 1)
	}
	l.cells = append(l.cells, v)
	l.kinds = append(l.kinds, k)
}

func (l *ColumnLog) addData(k Kind, raw []byte) {
	need := uvarintLen(uint64(len(raw))) + len(raw) // #nosec G115 -- a length is never negative
	if n := len(l.data); n == 0 || cap(l.data[n-1])-len(l.data[n-1]) < need {
		l.chunkSize = min(max(2*l.chunkSize, minLogChunk), maxLogChunk)
		l.data = append(l.data, l.newChunk(need, max(l.chunkSize, need)))
	}
	ci := len(l.data) - 1
	off := len(l.data[ci])
	chunk := binary.AppendUvarint(l.data[ci], uint64(len(raw))) // #nosec G115 -- a length is never negative
	l.data[ci] = append(chunk, raw...)
	l.dataLen += len(raw)
	l.addCell(k, uint64(ci)<<cellOffsetShift|uint64(off)) // #nosec G115 -- as above
}

// newChunk returns an empty chunk with room for need bytes, a spare one if it has room, or else a new
// one of size bytes
func (l *ColumnLog) newChunk(need, size int) []byte {
	for len(l.spare) > 0 {
		c := l.spare[0]
		l.spare = l.spare[1:]
		if cap(c) >= need {
			return c
		}
	}
	return make([]byte, 0, size)
}

// uvarintLen returns the bytes binary.AppendUvarint writes for x
func uvarintLen(x uint64) int {
	n := 1
	for ; x >= 0x80; x >>= 7 {
		n++
	}
	return n
}

// dataAt returns the bytes of a staged or committed bytes value, by its cell
func (l *ColumnLog) dataAt(cell uint64) []byte {
	b := l.data[cell>>cellOffsetShift][cell&cellLengthMask:]
	n, w := binary.Uvarint(b)
	return b[w : w+int(n)] // #nosec G115 -- the length was written from a slice's
}

// AddNull stages a null value.
func (l *ColumnLog) AddNull() {
	l.addCell(KindNull, 0)
}

// AddBool stages a bool value.
func (l *ColumnLog) AddBool(v bool) {
	if v {
		l.addCell(KindBool, 1)
		return
	}
	l.addCell(KindBool, 0)
}

// AddInt64 stages an int64 value.
func (l *ColumnLog) AddInt64(v int64) {
	l.addCell(KindInt64, uint64(v)) // #nosec G115 -- the cell holds the int64's bits
}

// AddUint64 stages a uint64 value.
func (l *ColumnLog) AddUint64(v uint64) {
	l.addCell(KindUint64, v)
}

// AddFloat64 stages a float64 value.
func (l *ColumnLog) AddFloat64(v float64) {
	l.addCell(KindFloat64, math.Float64bits(v))
}

// AddString stages a copy of raw as a text value.
func (l *ColumnLog) AddString(raw []byte) {
	l.addData(KindString, raw)
}

// AddBytes stages a copy of raw as a bytes value; nil stages a null.
func (l *ColumnLog) AddBytes(raw []byte) {
	if raw == nil {
		l.AddNull()
		return
	}
	l.addData(KindBytes, raw)
}

// AddNumber stages a copy of raw, a number's literal text, as a number value.
func (l *ColumnLog) AddNumber(raw []byte) {
	l.addData(KindNumber, raw)
}

// AddExt stages a value of a kind the others don't hold, boxed as it is.
func (l *ColumnLog) AddExt(v any) {
	l.addCell(KindExt, uint64(len(l.ext)))
	l.ext = append(l.ext, v)
}

// AddValue stages v by its Go type, as Column.Value would return it; other types stage as KindExt.
func (l *ColumnLog) AddValue(v any) {
	k, cell, raw := cellOf(v)
	switch {
	case k.IsBytes():
		l.addData(k, raw)
	case k == KindExt:
		l.AddExt(v)
	default:
		l.addCell(k, cell)
	}
}

// Rollback discards the staged values.
func (l *ColumnLog) Rollback() {
	// every staged value has a cell, so with none there's nothing to discard
	if l.finished || len(l.kinds) == l.staged {
		return
	}
	clear(l.ext[l.stagedExt:])
	l.cells, l.kinds = l.cells[:l.staged], l.kinds[:l.staged]
	l.dataLen, l.ext = l.stagedDataLen, l.ext[:l.stagedExt]
	// chunks the staged values opened are emptied but kept, so the next values reuse them
	for i := l.stagedChunks; i < len(l.data); i++ {
		l.data[i] = l.data[i][:0]
	}
	if l.stagedChunks > 0 {
		l.data[l.stagedChunks-1] = l.data[l.stagedChunks-1][:l.stagedOff]
	}
}

// Commit adds the staged values as a row of series at epoch e, or discards them when it fails. An
// in-order repeated epoch fails under DuplicatesError and is dropped under DuplicatesFirstWins.
func (l *ColumnLog) Commit(series int, e epoch.Epoch) error {
	if l.finished {
		return ErrBuilderFinished
	}
	if series < 0 || series >= len(l.series) {
		l.Rollback()
		return ErrInvalidRow
	}
	if s := &l.series[series]; s.cols < 0 {
		s.cols, s.stat, s.lazy = len(l.kinds)-l.staged, len(l.stats), true
		for range s.cols {
			l.stats = append(l.stats, logColumn{})
		}
	} else if s.lazy {
		for range s.cols - (len(l.kinds) - l.staged) {
			l.AddNull()
		}
	}
	if len(l.kinds)-l.staged != l.series[series].cols {
		l.Rollback()
		return ErrInvalidRow
	}
	if l.dataLen > maxColumnData {
		l.Rollback()
		return ErrValuesTooLarge
	}
	s := &l.series[series]
	if s.rows > 0 && !s.unordered {
		switch {
		case e < s.last:
			s.unordered = true
		case e == s.last:
			switch l.policy {
			case DuplicatesError:
				l.Rollback()
				return ErrDuplicateEpoch
			case DuplicatesFirstWins:
				l.Rollback()
				return nil
			case DuplicatesLastWins:
				s.dups = true
			}
		}
	}
	s.last = e
	s.rows++
	if len(l.rowEpochs) == cap(l.rowEpochs) {
		l.rowSeries, l.rowEpochs, l.rowCells = grow(l.rowSeries, 1), grow(l.rowEpochs, 1), grow(l.rowCells, 1)
	}
	// #nosec G115 -- series and value counts are far below 2^31
	l.rowSeries = append(l.rowSeries, int32(series))
	l.rowEpochs = append(l.rowEpochs, e)
	l.rowCells = append(l.rowCells, int32(l.staged)) // #nosec G115 -- as above
	for c := range s.cols {
		st := &l.stats[s.stat+c]
		k := l.kinds[l.staged+c]
		st.kt.add(k)
		if k == KindExt {
			st.exts++
		}
	}
	l.stage()
	return nil
}

// stage marks the end of the committed values, where the next row's begin
func (l *ColumnLog) stage() {
	l.staged, l.stagedChunks, l.stagedDataLen, l.stagedExt = len(l.kinds), len(l.data), l.dataLen, len(l.ext)
	if n := len(l.data); n > 0 {
		l.stagedOff = len(l.data[n-1])
	}
}

// Finish returns one Segment per series, in the order they were added, each sorted by epoch with
// the duplicate policy applied. The ColumnLog can't be used afterward.
func (l *ColumnLog) Finish() ([]Segment, error) {
	if l.finished {
		return nil, ErrBuilderFinished
	}
	l.Rollback()
	l.finished = true
	segs := make([]Segment, len(l.series))
	if len(l.rowEpochs) == 0 {
		l.recycle(nil)
		return segs, nil
	}
	perm, starts := l.groupRows()
	defer l.recycle(perm)
	// sort and dedupe each series' rows, noting how many each keeps
	kept := make([][]int32, len(l.series))
	var totalRows, totalCells, totalTags, totalCols int
	for i := range l.series {
		s := &l.series[i]
		s.cols = max(s.cols, 0)
		rows := perm[starts[i]:starts[i+1]]
		if s.unordered {
			// a stable sort keeps arrival order among equal epochs for the duplicate policy
			slices.SortStableFunc(rows, func(a, b int32) int { return cmp.Compare(l.rowEpochs[a], l.rowEpochs[b]) })
		}
		if (s.unordered || s.dups) && l.policy != DuplicatesKeep {
			var err error
			if rows, err = l.dedupe(rows); err != nil {
				return nil, err
			}
		}
		kept[i] = rows
		totalRows += len(rows)
		totalCells += len(rows) * s.cols
		totalCols += s.cols
		for c := range s.cols {
			st := &l.stats[s.stat+c]
			if _, tagged := st.kt.result(); tagged {
				totalTags += len(rows)
			}
		}
	}
	epochs := make([]epoch.Epoch, totalRows)
	vals := make([]uint64, totalCells)
	cols := make([]Column, totalCols)
	var tags []Kind
	if totalTags > 0 {
		tags = make([]Kind, totalTags)
	}
	// every column's bytes share one slab, which the committed values' bytes can't overflow
	var data []byte
	if l.dataLen > 0 {
		data = make([]byte, 0, l.dataLen)
	}
	var fixed []fixedColumn
	for i := range l.series {
		s := &l.series[i]
		rows := kept[i]
		n := len(rows)
		seg := Segment{epochs: epochs[:n:n], cols: cols[:s.cols:s.cols]}
		epochs, cols = epochs[n:], cols[s.cols:]
		// columns of one fixed-width kind throughout are written together, a row's cells at a time
		fixed = fixed[:0]
		for c := range s.cols {
			if k, tagged := l.stats[s.stat+c].kt.result(); !tagged && !k.IsBytes() && k != KindExt {
				seg.cols[c] = Column{kind: k, vals: vals[c*n : (c+1)*n : (c+1)*n]}
				fixed = append(fixed, fixedColumn{c: c, vals: seg.cols[c].vals})
			}
		}
		for j, r := range rows {
			seg.epochs[j] = l.rowEpochs[r]
			cells := l.cells[l.rowCells[r]:]
			for _, f := range fixed {
				f.vals[j] = cells[f.c]
			}
		}
		for c := range s.cols {
			k, tagged := l.stats[s.stat+c].kt.result()
			if !tagged && !k.IsBytes() && k != KindExt {
				continue
			}
			var colTags []Kind
			if tagged {
				colTags, tags = tags[:n:n], tags[n:]
			}
			seg.cols[c], data = l.writeColumn(rows, c, vals[c*n:(c+1)*n:(c+1)*n], colTags, data, l.stats[s.stat+c].exts)
		}
		vals = vals[s.cols*n:]
		segs[i] = seg
	}
	return segs, nil
}

type fixedColumn struct {
	c    int
	vals []uint64
}

// groupRows returns the committed rows grouped by series, each series' rows in arrival order, and
// where each series' rows start
func (l *ColumnLog) groupRows() ([]int32, []int) {
	starts := make([]int, len(l.series)+1)
	for i := range l.series {
		starts[i+1] = starts[i] + l.series[i].rows
	}
	next := slices.Clone(starts[:len(l.series)])
	var perm []int32
	if l.bufs != nil {
		perm = l.bufs.perm
	}
	perm = slices.Grow(perm[:0], len(l.rowEpochs))[:len(l.rowEpochs)]
	for r, s := range l.rowSeries {
		perm[next[s]] = int32(r) // #nosec G115 -- row counts are far below 2^31
		next[s]++
	}
	return perm, starts
}

// dedupe applies the policy to rows sorted by epoch, keeping arrival order among equal epochs
func (l *ColumnLog) dedupe(rows []int32) ([]int32, error) {
	k := 0
	for i := 1; i < len(rows); i++ {
		if l.rowEpochs[rows[i]] != l.rowEpochs[rows[k]] {
			k++
			rows[k] = rows[i]
			continue
		}
		switch l.policy {
		case DuplicatesError:
			return nil, ErrDuplicateEpoch
		case DuplicatesLastWins:
			rows[k] = rows[i]
		}
	}
	return rows[:k+1], nil
}

// writeColumn fills vals and tags with value c of each row, appending its bytes to data, and returns
// the Column and the rest of data
func (l *ColumnLog) writeColumn(rows []int32, c int, vals []uint64, tags []Kind, data []byte,
	exts int,
) (Column, []byte) {
	start := len(data)
	var ext []any
	if exts > 0 {
		ext = make([]any, 0, exts)
	}
	var kt kindTracker
	for j, r := range rows {
		cell := int(l.rowCells[r]) + c
		k, v := l.kinds[cell], l.cells[cell]
		switch {
		case k.IsBytes():
			b := l.dataAt(v)
			v = uint64(len(data)-start)<<cellOffsetShift | uint64(len(b)) // #nosec G115 -- never negative
			data = append(data, b...)
		case k == KindExt:
			ext = append(ext, l.ext[v])
			v = uint64(len(ext) - 1) // #nosec G115 -- ext holds at least the value just appended
		}
		vals[j] = v
		if tags != nil {
			tags[j] = k
		}
		kt.add(k)
	}
	kind, tagged := kt.result()
	col := Column{kind: kind, vals: vals, ext: ext}
	if tagged {
		// the rows the policy dropped may have been the only ones that needed tags
		col.tags = tags
	}
	if len(data) > start {
		col.data = data[start:len(data):len(data)]
	}
	return col, data
}
