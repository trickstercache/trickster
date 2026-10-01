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
	"math"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/stretchr/testify/require"
)

func makeStringPoints(vals ...struct {
	epoch int64
	value string
},
) Points {
	p := make(Points, len(vals))
	for i, v := range vals {
		p[i] = Point{
			Epoch:  epoch.Epoch(v.epoch),
			Values: []any{v.value},
		}
	}
	return p
}

type ev struct {
	epoch int64
	value string
}

func TestMergePointsWithStrategySum(t *testing.T) {
	p1 := makeStringPoints(ev{100, "1.0"}, ev{200, "2.0"})
	p2 := makeStringPoints(ev{100, "3.0"}, ev{200, "4.0"})
	result := mergePoints(p1, p2, MergeOpts{SortPoints: true, Strategy: merge.StrategySum})
	require.Len(t, result, 2)
	require.Equal(t, "4", result[0].Values[0])
	require.Equal(t, "6", result[1].Values[0])
}

func TestMergePointsWithStrategyDedup(t *testing.T) {
	p1 := makeStringPoints(ev{100, "1.0"}, ev{200, "2.0"})
	p2 := makeStringPoints(ev{100, "3.0"}, ev{300, "4.0"})
	result := mergePoints(p1, p2, MergeOpts{SortPoints: true, Strategy: merge.StrategyDedup})
	require.Len(t, result, 3)
}

func TestMergePointsWithStrategyNilInputs(t *testing.T) {
	require.Nil(t, mergePoints(nil, nil, MergeOpts{SortPoints: true, Strategy: merge.StrategySum}))
	require.Len(t, mergePoints(Points{}, Points{}, MergeOpts{SortPoints: true, Strategy: merge.StrategySum}), 0)
}

func TestMergePointsWithStrategyCount(t *testing.T) {
	p1 := makeStringPoints(ev{100, "99.0"}, ev{200, "88.0"})
	p2 := makeStringPoints(ev{100, "77.0"}, ev{200, "66.0"})
	result := mergePoints(p1, p2, MergeOpts{SortPoints: true, Strategy: merge.StrategyCount})
	require.Len(t, result, 2)
	require.Equal(t, "2", result[0].Values[0])
	require.Equal(t, "2", result[1].Values[0])
}

func TestMergePointsWithStrategyAvg(t *testing.T) {
	p1 := makeStringPoints(ev{100, "10.0"}, ev{200, "20.0"})
	p2 := makeStringPoints(ev{100, "30.0"}, ev{200, "40.0"})
	result := mergePoints(p1, p2, MergeOpts{SortPoints: true, Strategy: merge.StrategyAvg})
	require.Len(t, result, 2)
	require.Equal(t, "20", result[0].Values[0])
	require.Equal(t, "30", result[1].Values[0])
}

func TestMergePointsWithStrategyScalar(t *testing.T) {
	t.Run("finite replaces NaN", func(t *testing.T) {
		result := mergePoints(makeStringPoints(ev{100, "NaN"}),
			makeStringPoints(ev{100, "42"}), MergeOpts{SortPoints: true, Strategy: merge.StrategyScalar})
		require.Len(t, result, 1)
		require.Equal(t, "42", result[0].Values[0])
	})

	t.Run("first finite member wins", func(t *testing.T) {
		result := mergePoints(makeStringPoints(ev{100, "42"}),
			makeStringPoints(ev{100, "99"}), MergeOpts{SortPoints: true, Strategy: merge.StrategyScalar})
		require.Len(t, result, 1)
		require.Equal(t, "42", result[0].Values[0])
	})
}

func TestParseFloat(t *testing.T) {
	require.Equal(t, 1.5, parseFloat("1.5"))
	require.Equal(t, 1.5, parseFloat(float64(1.5)))
	require.True(t, math.IsNaN(parseFloat("not_a_number")))
	require.True(t, math.IsNaN(parseFloat(42))) // int, not float64 or string
}

