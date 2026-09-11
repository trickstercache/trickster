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
			Size:   32,
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
	result := MergePointsWithStrategy(p1, p2, true, merge.StrategySum)
	require.Len(t, result, 2)
	require.Equal(t, "4", result[0].Values[0])
	require.Equal(t, "6", result[1].Values[0])
}

func TestMergePointsWithStrategyDedup(t *testing.T) {
	p1 := makeStringPoints(ev{100, "1.0"}, ev{200, "2.0"})
	p2 := makeStringPoints(ev{100, "3.0"}, ev{300, "4.0"})
	result := MergePointsWithStrategy(p1, p2, true, merge.StrategyDedup)
	require.Len(t, result, 3)
}

func TestMergePointsWithStrategyNilInputs(t *testing.T) {
	require.Nil(t, MergePointsWithStrategy(nil, nil, true, merge.StrategySum))
	require.Len(t, MergePointsWithStrategy(Points{}, Points{}, true, merge.StrategySum), 0)
}

func TestMergePointsWithStrategyCount(t *testing.T) {
	p1 := makeStringPoints(ev{100, "99.0"}, ev{200, "88.0"})
	p2 := makeStringPoints(ev{100, "77.0"}, ev{200, "66.0"})
	result := MergePointsWithStrategy(p1, p2, true, merge.StrategyCount)
	require.Len(t, result, 2)
	require.Equal(t, "2", result[0].Values[0])
	require.Equal(t, "2", result[1].Values[0])
}

func TestMergePointsWithStrategyAvg(t *testing.T) {
	p1 := makeStringPoints(ev{100, "10.0"}, ev{200, "20.0"})
	p2 := makeStringPoints(ev{100, "30.0"}, ev{200, "40.0"})
	result := MergePointsWithStrategy(p1, p2, true, merge.StrategyAvg)
	require.Len(t, result, 2)
	require.Equal(t, "20", result[0].Values[0])
	require.Equal(t, "30", result[1].Values[0])
}

