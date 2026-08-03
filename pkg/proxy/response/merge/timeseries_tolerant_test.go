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
	"errors"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/stretchr/testify/require"
)

var errUnmarshal = errors.New("unmarshal failed")

// nonOptsMergerTS wraps timeseries.Timeseries via an embedded interface so
// only the interface methods are reachable; the underlying *DataSet's
// MergeWithStrategyTolerant and MergeWithStrategy are not promoted. This
// drives the legacy accum.tsdata.Merge fallback path in the tolerant
// merge adapters.
type nonOptsMergerTS struct {
	timeseries.Timeseries
}

// strategyOnlyTS exposes MergeWithStrategy but not MergeWithStrategyTolerant,
// covering the strategyMerger fallback when tolerance > 0.
type strategyOnlyTS struct {
	timeseries.Timeseries
}

func (s strategyOnlyTS) MergeWithStrategy(sortPoints bool, strategy int, collection ...timeseries.Timeseries) {
	if ds, ok := s.Timeseries.(*dataset.DataSet); ok {
		ds.MergeWithStrategy(sortPoints, strategy, collection...)
	}
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

		mf := TimeseriesMergeFuncWithStrategyTolerant(unmarshaler, int(merge.StrategyDedup), 5)
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

		mf := TimeseriesMergeFuncWithStrategyTolerant(unmarshaler, int(merge.StrategyAvg), 0)
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

		mf := TimeseriesMergeFuncWithStrategyTolerant(unmarshaler, int(merge.StrategySum), 5)
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

		mf := TimeseriesMergeFuncWithStrategyTolerant(unmarshalerCapture, int(merge.StrategyDedup), 5)
		require.NoError(t, mf(accum, []byte("fake"), 0))
		require.True(t, called)
		require.NotNil(t, accum.GetTSData())
	})

	t.Run("unmarshal error is returned", func(t *testing.T) {
		accum := NewAccumulator()
		mf := TimeseriesMergeFuncWithStrategyTolerant(
			func([]byte, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
				return nil, errUnmarshal
			},
			int(merge.StrategySum),
			5,
		)
		err := mf(accum, []byte("bad"), 0)
		require.ErrorIs(t, err, errUnmarshal)
	})

	t.Run("strategyOnly falls back to MergeWithStrategy", func(t *testing.T) {
		accum := NewAccumulator()
		seed := makeTestDataSet(0, "up", nil, []int64{100}, []string{"5"})
		accum.SetTSData(strategyOnlyTS{Timeseries: seed})
		next := makeTestDataSet(0, "up", nil, []int64{100}, []string{"7"})

		mf := TimeseriesMergeFuncWithStrategyTolerant(unmarshaler, int(merge.StrategySum), 5)
		require.NoError(t, mf(accum, next, 0))
		require.Equal(t, 1, accum.MergeCount)

		got, ok := accum.GetTSData().(strategyOnlyTS)
		require.True(t, ok)
		gotDS, ok := got.Timeseries.(*dataset.DataSet)
		require.True(t, ok)
		require.Equal(t, "12", gotDS.Results[0].SeriesList[0].Points[0].Values[0])
	})
}

func TestTimeseriesBatchMergeFuncTolerant(t *testing.T) {
	t.Run("batches decoded timeseries with tolerance", func(t *testing.T) {
		accum := NewAccumulator()
		items := []BatchItem{
			{Data: makeTestDataSet(0, "up", nil, []int64{1000}, []string{"1"}), Member: 0},
			{Data: makeTestDataSet(0, "up", nil, []int64{1003}, []string{"2"}), Member: 1},
		}

		handled, err := TimeseriesBatchMergeFuncTolerant(5)(accum, items)
		require.NoError(t, err)
		require.True(t, handled)
		ds, ok := accum.GetTSData().(*dataset.DataSet)
		require.True(t, ok)
		require.Len(t, ds.Results[0].SeriesList[0].Points, 1)
	})

	t.Run("incompatible input leaves accumulator untouched", func(t *testing.T) {
		accum := NewAccumulator()
		seed := makeTestDataSet(0, "up", nil, []int64{1000}, []string{"1"})
		accum.SetTSData(seed)

		handled, err := TimeseriesBatchMergeFunc()(accum, []BatchItem{{Data: []byte("wire")}})
		require.NoError(t, err)
		require.False(t, handled)
		require.Same(t, seed, accum.GetTSData())
		require.Len(t, seed.Results[0].SeriesList[0].Points, 1)
	})

	t.Run("empty items is not handled", func(t *testing.T) {
		handled, err := TimeseriesBatchMergeFunc()(NewAccumulator(), nil)
		require.NoError(t, err)
		require.False(t, handled)
	})

	t.Run("non-optsMerger falls back to plain Merge", func(t *testing.T) {
		accum := NewAccumulator()
		seed := makeTestDataSet(0, "up", nil, []int64{1000}, []string{"1"})
		accum.SetTSData(nonOptsMergerTS{Timeseries: seed})
		next := makeTestDataSet(0, "up", nil, []int64{1003}, []string{"2"})

		handled, err := TimeseriesBatchMergeFuncTolerant(5)(accum, []BatchItem{
			{Data: next, Member: 0},
		})
		require.NoError(t, err)
		require.True(t, handled)

		got, ok := accum.GetTSData().(nonOptsMergerTS)
		require.True(t, ok)
		gotDS, ok := got.Timeseries.(*dataset.DataSet)
		require.True(t, ok)
		require.Len(t, gotDS.Results[0].SeriesList[0].Points, 2)
	})

	t.Run("zero tolerance merges without tolerant path", func(t *testing.T) {
		accum := NewAccumulator()
		items := []BatchItem{
			{Data: makeTestDataSet(0, "up", nil, []int64{1000}, []string{"1"}), Member: 0},
			{Data: makeTestDataSet(0, "up", nil, []int64{2000}, []string{"2"}), Member: 1},
		}

		handled, err := TimeseriesBatchMergeFuncTolerant(0)(accum, items)
		require.NoError(t, err)
		require.True(t, handled)
		ds, ok := accum.GetTSData().(*dataset.DataSet)
		require.True(t, ok)
		require.Len(t, ds.Results[0].SeriesList[0].Points, 2)
	})
}