func TestMergePointsWithStrategyHistogram(t *testing.T) {
	hist := `{"count":"10","sum":"100","buckets":[[0,"1","2","3"]]}`
	p1 := makeStringPoints(ev{100, hist}, ev{200, "2.0"})
	p2 := makeStringPoints(ev{100, hist}, ev{200, "4.0"})
	result := mergePoints(p1, p2, MergeOpts{SortPoints: true, Strategy: merge.StrategySum})
	require.Len(t, result, 2)
	require.Equal(t, hist, result[0].Values[0])
	require.Equal(t, "6", result[1].Values[0])
}

// finalizePoints averages pts as DataSet.FinalizeAvg averages a series' rows
func finalizePoints(pts Points, count int) Points {
	ds := &DataSet{Results: Results{{SeriesList: SeriesList{NewSeries(SeriesHeader{}, pts)}}}}
	ds.FinalizeAvg(count)
	return seriesPoints(ds.Results[0].SeriesList[0])
}

// mergePoints merges pts through MergeSegments
func mergePoints(p, p2 Points, opts MergeOpts) Points {
	return seriesPoints(NewSeriesOf(SeriesHeader{}, MergeSegments(segmentsFromPoints(p), segmentsFromPoints(p2), opts)))
}

func TestFinalizeAvgNaN(t *testing.T) {
	hist := `{"count":"10","sum":"100"}`
	out := finalizePoints(Points{{Epoch: 100, Values: []any{hist}}}, 3)
	require.Equal(t, hist, out[0].Values[0])
}

func TestFinalizeAvgNumeric(t *testing.T) {
	out := finalizePoints(Points{{Epoch: 100, Values: []any{"12"}}}, 3)
	require.Equal(t, "4", out[0].Values[0])
	// a count of one leaves values as they are
	out = finalizePoints(Points{{Epoch: 100, Values: []any{"12"}}}, 1)
	require.Equal(t, "12", out[0].Values[0])
}

type stubValueOps struct {
	mergeHandled  bool
	divideHandled bool
	merged        any
	divided       any
}

func (s *stubValueOps) MergeValues(dst, src any, _ merge.Strategy) (any, bool) {
	if !s.mergeHandled {
		return nil, false
	}
	return s.merged, true
}

func (s *stubValueOps) DivideValue(value any, _ float64) (any, bool) {
	if !s.divideHandled {
		return nil, false
	}
	return s.divided, true
}

func (s *stubValueOps) PairingHash(_ *SeriesHeader, _ string) Hash { return 0 }

func (s *stubValueOps) FinalizeMerge(_ *DataSet, _ merge.Strategy) {}

func TestSortAndAggregateEdges(t *testing.T) {
	sorted := func(strategy merge.Strategy) MergeOpts {
		return MergeOpts{SortPoints: true, Strategy: strategy}
	}
	t.Run("dedup delegates", func(t *testing.T) {
		out := mergePoints(makeStringPoints(ev{100, "1"}, ev{100, "2"}, ev{200, "3"}), nil,
			sorted(merge.StrategyDedup))
		require.Len(t, out, 2)
		require.Equal(t, "2", out[0].Values[0])
	})

	t.Run("single point", func(t *testing.T) {
		out := mergePoints(makeStringPoints(ev{100, "1"}), nil, sorted(merge.StrategySum))
		require.Len(t, out, 1)
		require.Equal(t, "1", out[0].Values[0])
	})

	t.Run("empty", func(t *testing.T) {
		require.Empty(t, mergePoints(Points{}, nil, sorted(merge.StrategySum)))
	})
}

func TestAggregateValuesWithOperationsEdges(t *testing.T) {
	sum := func(ops ValueMergeOperations) MergeOpts {
		return MergeOpts{SortPoints: true, Strategy: merge.StrategySum, ValueOperations: ops}
	}
	t.Run("a null takes a numeric peer's value", func(t *testing.T) {
		// a row's null first value is not a number, so a sum keeps its numeric peer's value
		out := mergePoints(Points{{Epoch: 1, Values: []any{nil}}}, Points{{Epoch: 1, Values: []any{"1"}}}, sum(nil))
		require.Equal(t, "1", out[0].Values[0])
		out = mergePoints(Points{{Epoch: 1, Values: []any{"1"}}}, Points{{Epoch: 1, Values: []any{nil}}}, sum(nil))
		require.Equal(t, "1", out[0].Values[0])
	})

	t.Run("both non-numeric with ops", func(t *testing.T) {
		ops := &stubValueOps{mergeHandled: true, merged: "merged-hist"}
		out := mergePoints(makeStringPoints(ev{1, "hist-a"}), makeStringPoints(ev{1, "hist-b"}), sum(ops))
		require.Equal(t, "merged-hist", out[0].Values[0])
	})

	t.Run("both non-numeric ops not handled", func(t *testing.T) {
		ops := &stubValueOps{mergeHandled: false}
		out := mergePoints(makeStringPoints(ev{1, "hist-a"}), makeStringPoints(ev{1, "hist-b"}), sum(ops))
		require.Equal(t, "hist-a", out[0].Values[0])
	})
}

