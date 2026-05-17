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
)

// nonOptsMergerTS wraps timeseries.Timeseries via an embedded interface so
// only the interface methods are reachable; the underlying *DataSet's
// MergeWithStrategyTolerant and MergeWithStrategy are not promoted. This
// drives the legacy accum.tsdata.Merge fallback path in the tolerant
// merge adapters.
type nonOptsMergerTS struct {
	timeseries.Timeseries
}

func TestTimeseriesMergeFuncTolerant(t *testing.T) {
	unmarshaler := func([]byte, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		return nil, nil
	}

	t.Run("optsMerger path collapses sub-tolerance epochs", func(t *testing.T) {
		accum := NewAccumulator()
		// Same logical timestamp landing on neighboring nanos from independent
		// shards. With tolerance >= 3ns, both clusters collapse to one point.
		ds1 := makeTestDataSet(0, "up", nil, []int64{1000}, []string{"1"})
		ds2 := makeTestDataSet(0, "up", nil, []int64{1003}, []string{"2"})

		mf := TimeseriesMergeFuncTolerant(unmarshaler, 5)
		require.NoError(t, mf(accum, ds1, 0))
		require.NoError(t, mf(accum, ds2, 1))

		ds, ok := accum.GetTSData().(*dataset.DataSet)
		require.True(t, ok)
		require.Equal(t, 1, ds.SeriesCount())
		pts := ds.Results[0].SeriesList[0].Points
		require.Len(t, pts, 1, "sub-tolerance epochs should collapse to a single point")
	})

	t.Run("tolerance zero preserves legacy semantics", func(t *testing.T) {
		accum := NewAccumulator()
		ds1 := makeTestDataSet(0, "up", nil, []int64{1000}, []string{"1"})
		ds2 := makeTestDataSet(0, "up", nil, []int64{1003}, []string{"2"})

		mf := TimeseriesMergeFuncTolerant(unmarshaler, 0)
		require.NoError(t, mf(accum, ds1, 0))
		require.NoError(t, mf(accum, ds2, 1))

		ds, ok := accum.GetTSData().(*dataset.DataSet)
		require.True(t, ok)
		require.Equal(t, 1, ds.SeriesCount())
		pts := ds.Results[0].SeriesList[0].Points
		require.Len(t, pts, 2, "distinct epochs without tolerance should not collapse")
	})

	t.Run("non-optsMerger falls back to plain Merge", func(t *testing.T) {
		accum := NewAccumulator()
		// Seed the accumulator with a wrapper that hides the
		// MergeWithStrategyTolerant method, forcing the fallback path.
		seedDS := makeTestDataSet(0, "up", nil, []int64{1000}, []string{"1"})
		accum.SetTSData(nonOptsMergerTS{Timeseries: seedDS})

		next := makeTestDataSet(0, "up", nil, []int64{1003}, []string{"2"})

		mf := TimeseriesMergeFuncTolerant(unmarshaler, 5)
		require.NoError(t, mf(accum, next, 0))

		// The fallback path calls Merge(false, ts), which delegates to the
		// embedded *DataSet's Merge. Compare against an independent baseline
		// produced by the legacy path.
		got, ok := accum.GetTSData().(nonOptsMergerTS)
		require.True(t, ok, "accumulator should still hold the non-optsMerger wrapper")
		gotDS, ok := got.Timeseries.(*dataset.DataSet)
		require.True(t, ok)

		expectedAccum := NewAccumulator()
		legacy := TimeseriesMergeFunc(unmarshaler)
		require.NoError(t, legacy(expectedAccum,
			makeTestDataSet(0, "up", nil, []int64{1000}, []string{"1"}), 0))
		require.NoError(t, legacy(expectedAccum,
			makeTestDataSet(0, "up", nil, []int64{1003}, []string{"2"}), 0))
		wantDS, ok := expectedAccum.GetTSData().(*dataset.DataSet)
		require.True(t, ok)

		require.Equal(t, wantDS.SeriesCount(), gotDS.SeriesCount())
		require.Equal(t,
			len(wantDS.Results[0].SeriesList[0].Points),
			len(gotDS.Results[0].SeriesList[0].Points),
			"fallback should match legacy Merge point count")
	})
}

