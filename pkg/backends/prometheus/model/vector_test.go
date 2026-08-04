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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const testVector = `{"status":"success","data":{"resultType":"vector","result":[` +
	`{"metric":{"__name__":"go_memstats_alloc_bytes","instance":` +
	`"host.docker.internal:8481","job":"trickster"},` +
	`"value":[1577836800,"1"]}]}}`

const testVector2 = `{"status":"success","data":{"resultType":"vector","result":[` +
	`{"metric":{"__name__":"go_memstats_alloc_bytes","instance":` +
	`"trickstercache.org:8481","job":"trickster"},` +
	`"value":[1577836800,"1"]}]}}`

func TestMergeAndWriteVector(t *testing.T) {
	unmarshaler := func(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		if trq == nil {
			trq = &timeseries.TimeRangeQuery{}
		}
		return UnmarshalTimeseries(data, trq)
	}
	mergeFunc := MergeAndWriteVectorMergeFunc(unmarshaler)
	respondFunc := MergeAndWriteVectorRespondFunc(MarshalTimeseriesWriter)

	t.Run("nil accumulator responds bad gateway", func(t *testing.T) {
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)
		accum := merge.NewAccumulator()
		respondFunc(w, r, accum, http.StatusOK)
		if w.Code != http.StatusBadGateway {
			t.Errorf("expected %d got %d", http.StatusBadGateway, w.Code)
		}
	})

	t.Run("valid merge of two vectors", func(t *testing.T) {
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)
		accum := merge.NewAccumulator()
		if err := mergeFunc(accum, []byte(testVector), 0); err != nil {
			t.Fatalf("unexpected error merging first vector: %v", err)
		}
		_ = mergeFunc(accum, []byte(`{"stat`), 1) // bad JSON, skipped
		if err := mergeFunc(accum, []byte(testVector2), 2); err != nil {
			t.Fatalf("unexpected error merging second vector: %v", err)
		}
		respondFunc(w, r, accum, http.StatusOK)
		if w.Code != http.StatusOK {
			t.Errorf("expected %d got %d", http.StatusOK, w.Code)
		}
	})

	t.Run("error envelope", func(t *testing.T) {
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)
		accum := merge.NewAccumulator()
		if err := mergeFunc(accum, []byte(`{"status":"error","data":{}}`), 0); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		respondFunc(w, r, accum, http.StatusOK)
		if w.Code != http.StatusOK {
			t.Errorf("expected %d got %d", http.StatusOK, w.Code)
		}
	})
}

func TestMergeAndWriteScalarPrefersFirstNonNaN(t *testing.T) {
	unmarshaler := func(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		if trq == nil {
			trq = &timeseries.TimeRangeQuery{}
		}
		return UnmarshalTimeseries(data, trq)
	}
	decode := func(body string) timeseries.Timeseries {
		t.Helper()
		ts, err := unmarshaler([]byte(body), nil)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	items := []merge.BatchItem{
		{Member: 0, Data: decode(`{"status":"success","data":{"resultType":"scalar","result":[100,"NaN"]}}`)},
		{Member: 1, Data: decode(`{"status":"success","data":{"resultType":"scalar","result":[101,"42"]}}`)},
		{Member: 2, Data: decode(`{"status":"success","data":{"resultType":"scalar","result":[102,"99"]}}`)},
	}
	accum := merge.NewAccumulator()
	handled, err := MergeAndWriteVectorBatchMergeFunc()(accum, items)
	if err != nil || !handled {
		t.Fatalf("scalar batch merge: handled=%v err=%v", handled, err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/query", nil)
	MergeAndWriteVectorRespondFunc(MarshalTimeseriesWriter)(w, r, accum, http.StatusOK)
	body, err := io.ReadAll(w.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	const expected = `{"status":"success","data":{"resultType":"scalar","result":[101,"42"]}}`
	if string(body) != expected {
		t.Fatalf("body: got %s want %s", body, expected)
	}
}

func TestMergeAndWriteScalarIgnoresErrorEnvelope(t *testing.T) {
	decode := func(body string) timeseries.Timeseries {
		t.Helper()
		ts, err := UnmarshalTimeseries([]byte(body), &timeseries.TimeRangeQuery{})
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	for _, items := range [][]merge.BatchItem{
		{
			{Member: 0, Data: decode(`{"status":"error","errorType":"bad_data","error":"boom"}`)},
			{Member: 1, Data: decode(`{"status":"success","data":{"resultType":"scalar","result":[101,"42"]}}`)},
		},
		{
			{Member: 0, Data: decode(`{"status":"success","data":{"resultType":"scalar","result":[101,"42"]}}`)},
			{Member: 1, Data: decode(`{"status":"error","errorType":"bad_data","error":"boom"}`)},
		},
	} {
		accum := merge.NewAccumulator()
		handled, err := MergeAndWriteVectorBatchMergeFunc()(accum, items)
		if err != nil || !handled {
			t.Fatalf("scalar batch merge: handled=%v err=%v", handled, err)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/query", nil)
		MergeAndWriteVectorRespondFunc(MarshalTimeseriesWriter)(w, r, accum, http.StatusOK)
		const want = `{"status":"success","data":{"resultType":"scalar","result":[101,"42"]}}`
		if got := w.Body.String(); got != want {
			t.Fatalf("body: got %s want %s", got, want)
		}
	}
}