func TestTimeseriesBatchMergeFuncWithStrategyTolerant(t *testing.T) {
	t.Run("avg accumulates sums", func(t *testing.T) {
		accum := NewAccumulator()
		items := []BatchItem{
			{Data: makeTestDataSet(0, "latency", nil, []int64{100}, []string{"10"}), Member: 0},
			{Data: makeTestDataSet(0, "latency", nil, []int64{100}, []string{"30"}), Member: 1},
			{Data: makeTestDataSet(0, "latency", nil, []int64{100}, []string{"20"}), Member: 2},
		}

		handled, err := TimeseriesBatchMergeFuncWithStrategyTolerant(
			int(merge.StrategyAvg), 0)(accum, items)
		require.NoError(t, err)
		require.True(t, handled)
		require.Equal(t, 3, accum.MergeCount)
		ds, ok := accum.GetTSData().(*dataset.DataSet)
		require.True(t, ok)
		require.Equal(t, "60", ds.Results[0].SeriesList[0].Points[0].Values[0])
	})

	t.Run("incompatible input is not handled", func(t *testing.T) {
		handled, err := TimeseriesBatchMergeFuncWithStrategyTolerant(
			int(merge.StrategySum), 0)(NewAccumulator(), []BatchItem{{Data: []byte("wire")}})
		require.NoError(t, err)
		require.False(t, handled)
	})

	t.Run("tolerant optsMerger path", func(t *testing.T) {
		accum := NewAccumulator()
		items := []BatchItem{
			{Data: makeTestDataSet(0, "up", nil, []int64{1000}, []string{"1"}), Member: 0},
			{Data: makeTestDataSet(0, "up", nil, []int64{1003}, []string{"2"}), Member: 1},
		}

		handled, err := TimeseriesBatchMergeFuncWithStrategyTolerant(
			int(merge.StrategyDedup), 5)(accum, items)
		require.NoError(t, err)
		require.True(t, handled)
		ds, ok := accum.GetTSData().(*dataset.DataSet)
		require.True(t, ok)
		require.Len(t, ds.Results[0].SeriesList[0].Points, 1)
	})

	t.Run("strategyOnly falls back to MergeWithStrategy", func(t *testing.T) {
		accum := NewAccumulator()
		seed := makeTestDataSet(0, "up", nil, []int64{100}, []string{"5"})
		accum.SetTSData(strategyOnlyTS{Timeseries: seed})
		next := makeTestDataSet(0, "up", nil, []int64{100}, []string{"7"})

		handled, err := TimeseriesBatchMergeFuncWithStrategyTolerant(
			int(merge.StrategySum), 5)(accum, []BatchItem{{Data: next, Member: 0}})
		require.NoError(t, err)
		require.True(t, handled)
		require.Equal(t, 1, accum.MergeCount)

		got, ok := accum.GetTSData().(strategyOnlyTS)
		require.True(t, ok)
		gotDS, ok := got.Timeseries.(*dataset.DataSet)
		require.True(t, ok)
		require.Equal(t, "12", gotDS.Results[0].SeriesList[0].Points[0].Values[0])
	})

	t.Run("non-optsMerger falls back to plain Merge", func(t *testing.T) {
		accum := NewAccumulator()
		seed := makeTestDataSet(0, "up", nil, []int64{100}, []string{"1"})
		accum.SetTSData(nonOptsMergerTS{Timeseries: seed})
		next := makeTestDataSet(0, "up", nil, []int64{200}, []string{"2"})

		handled, err := TimeseriesBatchMergeFuncWithStrategyTolerant(
			int(merge.StrategySum), 5)(accum, []BatchItem{{Data: next, Member: 0}})
		require.NoError(t, err)
		require.True(t, handled)
		require.Equal(t, 1, accum.MergeCount)

		got, ok := accum.GetTSData().(nonOptsMergerTS)
		require.True(t, ok)
		gotDS, ok := got.Timeseries.(*dataset.DataSet)
		require.True(t, ok)
		require.Len(t, gotDS.Results[0].SeriesList[0].Points, 2)
	})

	t.Run("zero tolerance non-strategyMerger falls back to Merge", func(t *testing.T) {
		accum := NewAccumulator()
		seed := makeTestDataSet(0, "up", nil, []int64{100}, []string{"1"})
		accum.SetTSData(nonOptsMergerTS{Timeseries: seed})
		next := makeTestDataSet(0, "up", nil, []int64{200}, []string{"2"})

		handled, err := TimeseriesBatchMergeFuncWithStrategyTolerant(
			int(merge.StrategySum), 0)(accum, []BatchItem{{Data: next, Member: 0}})
		require.NoError(t, err)
		require.True(t, handled)
		require.Equal(t, 1, accum.MergeCount)
	})
}
