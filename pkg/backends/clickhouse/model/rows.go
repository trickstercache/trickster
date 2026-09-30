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
)

// outputRow pairs a point with the series it belongs to.
type outputRow struct {
	series *dataset.Series
	point  *dataset.Point
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
				if !yield(outputRow{series: row.Series, point: row.Point}) {
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
		for i := range s.PointCount() {
			rows = append(rows, outputRow{series: s, point: s.PointAt(i)})
		}
	}
	slices.SortStableFunc(rows, func(a, b outputRow) int { return cmp.Compare(a.point.Epoch, b.point.Epoch) })
	return slices.Values(rows), n
}
