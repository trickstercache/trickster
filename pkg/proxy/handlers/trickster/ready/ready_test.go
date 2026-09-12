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

package ready

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubListeners bool

func (s stubListeners) Serving() bool { return bool(s) }

func TestHandlerFunc(t *testing.T) {
	tests := []struct {
		name      string
		draining  bool
		pending   bool
		listeners Listeners
		status    int
		body      string
	}{
		{name: "ready", listeners: stubListeners(true), status: http.StatusOK, body: BodyReady},
		{name: "listener not ready", listeners: stubListeners(false), status: http.StatusServiceUnavailable, body: BodyNotReady},
		{name: "no listeners", status: http.StatusServiceUnavailable, body: BodyNotReady},
		{name: "draining", draining: true, listeners: stubListeners(true), status: http.StatusServiceUnavailable, body: BodyDraining},
		{name: "pending", pending: true, listeners: stubListeners(true), status: http.StatusServiceUnavailable, body: BodyNotProgrammed},
		{name: "pending outranked by a listener", pending: true, listeners: stubListeners(false), status: http.StatusServiceUnavailable, body: BodyNotReady},
		{name: "pending outranked by draining", pending: true, draining: true, listeners: stubListeners(true), status: http.StatusServiceUnavailable, body: BodyDraining},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &State{}
			if test.draining {
				state.SetDraining()
			}
			if test.pending {
				state.SetPending()
			}
			w := httptest.NewRecorder()
			HandlerFunc(state, test.listeners)(w, httptest.NewRequest(http.MethodGet, "http://0/trickster/ready", nil))
			if w.Code != test.status || w.Body.String() != test.body {
				t.Errorf("response = %d %q; want %d %q", w.Code, w.Body.String(), test.status, test.body)
			}
		})
	}
}

func TestSetProgrammedClearsPending(t *testing.T) {
	state := &State{}
	state.SetPending()
	if !state.Pending() {
		t.Fatal("SetPending did not take")
	}
	state.SetProgrammed()
	if state.Pending() {
		t.Error("SetProgrammed did not clear the wait")
	}
	w := httptest.NewRecorder()
	HandlerFunc(state, stubListeners(true))(w, httptest.NewRequest(http.MethodGet, "http://0/", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200 once programmed", w.Code)
	}
}

func TestStateNilSafety(t *testing.T) {
	var state *State
	state.SetDraining()
	state.SetPending()
	state.SetProgrammed()
	if state.Draining() || state.Pending() {
		t.Error("nil state must report neither draining nor pending")
	}
	w := httptest.NewRecorder()
	HandlerFunc(state, stubListeners(true))(w, httptest.NewRequest(http.MethodGet, "http://0/", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200 with a nil state", w.Code)
	}
}
