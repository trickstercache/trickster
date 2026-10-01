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

package dataset

import "fmt"

// seriesPoints returns a copy of the series' rows as Points, every row as wide as its first Segment's,
// boxing every value, as the testutil/dspoints package does for other packages' tests
func seriesPoints(s *Series) Points {
	n := s.segs.Len()
	if n == 0 {
		return nil
	}
	cols := s.segs.NumCols()
	out := make(Points, 0, n)
	var vals []any
	if cols > 0 {
		vals = make([]any, n*cols)
	}
	for k := range s.segs {
		seg := &s.segs[k]
		for j := range seg.Len() {
			p := Point{Epoch: seg.epochs[j]}
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

// seriesPointAt returns row i of the series as a Point, boxing its values; it panics when there's none
func seriesPointAt(s *Series, i int) Point {
	seg, row, ok := s.RowAt(i)
	if !ok {
		panic(fmt.Sprintf("dataset: row %d out of range", i))
	}
	p := Point{Epoch: seg.epochs[row]}
	if n := seg.NumCols(); n > 0 {
		p.Values = make([]any, n)
		for c := range n {
			p.Values[c] = seg.Value(c, row)
		}
	}
	return p
}
