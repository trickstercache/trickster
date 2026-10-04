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

// Package dspoints reads a dataset's series as boxed Points, for tests that compare or rebuild rows;
// production code reads a series' Segments in place.
package dspoints

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// Of returns a copy of the series' rows as Points, every row as wide as its first Segment's, boxing
// every value.
func Of(s *dataset.Series) dataset.Points {
	segs := s.Segments()
	n := segs.Len()
	if n == 0 {
		return nil
	}
	cols := segs.NumCols()
	out := make(dataset.Points, 0, n)
	var vals []any
	if cols > 0 {
		vals = make([]any, n*cols)
	}
	for k := range segs {
		seg := &segs[k]
		for j := range seg.Len() {
			p := dataset.Point{Epoch: seg.Epoch(j)}
			if cols > 0 {
				p.Values, vals = vals[:cols:cols], vals[cols:]
				for c := range min(cols, seg.NumCols()) {
					p.Values[c] = seg.Value(c, j)
				}
			}
			out = append(out, p)
		}
	}
	return out
}

// At returns row i of the series as a Point, boxing its values; it panics when there's no row i.
func At(s *dataset.Series, i int) dataset.Point {
	seg, row, ok := s.RowAt(i)
	if !ok {
		panic(fmt.Sprintf("dspoints: row %d out of range", i))
	}
	p := dataset.Point{Epoch: seg.Epoch(row)}
	if n := seg.NumCols(); n > 0 {
		p.Values = make([]any, n)
		for c := range n {
			p.Values[c] = seg.Value(c, row)
		}
	}
	return p
}
