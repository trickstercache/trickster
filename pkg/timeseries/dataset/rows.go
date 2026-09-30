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
	"iter"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// Row is one row of a series, as Result.Rows yields it. Its values are read in place by column.
type Row struct {
	Series *Series
	// Seg holds the row, which must not be modified
	Seg *Segment
	// Index is the row's position within Seg
	Index int
	// SeriesIndex is the series' position in its result's SeriesList
	SeriesIndex int
}

// Epoch returns the row's epoch.
func (r Row) Epoch() epoch.Epoch {
	return r.Seg.epochs[r.Index]
}

// KindAt returns the kind of the row's value in column c.
func (r Row) KindAt(c int) Kind {
	return r.Seg.KindAt(c, r.Index)
}

// Value returns the row's value in column c, boxed.
func (r Row) Value(c int) any {
	return r.Seg.Value(c, r.Index)
}

// Bytes returns the row's value in column c, which must be of a bytes kind; it must not be modified.
func (r Row) Bytes(c int) []byte {
	return r.Seg.Bytes(c, r.Index)
}

// Int64 returns the row's value in column c, which must be a KindInt64.
func (r Row) Int64(c int) int64 {
	return r.Seg.Int64(c, r.Index)
}

// RowOrder orders the rows Result.Rows yields: by epoch, newest first when Descending, and within
// an epoch by Compare, or by series order when Compare is nil.
type RowOrder struct {
	Descending bool
	Compare    func(a, b Row) int
}

// Rows yields the result's rows one at a time, merging its series, which must each be sorted by
// epoch. The rows are the result's own and must not be modified.
func (r *Result) Rows(order RowOrder) iter.Seq[Row] {
	return func(yield func(Row) bool) {
		if r == nil {
			return
		}
		lists := make([]Segments, len(r.SeriesList))
		for i, s := range r.SeriesList {
			if s != nil {
				lists[i] = s.segs
			}
		}
		row := func(sr SegmentRow) Row {
			return Row{Series: r.SeriesList[sr.List], Seg: sr.Seg, Index: sr.Index, SeriesIndex: sr.List}
		}
		segOrder := SegmentRowOrder{Descending: order.Descending}
		if order.Compare != nil {
			segOrder.Compare = func(a, b SegmentRow) int { return order.Compare(row(a), row(b)) }
		}
		for sr := range SegmentRows(lists, segOrder) {
			if !yield(row(sr)) {
				return
			}
		}
	}
}
