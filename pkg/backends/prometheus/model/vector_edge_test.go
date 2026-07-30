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

package model

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

type stubTimeseries struct {
	timeseries.Timeseries
}

func (s stubTimeseries) Clone() timeseries.Timeseries { return s }
func (s stubTimeseries) Merge(bool, ...timeseries.Timeseries) {}
func (s stubTimeseries) Sort()                             {}
func (s stubTimeseries) SetExtents(timeseries.ExtentList)  {}
func (s stubTimeseries) Extents() timeseries.ExtentList    { return nil }
func (s stubTimeseries) VolatileExtents() timeseries.ExtentList {
	return nil
}
func (s stubTimeseries) SetVolatileExtents(timeseries.ExtentList) {}
func (s stubTimeseries) TimestampCount() int64                    { return 0 }
func (s stubTimeseries) Step() time.Duration                      { return 0 }
func (s stubTimeseries) CroppedClone(timeseries.Extent) timeseries.Timeseries {
	return s
}
func (s stubTimeseries) CropToRange(timeseries.Extent)                {}
func (s stubTimeseries) CropToSize(int, time.Time, timeseries.Extent) {}
func (s stubTimeseries) SeriesCount() int                             { return 0 }
func (s stubTimeseries) ValueCount() int64                            { return 0 }
func (s stubTimeseries) Size() int64                                  { return 0 }
func (s stubTimeseries) SetTimeRangeQuery(*timeseries.TimeRangeQuery) {}

func testUnmarshaler(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		trq = &timeseries.TimeRangeQuery{}
	}
	return UnmarshalTimeseries(data, trq)
}

