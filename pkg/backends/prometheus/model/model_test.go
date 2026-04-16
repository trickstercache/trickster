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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

const testMatrix = `{"status":"success","data":{"resultType":"matrix","result":[{` +
	`"metric":{"__name__":"up","instance":"localhost:9090","job":"prometheus"},"values"` +
	`:[[1435781430,"1"],[1435781445,"1"],[1435781460,"1"]]},{"metric":` +
	`{"__name__":"up","instance":"localhost:9091","job":"node"},"values":` +
	`[[1435781430,"0"],[1435781445,"0"],[1435781460,"1"]]}]}}`

func TestUnmarshalTimeseries(t *testing.T) {
	b := []byte(testMatrix)
	trq := &timeseries.TimeRangeQuery{}
	ts, err := UnmarshalTimeseries(b, trq)
	if err != nil {
		t.Error(err)
	}

	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		t.Error(timeseries.ErrUnknownFormat)
	}

	b, err = MarshalTimeseries(ds, nil, 200)
	if err != nil {
		t.Error(err)
	}

	if string(b) != testMatrix {
		t.Error("marsahing error")
	}
}

func TestMarshalTimeseries_EscapesTagValues(t *testing.T) {
	ds := &dataset.DataSet{
		Results: dataset.Results{{
			SeriesList: dataset.SeriesList{{
				Header: dataset.SeriesHeader{
					Name: "up",
					Tags: dataset.Tags{
						"__name__": "up",
						"path":     `/api/v1/query?q="a"`,
					},
				},
				Points: dataset.Points{{
					Epoch:  1435781430000000000,
					Size:   33,
					Values: []any{"1"},
				}},
			}},
		}},
	}
	b, err := MarshalTimeseries(ds, nil, 200)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("invalid JSON: %v (body=%s)", err, string(b))
	}
}

func TestUnmarshalInstantaneous(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{}
	bytes := []byte(`{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"__name__":"up","instance":"localhost:9090","job":"prometheus"},` +
		`"value":[1554730772.113,"1"]}]}}`)
	_, err := UnmarshalTimeseries(bytes, trq)
	if err != nil {
		t.Error(err)
		return
	}
}

func TestStartMarshal(t *testing.T) {
	t.Run("with error and errorType", func(t *testing.T) {
		w := httptest.NewRecorder()
		e := &Envelope{
			Status:    "error",
			Error:     "test error",
			ErrorType: "test type",
		}
		e.StartMarshal(w, 400)
		if w.Code != 400 {
			t.Errorf("expected %d got %d", 400, w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, `"error":"test error"`) {
			t.Errorf("expected error field in body: %s", body)
		}
		if !strings.Contains(body, `"errorType":"test type"`) {
			t.Errorf("expected errorType field in body: %s", body)
		}
	})

	t.Run("with warnings", func(t *testing.T) {
		w := httptest.NewRecorder()
		e := &Envelope{
			Status:   "success",
			Warnings: []string{"w1", "w2"},
		}
		e.StartMarshal(w, 200)
		body := w.Body.String()
		if !strings.Contains(body, `"warnings":["w1","w2"]`) {
			t.Errorf("expected warnings in body: %s", body)
		}
	})

	t.Run("nil writer no panic", func(t *testing.T) {
		e := &Envelope{Status: "success"}
		e.StartMarshal(nil, 200) // should not panic
	})

	t.Run("zero status defaults to 200", func(t *testing.T) {
		w := httptest.NewRecorder()
		e := &Envelope{Status: "success"}
		e.StartMarshal(w, 0)
		if w.Code != 200 {
			t.Errorf("expected 200 got %d", w.Code)
		}
	})
}

func TestStartMarshal_EscapesSpecialChars(t *testing.T) {
	w := httptest.NewRecorder()
	e := &Envelope{
		Status:    "error",
		Error:     `parse error: unexpected "}"`,
		ErrorType: `bad_data`,
		Warnings:  []string{`warning with "quotes"`, `back\slash`},
	}
	e.StartMarshal(w, 400)
	w.Write([]byte("}"))

	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v (body=%s)", err, w.Body.String())
	}
	if env["error"] != `parse error: unexpected "}"` {
		t.Errorf("error not escaped, got %q", env["error"])
	}
}

func TestEnvelopeMerge(t *testing.T) {
	tests := []struct {
		name           string
		e1Status       string
		e1Error        string
		e2Status       string
		e2Error        string
		e2Warnings     []string
		expectStatus   string
		expectError    string
		expectWarnings []string
	}{
		{
			name:           "success + success",
			e1Status:       "success",
			e2Status:       "success",
			expectStatus:   "success",
			expectError:    "",
			expectWarnings: nil,
		},
		{
			name:           "error + success promotes",
			e1Status:       "error",
			e1Error:        "err1",
			e2Status:       "success",
			expectStatus:   "success",
			expectError:    "",
			expectWarnings: []string{"err1"},
		},
		{
			name:           "success + error keeps success",
			e1Status:       "success",
			e2Status:       "error",
			e2Error:        "err2",
			expectStatus:   "success",
			expectError:    "",
			expectWarnings: []string{"err2"},
		},
		{
			name:           "both errors stays error",
			e1Status:       "error",
			e1Error:        "err1",
			e2Status:       "error",
			e2Error:        "err2",
			expectStatus:   "error",
			expectError:    "err1",
			expectWarnings: []string{"err2"},
		},
		{
			name:           "warnings accumulate",
			e1Status:       "success",
			e2Status:       "success",
			e2Warnings:     []string{"w1"},
			expectStatus:   "success",
			expectError:    "",
			expectWarnings: []string{"w1"},
		},
		{
			name:           "error with warnings",
			e1Status:       "success",
			e2Status:       "error",
			e2Error:        "err",
			e2Warnings:     []string{"w1"},
			expectStatus:   "success",
			expectError:    "",
			expectWarnings: []string{"err", "w1"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e1 := &Envelope{Status: test.e1Status, Error: test.e1Error}
			e2 := &Envelope{Status: test.e2Status, Error: test.e2Error, Warnings: test.e2Warnings}
			e1.Merge(e2)
			if e1.Status != test.expectStatus {
				t.Errorf("status: expected %q got %q", test.expectStatus, e1.Status)
			}
			if e1.Error != test.expectError {
				t.Errorf("error: expected %q got %q", test.expectError, e1.Error)
			}
			if len(e1.Warnings) != len(test.expectWarnings) {
				t.Fatalf("warnings count: expected %d got %d (%v)",
					len(test.expectWarnings), len(e1.Warnings), e1.Warnings)
			}
			for i, w := range test.expectWarnings {
				if e1.Warnings[i] != w {
					t.Errorf("warning[%d]: expected %q got %q", i, w, e1.Warnings[i])
				}
			}
		})
	}
}
