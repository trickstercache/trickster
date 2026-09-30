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

package model

import (
	"cmp"
	"iter"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// outputRow locates a row: its series, and where the series holds it.
type outputRow struct {
	series *dataset.Series
	seg    *dataset.Segment
	i      int
}

func (r outputRow) epoch() epoch.Epoch {
	return r.seg.Epoch(r.i)
}

// rows by time with each bucket's series in list order, as ClickHouse returns a grouped query, which
// clients' long-to-wide conversion relies on; the row count is returned too
func timeOrderedRows(r *dataset.Result) (iter.Seq[outputRow], int) {
	n, sorted := 0, true
	for _, s := range r.SeriesList {
		if s == nil {
			continue
		}
		n += s.PointCount()
		sorted = sorted && s.IsSorted()
	}
	if sorted {
		// merging the sorted series breaks ties by series position, as the stable sort below does
		return func(yield func(outputRow) bool) {
			for row := range r.Rows(dataset.RowOrder{}) {
				if !yield(outputRow{series: row.Series, seg: row.Seg, i: row.Index}) {
					return
				}
			}
		}, n
	}
	rows := make([]outputRow, 0, n)
	for _, s := range r.SeriesList {
		if s == nil {
			continue
		}
		segs := s.Segments()
		for k := range segs {
			for i := range segs[k].Len() {
				rows = append(rows, outputRow{series: s, seg: &segs[k], i: i})
			}
		}
	}
	slices.SortStableFunc(rows, func(a, b outputRow) int { return cmp.Compare(a.epoch(), b.epoch()) })
	return slices.Values(rows), n
}
