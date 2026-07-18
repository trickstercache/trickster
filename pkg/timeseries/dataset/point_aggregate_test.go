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

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
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

func TestParseFloat(t *testing.T) {
	require.Equal(t, 1.5, parseFloat("1.5"))
	require.Equal(t, 1.5, parseFloat(float64(1.5)))
	require.True(t, math.IsNaN(parseFloat("not_a_number")))
	require.True(t, math.IsNaN(parseFloat(42))) // int, not float64 or string
}

func TestAggregateValuesHistogramBothNonNumeric(t *testing.T) {
	histA := `{"count":"10","sum":"100","buckets":[[0,"1","2","3"]]}`
	histB := `{"count":"20","sum":"200","buckets":[[0,"1","2","5"]]}`
	dst := Point{Epoch: 100, Values: []any{histA}}
	src := Point{Epoch: 100, Values: []any{histB}}
	aggregateValues(&dst, &src, merge.StrategySum)
	require.Equal(t, histA, dst.Values[0])
}

func TestAggregateValuesHistogramOneNumeric(t *testing.T) {
	hist := `{"count":"10","sum":"100","buckets":[[0,"1","2","3"]]}`
	dst := Point{Epoch: 100, Values: []any{hist}}
	src := Point{Epoch: 100, Values: []any{"5.0"}}
	aggregateValues(&dst, &src, merge.StrategySum)
	require.Equal(t, "5.0", dst.Values[0])

	dst2 := Point{Epoch: 100, Values: []any{"5.0"}}
	src2 := Point{Epoch: 100, Values: []any{hist}}
	aggregateValues(&dst2, &src2, merge.StrategySum)
	require.Equal(t, "5.0", dst2.Values[0])
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
