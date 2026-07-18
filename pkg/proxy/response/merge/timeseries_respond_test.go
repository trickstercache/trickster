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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/stretchr/testify/require"
)

func TestTimeseriesMergeFuncErrors(t *testing.T) {
	t.Parallel()

	accum := NewAccumulator()
	mf := TimeseriesMergeFunc(func([]byte, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		return nil, errors.New("unmarshal failed")
	})
	err := mf(accum, []byte("bad"), 0)
	require.Error(t, err)

	err = mf(accum, 42, 0)
	require.ErrorContains(t, err, "unexpected data type")
}

func TestTimeseriesMergeFuncFromBytes(t *testing.T) {
	t.Parallel()

	ds := makeTestDataSet(0, "up", nil, []int64{100}, []string{"1"})
	accum := NewAccumulator()
	fromBytes := TimeseriesMergeFuncFromBytes(func([]byte, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		return ds, nil
	})
	require.NoError(t, fromBytes(accum, []byte("payload"), 0))
	require.NotNil(t, accum.GetTSData())
}

func TestTimeseriesRespondFunc(t *testing.T) {
	t.Parallel()

	t.Run("nil data returns bad gateway", func(t *testing.T) {
		rf := TimeseriesRespondFunc(func(timeseries.Timeseries, *timeseries.RequestOptions, int, io.Writer) error {
			return nil
		}, nil)
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		rf(w, r, NewAccumulator(), 0)
		require.Equal(t, http.StatusBadGateway, w.Code)
	})

	t.Run("writes merged data", func(t *testing.T) {
		accum := NewAccumulator()
		ds := makeTestDataSet(0, "up", nil, []int64{100}, []string{"1"})
		require.NoError(t, TimeseriesMergeFunc(nil)(accum, ds, 0))

		var called bool
		rf := TimeseriesRespondFunc(func(_ timeseries.Timeseries, _ *timeseries.RequestOptions, status int, w io.Writer) error {
			called = true
			require.Equal(t, http.StatusOK, status)
			_, err := w.Write([]byte("ok"))
			return err
		}, nil)
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		rf(w, r, accum, 0)
		require.True(t, called)
		require.Equal(t, http.StatusOK, w.Code)
	})
}

func TestTimeseriesRespondFuncWithStrategy(t *testing.T) {
	t.Parallel()

	accum := NewAccumulator()
	ds1 := makeTestDataSet(0, "latency", nil, []int64{100}, []string{"10"})
	ds2 := makeTestDataSet(0, "latency", nil, []int64{100}, []string{"30"})
	mf := TimeseriesMergeFuncWithStrategy(nil, int(merge.StrategyAvg))
	require.NoError(t, mf(accum, ds1, 0))
	require.NoError(t, mf(accum, ds2, 1))

	var finalized string
	rf := TimeseriesRespondFuncWithStrategy(
		func(ts timeseries.Timeseries, _ *timeseries.RequestOptions, _ int, _ io.Writer) error {
			ds := ts.(*dataset.DataSet)
			finalized = ds.Results[0].SeriesList[0].Points[0].Values[0].(string)
			return nil
		},
		nil,
		int(merge.StrategyAvg),
	)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	rf(w, r, accum, 0)
	require.Equal(t, "20", finalized)
}