func TestFinalizeAvgWithOperationsEdges(t *testing.T) {
	avg := func(ops ValueMergeOperations) MergeOpts {
		return MergeOpts{SortPoints: true, Strategy: merge.StrategyAvg, ValueOperations: ops}
	}
	t.Run("nan with ops", func(t *testing.T) {
		ops := &stubValueOps{mergeHandled: true, merged: "hist", divideHandled: true, divided: "avg-hist"}
		out := mergePoints(makeStringPoints(ev{1, "hist"}), makeStringPoints(ev{1, "hist"}), avg(ops))
		require.Equal(t, "avg-hist", out[0].Values[0])
	})

	t.Run("nan ops not handled", func(t *testing.T) {
		ops := &stubValueOps{}
		out := mergePoints(makeStringPoints(ev{1, "hist"}), makeStringPoints(ev{1, "hist2"}), avg(ops))
		require.Equal(t, "hist", out[0].Values[0])
	})
}

func TestMergePointsWithOptsNonDedupEdges(t *testing.T) {
	t.Run("nil both", func(t *testing.T) {
		require.Nil(t, mergePoints(nil, nil, MergeOpts{Strategy: merge.StrategySum}))
	})

	t.Run("empty both", func(t *testing.T) {
		require.Empty(t, mergePoints(Points{}, Points{}, MergeOpts{Strategy: merge.StrategySum}))
	})

	t.Run("only p2 empty sorts", func(t *testing.T) {
		p1 := makeStringPoints(ev{200, "2"}, ev{100, "1"}, ev{100, "3"})
		out := mergePoints(p1, Points{}, MergeOpts{
			SortPoints: true,
			Strategy:   merge.StrategySum,
		})
		require.Len(t, out, 2)
		require.Equal(t, epoch.Epoch(100), out[0].Epoch)
		require.Equal(t, "4", out[0].Values[0])
	})

	t.Run("only p1 empty sorts", func(t *testing.T) {
		p2 := makeStringPoints(ev{200, "2"}, ev{100, "1"})
		out := mergePoints(Points{}, p2, MergeOpts{
			SortPoints: true,
			Strategy:   merge.StrategyMin,
		})
		require.Len(t, out, 2)
		require.Equal(t, epoch.Epoch(100), out[0].Epoch)
	})

	t.Run("count with empty values", func(t *testing.T) {
		p1 := Points{{Epoch: 100, Values: nil}, {Epoch: 100, Values: []any{"9"}}}
		p2 := Points{}
		out := mergePoints(p1, p2, MergeOpts{
			SortPoints: true,
			Strategy:   merge.StrategyCount,
		})
		require.Len(t, out, 1)
		// a point without values holds a null in the series' column, and counts as a row
		require.Equal(t, "2", out[0].Values[0])
	})

	t.Run("avg with value operations", func(t *testing.T) {
		ops := &stubValueOps{mergeHandled: true, merged: "h", divideHandled: true, divided: "h/2"}
		p1 := makeStringPoints(ev{100, "hist-a"})
		p2 := makeStringPoints(ev{100, "hist-b"})
		out := mergePoints(p1, p2, MergeOpts{
			SortPoints:      true,
			Strategy:        merge.StrategyAvg,
			ValueOperations: ops,
		})
		require.Len(t, out, 1)
		require.Equal(t, "h/2", out[0].Values[0])
	})
}
