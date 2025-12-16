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
)

func TestMergeLabelData(t *testing.T) {
	ld1 := &WFLabelData{
		Envelope: &Envelope{
			Status: "error",
			Error:  "test error",
		},
		Data: []string{"test", "trickster"},
	}
	ld2 := &WFLabelData{
		Envelope: &Envelope{
			Status: "success",
		},
		Data: []string{"test2", "trickster2"},
	}
	ld3 := &WFLabelData{
		Envelope: &Envelope{
			Status: "error",
			Error:  "test error",
		},
		Data: []string{"test3", "trickster3"}, // should not be appended due to error
	}
	ld4 := &WFLabelData{
		Envelope: &Envelope{
			Status:   "success",
			Warnings: []string{"test warning 1"},
		},
		Data: []string{"test3", "trickster3"},
	}
	ld1.Merge(ld2)
	ld1.Merge(ld3)
	ld1.Merge(ld4)
	if len(ld1.Data) != 6 {
		t.Errorf("expected %d got %d", 6, len(ld1.Data))
	}

	if ld1.Envelope.Status != "success" {
		t.Errorf("expected %s got %s", "success", ld1.Envelope.Status)
	}

	if len(ld1.Envelope.Warnings) != 3 {
		t.Errorf("expected %d got %d", 3, len(ld1.Envelope.Warnings))
	}
}

func TestMergeAndWriteLabelData(t *testing.T) {
	tests := []struct {
		name    string
		bodies  [][]byte
		expCode int
	}{
		{
			name:    "nil bodies",
			bodies:  nil,
			expCode: http.StatusOK,
		},
		{
			name:    "empty bodies",
			bodies:  [][]byte{},
			expCode: http.StatusOK,
		},
		{
			name: "valid merge",
			bodies: [][]byte{
				[]byte(`{"status":"success","data":["test", "trickster"]}`),
				[]byte(`{"stat`),
				[]byte(`{"status":"success","data":["test2", "trickster2"]}`),
			},
			expCode: http.StatusOK,
		},
		{
			name: "error status",
			bodies: [][]byte{
				[]byte(`{"status":"error"`),
				[]byte(`{"status":"error","data":["should", "not", "append"]`),
			},
			expCode: http.StatusOK,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "/", nil)

			accum := merge.NewAccumulator()
			mergeFunc := MergeAndWriteLabelDataMergeFunc()
			respondFunc := MergeAndWriteLabelDataRespondFunc()
			for i, body := range test.bodies {
				_ = mergeFunc(accum, body, i)
			}
			respondFunc(w, r, accum, test.expCode)

			if w.Code != test.expCode {
				t.Errorf("expected %d got %d", test.expCode, w.Code)
			}
		})
	}
}
