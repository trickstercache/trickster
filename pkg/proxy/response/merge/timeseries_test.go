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

package merge

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func makeTestDataSet(stmtID int, name string, tags dataset.Tags, epochs []int64, values []string) *dataset.DataSet {
	points := make(dataset.Points, len(epochs))
	for i := range epochs {
		points[i] = dataset.Point{
			Epoch:  epoch.Epoch(epochs[i]),
			Size:   32,
			Values: []any{values[i]},
		}
	}
	return &dataset.DataSet{
		Results: dataset.Results{
			{StatementID: stmtID, SeriesList: dataset.SeriesList{
				{Header: dataset.SeriesHeader{Name: name, Tags: tags}, Points: points},
			}},
		},
	}
}

func TestTimeseriesMergeFunc(t *testing.T) {
	accum := NewAccumulator()
	ds1 := makeTestDataSet(0, "up", nil, []int64{100, 200}, []string{"1", "2"})
	ds2 := makeTestDataSet(0, "up", nil, []int64{100, 200}, []string{"3", "4"})

	unmarshaler := func([]byte, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		return nil, nil
	}
	mf := TimeseriesMergeFunc(unmarshaler)
	require.NoError(t, mf(accum, ds1, 0))
	require.NoError(t, mf(accum, ds2, 1))

	ts := accum.GetTSData()
	require.NotNil(t, ts)
	ds, ok := ts.(*dataset.DataSet)
	require.True(t, ok)
	// default merge uses sortPoints=false, so points are concatenated (not deduped)
	require.Equal(t, 1, ds.SeriesCount())
	require.Len(t, ds.Results[0].SeriesList[0].Points, 4) // 2+2 concatenated
}

func TestTimeseriesMergeFuncWithStrategy_Sum(t *testing.T) {
	accum := NewAccumulator()
	ds1 := makeTestDataSet(0, "up", nil, []int64{100, 200}, []string{"1", "2"})
	ds2 := makeTestDataSet(0, "up", nil, []int64{100, 200}, []string{"3", "4"})

	unmarshaler := func([]byte, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		return nil, nil
	}
	mf := TimeseriesMergeFuncWithStrategy(unmarshaler, int(dataset.MergeStrategySum))
	require.NoError(t, mf(accum, ds1, 0))
	require.NoError(t, mf(accum, ds2, 1))

	ts := accum.GetTSData()
	require.NotNil(t, ts)
	ds, ok := ts.(*dataset.DataSet)
	require.True(t, ok)
	require.Equal(t, 1, ds.SeriesCount())
	pts := ds.Results[0].SeriesList[0].Points
	require.Len(t, pts, 2)
	require.Equal(t, "4", pts[0].Values[0]) // 1+3
	require.Equal(t, "6", pts[1].Values[0]) // 2+4
}

func TestTimeseriesMergeFuncWithStrategy_Avg(t *testing.T) {
	accum := NewAccumulator()
	ds1 := makeTestDataSet(0, "latency", nil, []int64{100}, []string{"10"})
	ds2 := makeTestDataSet(0, "latency", nil, []int64{100}, []string{"30"})
	ds3 := makeTestDataSet(0, "latency", nil, []int64{100}, []string{"20"})

	unmarshaler := func([]byte, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		return nil, nil
	}
	mf := TimeseriesMergeFuncWithStrategy(unmarshaler, int(dataset.MergeStrategyAvg))
	require.NoError(t, mf(accum, ds1, 0))
	require.NoError(t, mf(accum, ds2, 1))
	require.NoError(t, mf(accum, ds3, 2))
	require.Equal(t, 3, accum.MergeCount)

	// Before finalization, values are accumulated sums
	ds, ok := accum.GetTSData().(*dataset.DataSet)
	require.True(t, ok)
	require.Equal(t, "60", ds.Results[0].SeriesList[0].Points[0].Values[0]) // 10+30+20

	// Simulate what TimeseriesRespondFuncWithStrategy does: finalize avg
	ds.FinalizeAvg(accum.MergeCount)
	require.Equal(t, "20", ds.Results[0].SeriesList[0].Points[0].Values[0]) // 60/3
}

func TestTimeseriesMergeFuncWithStrategy_NonDataSet(t *testing.T) {
	// When the accumulator holds a non-DataSet Timeseries, it should fall back to Merge
	accum := NewAccumulator()
	unmarshaler := func([]byte, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		return nil, nil
	}
	mf := TimeseriesMergeFuncWithStrategy(unmarshaler, int(dataset.MergeStrategySum))
	require.Error(t, mf(accum, 42, 0))
	require.Nil(t, accum.GetTSData())
}

func TestTimeseriesMergeFuncWithStrategy_ByteInput(t *testing.T) {
	// When data is []byte, it should unmarshal then merge
	accum := NewAccumulator()
	ds1 := makeTestDataSet(0, "up", nil, []int64{100}, []string{"5"})

	calledUnmarshal := false
	unmarshaler := func(b []byte, _ *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		calledUnmarshal = true
		return ds1, nil
	}

	mf := TimeseriesMergeFuncWithStrategy(unmarshaler, int(dataset.MergeStrategySum))
	require.NoError(t, mf(accum, []byte("fake"), 0))
	require.True(t, calledUnmarshal)
	require.NotNil(t, accum.GetTSData())
}
