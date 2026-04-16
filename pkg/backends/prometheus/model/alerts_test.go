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

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
)

var testResources = request.NewResources(nil, nil, nil, nil, nil, nil)

func TestCalculateHash(t *testing.T) {
	a := &WFAlert{
		State: "test",
	}

	i := a.CalculateHash()
	var expected uint64 = 9358847448315454117

	if i != expected {
		t.Errorf("expected %d got %d", expected, i)
	}
}

func TestCalculateHash_AmbiguousSeparators(t *testing.T) {
	a1 := &WFAlert{
		Labels: map[string]string{"a=b": "c"},
	}
	a2 := &WFAlert{
		Labels: map[string]string{"a": "b=c"},
	}
	if a1.CalculateHash() == a2.CalculateHash() {
		t.Fatal("different label sets must not collide — separator in key/value is ambiguous")
	}
}

func TestMerge(t *testing.T) {
	a1 := &WFAlerts{
		Envelope: &Envelope{
			Status: "error",
		},
		Data: &WFAlertData{
			Alerts: []WFAlert{
				{
					State:  "test",
					Labels: map[string]string{"test": "trickster"},
				},
			},
		},
	}
	a2 := &WFAlerts{
		Envelope: &Envelope{
			Status: "success",
		},
		Data: &WFAlertData{
			Alerts: []WFAlert{
				{
					State:  "test2",
					Labels: map[string]string{"test2": "trickster"},
				},
			},
		},
	}
	a1.Merge(a2)
	if len(a1.Data.Alerts) != 2 {
		t.Errorf("expected %d got %d", 2, len(a1.Data.Alerts))
	}

	if a1.Envelope.Status != "success" {
		t.Errorf("expected %s got %s", "success", a1.Envelope.Status)
	}
}

func newTestReq() *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	return r
}

func TestMergeAndWriteAlerts(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	tests := []struct {
		name     string
		bodies   [][]byte
		expCode  int
		hasError bool
	}{
		{
			name:     "nil bodies",
			bodies:   nil,
			expCode:  http.StatusOK,
			hasError: false,
		},
		{
			name:     "empty bodies",
			bodies:   [][]byte{},
			expCode:  http.StatusOK,
			hasError: false,
		},
		{
			name: "valid merge",
			bodies: [][]byte{
				[]byte(`{"status":"success","data":{"alerts":[{"state":"test","labels":{},"annotations":{},"value":"x","activeAt":"y"}]}}`),
				[]byte(`{"stat`),
				[]byte(`{"status":"success","data":{"alerts":[]}}`),
			},
			expCode:  http.StatusOK,
			hasError: false,
		},
		{
			name: "error status",
			bodies: [][]byte{
				[]byte(`{"status":"error","data":{"alerts":[]}}`),
				[]byte(`{"status":"error","data":{"alerts":[]}}`),
			},
			expCode:  http.StatusBadRequest,
			hasError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := request.SetResources(newTestReq(), testResources)

			accum := merge.NewAccumulator()
			mergeFunc := MergeAndWriteAlertsMergeFunc()
			respondFunc := MergeAndWriteAlertsRespondFunc()
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

func TestMergeAndWriteAlerts_MultipleAlertsParseable(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	bodies := [][]byte{
		[]byte(`{"status":"success","data":{"alerts":[` +
			`{"state":"firing","labels":{"alertname":"A"},"annotations":{"summary":"a"}}` +
			`]}}`),
		[]byte(`{"status":"success","data":{"alerts":[` +
			`{"state":"firing","labels":{"alertname":"B"},"annotations":{"summary":"b"}}` +
			`]}}`),
	}

	w := httptest.NewRecorder()
	r := request.SetResources(newTestReq(), testResources)
	accum := merge.NewAccumulator()
	mergeFunc := MergeAndWriteAlertsMergeFunc()
	respondFunc := MergeAndWriteAlertsRespondFunc()
	for i, b := range bodies {
		if err := mergeFunc(accum, b, i); err != nil {
			t.Fatalf("merge %d: %v", i, err)
		}
	}
	respondFunc(w, r, accum, http.StatusOK)

	var env struct {
		Status string `json:"status"`
		Data   struct {
			Alerts []map[string]any `json:"alerts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("merged body is invalid JSON: %v (body=%s)", err, w.Body.String())
	}
	if len(env.Data.Alerts) != 2 {
		t.Fatalf("want 2 merged alerts, got %d: %s", len(env.Data.Alerts), w.Body.String())
	}
}

func TestMergeAlertStatePriority(t *testing.T) {
	// All cases use same Labels+Annotations so they share the same hash
	labels := map[string]string{"alertname": "TestAlert", "job": "test"}
	annotations := map[string]string{"summary": "test alert"}

	tests := []struct {
		name          string
		existingState string
		incomingState string
		expectedState string
	}{
		{"firing beats inactive", "inactive", "firing", "firing"},
		{"firing beats pending", "pending", "firing", "firing"},
		{"pending beats inactive", "inactive", "pending", "pending"},
		{"firing not downgraded", "firing", "inactive", "firing"},
		{"same state no change", "firing", "firing", "firing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a1 := &WFAlerts{
				Envelope: &Envelope{Status: "success"},
				Data: &WFAlertData{
					Alerts: []WFAlert{
						{State: test.existingState, Labels: labels, Annotations: annotations},
					},
				},
			}
			a2 := &WFAlerts{
				Envelope: &Envelope{Status: "success"},
				Data: &WFAlertData{
					Alerts: []WFAlert{
						{State: test.incomingState, Labels: labels, Annotations: annotations},
					},
				},
			}
			a1.Merge(a2)
			if len(a1.Data.Alerts) != 1 {
				t.Fatalf("expected 1 alert after dedup, got %d", len(a1.Data.Alerts))
			}
			if a1.Data.Alerts[0].State != test.expectedState {
				t.Errorf("expected state %q got %q", test.expectedState, a1.Data.Alerts[0].State)
			}
		})
	}
}
