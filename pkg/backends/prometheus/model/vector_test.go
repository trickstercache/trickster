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
