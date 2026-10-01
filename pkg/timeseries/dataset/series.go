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
	"fmt"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Series represents a single timeseries in a Result
type Series struct {
	// Header is the Series Header describing the Series
	Header SeriesHeader
	// the rows, by column and in time order; a response may hold more than one Segment, adding rows
	// around stored ones without copying them. The rows are read-only once set.
	segs Segments
}

// Hash is a numeric value representing a calculated hash
type Hash uint64

// Hashes is a slice of type Hash
type Hashes []Hash

// SeriesLookup is a map of Series searchable by Series Header Hash
type SeriesLookup map[SeriesLookupKey]*Series

// SeriesLookupKey is the key for a SeriesLookup, consisting of a
// Result.StatementID and a Series.Hash
type SeriesLookupKey struct {
	StatementID int
	Hash        Hash
}

// NewSeries returns a Series of header h holding points, in their order.
func NewSeries(h SeriesHeader, points Points) *Series {
	s := &Series{Header: h}
	s.SetPoints(points)
	return s
}

// NewSeriesOf returns a Series of header h holding segs, which it shares.
func NewSeriesOf(h SeriesHeader, segs Segments) *Series {
	return &Series{Header: h, segs: segs}
}

// Segments returns the series' rows, which must not be modified.
func (s *Series) Segments() Segments {
	return s.segs
}

// SetSegments replaces the series' rows with segs, which it shares.
func (s *Series) SetSegments(segs Segments) {
	s.segs = segs
}

// ReorderValues orders the series' value columns and fields as order lists their current indexes,
// replacing its Segments rather than changing them, so any that share them keep their order.
func (s *Series) ReorderValues(order []int) {
	if len(s.Header.ValueFieldsList) == len(order) {
		fields := make(timeseries.FieldDefinitions, len(order))
		for i, j := range order {
			fields[i] = s.Header.ValueFieldsList[j]
		}
		s.Header.ValueFieldsList = fields
	}
	if len(s.segs) == 0 {
		return
	}
	segs := make(Segments, len(s.segs))
	for k, seg := range s.segs {
		if len(seg.cols) == len(order) {
			cols := make([]Column, len(order))
			for i, j := range order {
				cols[i] = seg.cols[j]
			}
			seg.cols = cols
		}
		segs[k] = seg
	}
	s.segs = segs
}

// PointCount returns the number of rows in the series.
func (s *Series) PointCount() int {
	return s.segs.Len()
}

// HasParts reports whether the series holds its rows in more than one Segment.
func (s *Series) HasParts() bool {
	return len(s.segs) > 1
}

// IsSorted reports whether the series' epochs never decrease.
func (s *Series) IsSorted() bool {
	return s.segs.IsSorted()
}

// RowAt returns the Segment holding row i of the series and the row's index within it; ok is false
// when the series has no row i.
func (s *Series) RowAt(i int) (seg *Segment, row int, ok bool) {
	if i < 0 {
		return nil, 0, false
	}
	for k := range s.segs {
		if n := s.segs[k].Len(); i >= n {
			i -= n
			continue
		}
		return &s.segs[k], i, true
	}
	return nil, 0, false
}

// SetPoints replaces the series' rows with points, in their order; the values are copied.
func (s *Series) SetPoints(points Points) {
	s.segs = segmentsFromPoints(points)
}

// Size returns the memory utilization of the Series in bytes
func (s *Series) Size() int64 {
	return 16 + s.segs.Size() + int64(s.Header.Size)
}

// Clone returns a copy of the Series with its own header; the rows, which are read-only, are shared.
func (s *Series) Clone() *Series {
	return &Series{Header: s.Header.Clone(), segs: s.segs}
}

func (s *Series) String() string {
	sb := &strings.Builder{}
	sb.WriteString(`{"header":`)
	sb.WriteString(s.Header.String())
	sb.WriteString(`,"points":[`)
	first := true
	for i := range s.segs {
		seg := &s.segs[i]
		for j := range seg.Len() {
			if !first {
				sb.WriteByte(',')
			}
			first = false
			fmt.Fprintf(sb, `{%d,`, seg.epochs[j])
			for c := range seg.NumCols() {
				if c > 0 {
					sb.WriteByte(',')
				}
				fmt.Fprintf(sb, `%v`, seg.Value(c, j))
			}
			sb.WriteByte('}')
		}
	}
	sb.WriteString(`]}`)
	return sb.String()
}

// segmentsFromPoints returns points as one Segment, in their order
func segmentsFromPoints(points Points) Segments {
	if len(points) == 0 {
		return nil
	}
	cols := 0
	for i := range points {
		cols = max(cols, len(points[i].Values))
	}
	w := newSegmentWriter(len(points), cols)
	for i := range points {
		w.epochs = append(w.epochs, points[i].Epoch)
		for c := range cols {
			var v any
			if c < len(points[i].Values) {
				v = points[i].Values[c]
			}
			w.appendValue(c, v)
		}
	}
	return Segments{w.finish()}
}