func TestMergePointsWithStrategyScalar(t *testing.T) {
	t.Run("finite replaces NaN", func(t *testing.T) {
		result := MergePointsWithStrategy(
			makeStringPoints(ev{100, "NaN"}),
			makeStringPoints(ev{100, "42"}), true, merge.StrategyScalar)
		require.Len(t, result, 1)
		require.Equal(t, "42", result[0].Values[0])
	})

	t.Run("first finite member wins", func(t *testing.T) {
		result := MergePointsWithStrategy(
			makeStringPoints(ev{100, "42"}),
			makeStringPoints(ev{100, "99"}), true, merge.StrategyScalar)
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
	result := MergePointsWithStrategy(p1, p2, true, merge.StrategySum)
	require.Len(t, result, 2)
	require.Equal(t, hist, result[0].Values[0])
	require.Equal(t, "6", result[1].Values[0])
}

func TestFinalizeAvgNaN(t *testing.T) {
	hist := `{"count":"10","sum":"100"}`
	p := Point{Epoch: 100, Values: []any{hist}}
	finalizeAvg(&p, 3)
	require.Equal(t, hist, p.Values[0])
}

func TestFinalizeAvgNumeric(t *testing.T) {
	p := Point{Epoch: 100, Values: []any{"12"}}
	finalizeAvg(&p, 3)
	require.Equal(t, "4", p.Values[0])
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

func TestSortAndAggregateTolerantEdges(t *testing.T) {
	t.Run("dedup delegates", func(t *testing.T) {
		p := makeStringPoints(ev{100, "1"}, ev{100, "2"}, ev{200, "3"})
		out := sortAndAggregateTolerant(p, merge.StrategyDedup, 0, nil)
		require.Len(t, out, 2)
		require.Equal(t, "2", out[0].Values[0])
	})

	t.Run("single point early return", func(t *testing.T) {
		p := makeStringPoints(ev{100, "1"})
		out := sortAndAggregateTolerant(p, merge.StrategySum, 0, nil)
		require.Len(t, out, 1)
		require.Equal(t, "1", out[0].Values[0])
	})

	t.Run("empty early return", func(t *testing.T) {
		out := sortAndAggregateTolerant(Points{}, merge.StrategySum, 0, nil)
		require.Empty(t, out)
	})
}

func TestAggregateValuesWithOperationsEdges(t *testing.T) {
	t.Run("empty values no-op", func(t *testing.T) {
		dst := Point{Epoch: 1, Values: nil}
		src := Point{Epoch: 1, Values: []any{"1"}}
		aggregateValuesWithOperations(&dst, &src, merge.StrategySum, nil)
		require.Nil(t, dst.Values)

		dst = Point{Epoch: 1, Values: []any{"1"}}
		src = Point{Epoch: 1, Values: nil}
		aggregateValuesWithOperations(&dst, &src, merge.StrategySum, nil)
		require.Equal(t, "1", dst.Values[0])
	})

	t.Run("both non-numeric with ops", func(t *testing.T) {
		ops := &stubValueOps{mergeHandled: true, merged: "merged-hist"}
		dst := Point{Epoch: 1, Size: 40, Values: []any{"hist-a"}}
		src := Point{Epoch: 1, Values: []any{"hist-b"}}
		aggregateValuesWithOperations(&dst, &src, merge.StrategySum, ops)
		require.Equal(t, "merged-hist", dst.Values[0])
		require.Equal(t, 40+len("merged-hist")-len("hist-a"), dst.Size)
	})

	t.Run("both non-numeric ops not handled", func(t *testing.T) {
		ops := &stubValueOps{mergeHandled: false}
		dst := Point{Epoch: 1, Values: []any{"hist-a"}}
		src := Point{Epoch: 1, Values: []any{"hist-b"}}
		aggregateValuesWithOperations(&dst, &src, merge.StrategySum, ops)
		require.Equal(t, "hist-a", dst.Values[0])
	})
}

func TestFinalizeAvgWithOperationsEdges(t *testing.T) {
	t.Run("count le 1 or empty", func(t *testing.T) {
		p := Point{Epoch: 1, Values: []any{"10"}}
		finalizeAvgWithOperations(&p, 1, nil)
		require.Equal(t, "10", p.Values[0])

		p = Point{Epoch: 1, Values: nil}
		finalizeAvgWithOperations(&p, 3, nil)
		require.Nil(t, p.Values)
	})

	t.Run("nan with ops", func(t *testing.T) {
		ops := &stubValueOps{divideHandled: true, divided: "avg-hist"}
		p := Point{Epoch: 1, Size: 30, Values: []any{"hist"}}
		finalizeAvgWithOperations(&p, 2, ops)
		require.Equal(t, "avg-hist", p.Values[0])
		require.Equal(t, 30+len("avg-hist")-len("hist"), p.Size)
	})

	t.Run("nan ops not handled", func(t *testing.T) {
		ops := &stubValueOps{divideHandled: false}
		p := Point{Epoch: 1, Values: []any{"hist"}}
		finalizeAvgWithOperations(&p, 2, ops)
		require.Equal(t, "hist", p.Values[0])
	})
}

func TestSetPointValue(t *testing.T) {
	p := Point{Size: 10, Values: []any{"abc"}}
	setPointValue(&p, 0, "abcdef")
	require.Equal(t, "abcdef", p.Values[0])
	require.Equal(t, 13, p.Size)

	p = Point{Size: 0, Values: []any{"abc"}}
	setPointValue(&p, 0, "xyz")
	require.Equal(t, "xyz", p.Values[0])
	require.Equal(t, 0, p.Size)

	p = Point{Size: 5, Values: []any{1}}
	setPointValue(&p, 0, 2)
	require.Equal(t, 2, p.Values[0])
	require.Equal(t, 5, p.Size)
}

func TestMergePointsWithOptsNonDedupEdges(t *testing.T) {
	t.Run("nil both", func(t *testing.T) {
		require.Nil(t, MergePointsWithOpts(nil, nil, MergeOpts{Strategy: merge.StrategySum}))
	})

	t.Run("empty both", func(t *testing.T) {
		out := MergePointsWithOpts(Points{}, Points{}, MergeOpts{Strategy: merge.StrategySum})
		require.NotNil(t, out)
		require.Empty(t, out)
	})

	t.Run("only p2 empty sorts", func(t *testing.T) {
		p1 := makeStringPoints(ev{200, "2"}, ev{100, "1"}, ev{100, "3"})
		out := MergePointsWithOpts(p1, Points{}, MergeOpts{
			SortPoints: true,
			Strategy:   merge.StrategySum,
		})
		require.Len(t, out, 2)
		require.Equal(t, epoch.Epoch(100), out[0].Epoch)
		require.Equal(t, "4", out[0].Values[0])
	})

	t.Run("only p1 empty sorts", func(t *testing.T) {
		p2 := makeStringPoints(ev{200, "2"}, ev{100, "1"})
		out := MergePointsWithOpts(Points{}, p2, MergeOpts{
			SortPoints: true,
			Strategy:   merge.StrategyMin,
		})
		require.Len(t, out, 2)
		require.Equal(t, epoch.Epoch(100), out[0].Epoch)
	})

	t.Run("count with empty values", func(t *testing.T) {
		p1 := Points{{Epoch: 100, Values: nil}, {Epoch: 100, Values: []any{"9"}}}
		p2 := Points{}
		out := MergePointsWithOpts(p1, p2, MergeOpts{
			SortPoints: true,
			Strategy:   merge.StrategyCount,
		})
		require.Len(t, out, 1)
		// The empty-values point is kept as-is during initCountValues; when it
		// is the aggregate destination, aggregateValues no-ops on empty Values.
		if len(out[0].Values) > 0 {
			require.Equal(t, "1", out[0].Values[0])
		}
	})

	t.Run("avg with value operations", func(t *testing.T) {
		ops := &stubValueOps{mergeHandled: true, merged: "h", divideHandled: true, divided: "h/2"}
		p1 := makeStringPoints(ev{100, "hist-a"})
		p2 := makeStringPoints(ev{100, "hist-b"})
		out := MergePointsWithOpts(p1, p2, MergeOpts{
			SortPoints:      true,
			Strategy:        merge.StrategyAvg,
			ValueOperations: ops,
		})
		require.Len(t, out, 1)
		require.Equal(t, "h/2", out[0].Values[0])
	})
}
