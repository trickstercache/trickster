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
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// outputRow pairs a point with the series it belongs to.
type outputRow struct {
	series *dataset.Series
	point  *dataset.Point
}

// timeOrderedRows flattens a result the way ClickHouse returns a bucketed,
// grouped query: rows ordered by time, with each bucket's series in the
// order the series are listed. Long-to-wide conversion in clients relies
// on that order, so a series-major layout would render as one zigzag line.
func timeOrderedRows(r *dataset.Result) []outputRow {
	n := 0
	for _, s := range r.SeriesList {
		n += len(s.Points)
	}
	rows := make([]outputRow, 0, n)
	for _, s := range r.SeriesList {
		for i := range s.Points {
			rows = append(rows, outputRow{series: s, point: &s.Points[i]})
		}
	}
	slices.SortStableFunc(rows, func(a, b outputRow) int { return cmp.Compare(a.point.Epoch, b.point.Epoch) })
	return rows
}