func TestTimeseriesMergeFuncWithStrategyTolerant(t *testing.T) {
	unmarshaler := func([]byte, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		return nil, nil
	}

	t.Run("tolerant dedup collapses sub-tolerance epochs", func(t *testing.T) {
		accum := NewAccumulator()
		ds1 := makeTestDataSet(0, "up", nil, []int64{1000}, []string{"1"})
		ds2 := makeTestDataSet(0, "up", nil, []int64{1003}, []string{"2"})

		mf := TimeseriesMergeFuncWithStrategyTolerant(unmarshaler, int(dataset.MergeStrategyDedup), 5)
		require.NoError(t, mf(accum, ds1, 0))
		require.NoError(t, mf(accum, ds2, 1))

		ds, ok := accum.GetTSData().(*dataset.DataSet)
		require.True(t, ok)
		require.Equal(t, 1, ds.SeriesCount())
		require.Len(t, ds.Results[0].SeriesList[0].Points, 1)
		require.Equal(t, 2, accum.MergeCount)
	})

	t.Run("avg rewrites to sum for pairwise accumulation", func(t *testing.T) {
		accum := NewAccumulator()
		ds1 := makeTestDataSet(0, "latency", nil, []int64{100}, []string{"10"})
		ds2 := makeTestDataSet(0, "latency", nil, []int64{100}, []string{"30"})
		ds3 := makeTestDataSet(0, "latency", nil, []int64{100}, []string{"20"})

		mf := TimeseriesMergeFuncWithStrategyTolerant(unmarshaler, int(dataset.MergeStrategyAvg), 0)
		require.NoError(t, mf(accum, ds1, 0))
		require.NoError(t, mf(accum, ds2, 1))
		require.NoError(t, mf(accum, ds3, 2))
		require.Equal(t, 3, accum.MergeCount)

		ds, ok := accum.GetTSData().(*dataset.DataSet)
		require.True(t, ok)
		// Pairwise sums accumulated; final divide happens in RespondFunc.
		require.Equal(t, "60", ds.Results[0].SeriesList[0].Points[0].Values[0])
	})

	t.Run("non-optsMerger with strategy falls back to strategyMerger", func(t *testing.T) {
		// When tolerance > 0 but the accumulator type is non-optsMerger, the
		// adapter still tries strategyMerger before falling back to plain Merge.
		// Wrapping a *DataSet via the interface embed hides BOTH optsMerger and
		// strategyMerger, so this exercises the final Merge fallback.
		accum := NewAccumulator()
		seedDS := makeTestDataSet(0, "up", nil, []int64{100}, []string{"5"})
		accum.SetTSData(nonOptsMergerTS{Timeseries: seedDS})
		next := makeTestDataSet(0, "up", nil, []int64{200}, []string{"7"})

		mf := TimeseriesMergeFuncWithStrategyTolerant(unmarshaler, int(dataset.MergeStrategySum), 5)
		require.NoError(t, mf(accum, next, 0))

		got, ok := accum.GetTSData().(nonOptsMergerTS)
		require.True(t, ok)
		gotDS, ok := got.Timeseries.(*dataset.DataSet)
		require.True(t, ok)
		require.Equal(t, 1, gotDS.SeriesCount())
		require.Equal(t, 1, accum.MergeCount)
	})

	t.Run("byte input is unmarshaled", func(t *testing.T) {
		accum := NewAccumulator()
		ds1 := makeTestDataSet(0, "up", nil, []int64{1000}, []string{"5"})

		var called bool
		unmarshalerCapture := func(b []byte, _ *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
			called = true
			return ds1, nil
		}

		mf := TimeseriesMergeFuncWithStrategyTolerant(unmarshalerCapture, int(dataset.MergeStrategyDedup), 5)
		require.NoError(t, mf(accum, []byte("fake"), 0))
		require.True(t, called)
		require.NotNil(t, accum.GetTSData())
	})
}