func scalarBody(ts int64, value string) string {
	return `{"status":"success","data":{"resultType":"scalar","result":[` +
		jsonNumber(ts) + `,"` + value + `"]}}`
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func decodeTS(t *testing.T, body string) *dataset.DataSet {
	t.Helper()
	ts, err := testUnmarshaler([]byte(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		t.Fatalf("expected *dataset.DataSet, got %T", ts)
	}
	return ds
}

func TestMergeAndWriteVectorMergeFuncScalarPaths(t *testing.T) {
	mergeFunc := MergeAndWriteVectorMergeFunc(testUnmarshaler)

	t.Run("unknown_data_type", func(t *testing.T) {
		accum := merge.NewAccumulator()
		if err := mergeFunc(accum, 42, 0); err != timeseries.ErrUnknownFormat {
			t.Fatalf("got %v want ErrUnknownFormat", err)
		}
	})

	t.Run("passthrough_timeseries", func(t *testing.T) {
		accum := merge.NewAccumulator()
		ds := decodeTS(t, scalarBody(100, "7"))
		if err := mergeFunc(accum, ds, 0); err != nil {
			t.Fatal(err)
		}
		got, ok := accum.GetTSData().(*dataset.DataSet)
		if !ok || !hasNonNaNScalar(got) {
			t.Fatalf("unexpected accumulator data: %#v", accum.GetTSData())
		}
	})

	t.Run("non_dataset_timeseries_falls_back", func(t *testing.T) {
		accum := merge.NewAccumulator()
		if err := mergeFunc(accum, stubTimeseries{}, 0); err != nil {
			t.Fatal(err)
		}
		if _, ok := accum.GetTSData().(stubTimeseries); !ok {
			t.Fatalf("expected stub timeseries in accumulator, got %T", accum.GetTSData())
		}
	})

	t.Run("error_ignored_when_scalar_present", func(t *testing.T) {
		accum := merge.NewAccumulator()
		if err := mergeFunc(accum, []byte(scalarBody(100, "7")), 0); err != nil {
			t.Fatal(err)
		}
		if err := mergeFunc(accum, []byte(`{"status":"error","errorType":"bad_data","error":"boom"}`), 1); err != nil {
			t.Fatal(err)
		}
		got := accum.GetTSData().(*dataset.DataSet)
		if got.SourceResultType != string(Scalar) || !hasNonNaNScalar(got) {
			t.Fatalf("scalar should remain after error envelope: %#v", got)
		}
	})

	t.Run("scalar_replaces_error", func(t *testing.T) {
		accum := merge.NewAccumulator()
		if err := mergeFunc(accum, []byte(`{"status":"error","errorType":"bad_data","error":"boom"}`), 0); err != nil {
			t.Fatal(err)
		}
		if err := mergeFunc(accum, []byte(scalarBody(101, "42")), 1); err != nil {
			t.Fatal(err)
		}
		got := accum.GetTSData().(*dataset.DataSet)
		if got.SourceResultType != string(Scalar) || !hasNonNaNScalar(got) {
			t.Fatalf("scalar should replace error envelope: %#v", got)
		}
	})

	t.Run("scalar_keeps_existing_vector", func(t *testing.T) {
		accum := merge.NewAccumulator()
		if err := mergeFunc(accum, []byte(testVector), 0); err != nil {
			t.Fatal(err)
		}
		if err := mergeFunc(accum, []byte(scalarBody(101, "42")), 1); err != nil {
			t.Fatal(err)
		}
		got := accum.GetTSData().(*dataset.DataSet)
		if got.SourceResultType != string(Vector) {
			t.Fatalf("vector should win over later scalar: %#v", got.SourceResultType)
		}
	})

	t.Run("scalar_prefers_first_non_nan", func(t *testing.T) {
		accum := merge.NewAccumulator()
		if err := mergeFunc(accum, []byte(scalarBody(100, "NaN")), 0); err != nil {
			t.Fatal(err)
		}
		if err := mergeFunc(accum, []byte(scalarBody(101, "42")), 1); err != nil {
			t.Fatal(err)
		}
		if err := mergeFunc(accum, []byte(scalarBody(102, "99")), 2); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		MergeAndWriteVectorRespondFunc(MarshalTimeseriesWriter)(w, r, accum, 0)
		want := `{"status":"success","data":{"resultType":"scalar","result":[101,"42"]}}`
		if got := w.Body.String(); got != want {
			t.Fatalf("body: got %s want %s", got, want)
		}
	})

	t.Run("keeps_current_non_dataset", func(t *testing.T) {
		accum := merge.NewAccumulator()
		accum.SetTSData(stubTimeseries{})
		if err := mergeFunc(accum, []byte(scalarBody(101, "42")), 0); err != nil {
			t.Fatal(err)
		}
		if _, ok := accum.GetTSData().(stubTimeseries); !ok {
			t.Fatalf("expected non-dataset current to remain, got %T", accum.GetTSData())
		}
	})
}

func TestMergeAndWriteVectorBatchMergeFuncEdgeCases(t *testing.T) {
	batch := MergeAndWriteVectorBatchMergeFunc()

	t.Run("empty", func(t *testing.T) {
		handled, err := batch(merge.NewAccumulator(), nil)
		if err != nil || handled {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
	})

	t.Run("non_dataset_falls_back", func(t *testing.T) {
		accum := merge.NewAccumulator()
		handled, err := batch(accum, []merge.BatchItem{{Data: stubTimeseries{}}})
		if err != nil {
			t.Fatal(err)
		}
		if !handled {
			t.Fatal("expected standard batch merge to handle stub timeseries")
		}
	})

	t.Run("vector_falls_back", func(t *testing.T) {
		accum := merge.NewAccumulator()
		handled, err := batch(accum, []merge.BatchItem{
			{Data: decodeTS(t, testVector)},
		})
		if err != nil || !handled {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
	})

	t.Run("errors_only_falls_back", func(t *testing.T) {
		accum := merge.NewAccumulator()
		handled, err := batch(accum, []merge.BatchItem{
			{Data: decodeTS(t, `{"status":"error","errorType":"bad_data","error":"boom"}`)},
		})
		if err != nil || !handled {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
	})

	t.Run("existing_vector_falls_back", func(t *testing.T) {
		accum := merge.NewAccumulator()
		accum.SetTSData(decodeTS(t, testVector))
		handled, err := batch(accum, []merge.BatchItem{
			{Data: decodeTS(t, scalarBody(101, "42"))},
		})
		if err != nil || !handled {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
		if accum.GetTSData().(*dataset.DataSet).SourceResultType != string(Vector) {
			t.Fatal("existing vector should remain when batch falls back")
		}
	})

	t.Run("existing_error_replaced_by_scalars", func(t *testing.T) {
		accum := merge.NewAccumulator()
		accum.SetTSData(decodeTS(t, `{"status":"error","errorType":"bad_data","error":"boom"}`))
		handled, err := batch(accum, []merge.BatchItem{
			{Data: decodeTS(t, scalarBody(100, "NaN"))},
			{Data: decodeTS(t, scalarBody(101, "42"))},
		})
		if err != nil || !handled {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
		got := accum.GetTSData().(*dataset.DataSet)
		if got.SourceResultType != string(Scalar) || !hasNonNaNScalar(got) {
			t.Fatalf("expected first non-NaN scalar, got %#v", got)
		}
	})
}

func TestHasNonNaNScalarEdgeCases(t *testing.T) {
	if hasNonNaNScalar(&dataset.DataSet{
		Results: dataset.Results{nil, {
			SeriesList: dataset.SeriesList{nil, {
				Points: dataset.Points{
					{Values: nil},
					{Values: []any{1.5}},
					{Values: []any{"not-a-float"}},
					{Values: []any{"NaN"}},
				},
			}},
		}},
	}) {
		t.Fatal("expected no non-NaN scalar")
	}
	if !hasNonNaNScalar(&dataset.DataSet{
		Results: dataset.Results{{
			SeriesList: dataset.SeriesList{{
				Points: dataset.Points{{Values: []any{"3.14"}}},
			}},
		}},
	}) {
		t.Fatal("expected non-NaN scalar")
	}
}
