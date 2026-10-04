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

package tsm

import (
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

// legacyReplicaConflictCount is replicaConflictCount as it compared boxed Points, kept as an oracle
func legacyReplicaConflictCount(contributions []*gatherContribution) int {
	points := make(map[replicaPointKey][]any)
	var conflicts int
	for _, contribution := range contributions {
		ds, ok := contribution.data.(*dataset.DataSet)
		if !ok || ds == nil {
			continue
		}
		for _, result := range ds.Results {
			if result == nil {
				continue
			}
			for _, series := range result.SeriesList {
				if series == nil {
					continue
				}
				hash := series.Header.CalculateHash()
				for _, point := range dspoints.Of(series) {
					key := replicaPointKey{
						statement: result.StatementID,
						series:    hash,
						epoch:     int64(point.Epoch),
					}
					if preferred, exists := points[key]; exists {
						if !reflect.DeepEqual(preferred, point.Values) {
							conflicts++
						}
						continue
					}
					points[key] = point.Values
				}
			}
		}
	}
	return conflicts
}

func TestReplicaConflictCountMatchesLegacy(t *testing.T) {
	rng := weaktest.NewRand(71, 71)
	// values replicas may disagree on, or agree on in different ways: a NaN never equals itself, -0 equals
	// 0, and values of other kinds or widths differ
	values := []any{
		"1", "1", "2", nil, math.NaN(), 0.0, math.Copysign(0, -1), int64(1), uint64(1), true,
		[]byte("1"),
		[]byte{},
	}
	total := 0
	for range 300 {
		var contributions []*gatherContribution
		for range 2 + rng.IntN(3) {
			var list dataset.SeriesList
			for s := range 1 + rng.IntN(3) {
				width := 1 + rng.IntN(2)
				var pts dataset.Points
				for at := range 1 + rng.IntN(4) {
					p := dataset.Point{Epoch: epoch.Epoch(int64(at) * 1e9)}
					if rng.IntN(8) > 0 {
						p.Values = make([]any, width)
						for c := range p.Values {
							p.Values[c] = values[rng.IntN(len(values))]
						}
					}
					pts = append(pts, p)
				}
				series := dataset.NewSeries(dataset.SeriesHeader{Name: fmt.Sprint(s)}, pts)
				if width == 2 && rng.IntN(3) == 0 {
					// a narrower Segment after a wider one, whose rows are padded with nulls
					narrow := dataset.NewSeries(series.Header, dataset.Points{{Epoch: epoch.Epoch(9e9), Values: []any{"1"}}})
					series = dataset.NewSeriesOf(series.Header, append(series.Segments(), narrow.Segments()...))
				}
				list = append(list, series)
			}
			contributions = append(contributions, &gatherContribution{
				data: &dataset.DataSet{Results: dataset.Results{{SeriesList: list}}},
			})
		}
		want := legacyReplicaConflictCount(contributions)
		require.Equal(t, want, replicaConflictCount(contributions))
		total += want
	}
	require.Greater(t, total, 100)
}
