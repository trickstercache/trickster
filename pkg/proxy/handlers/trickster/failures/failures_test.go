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

package failures

import (
	"net/http/httptest"
	"testing"
)

func TestHandleBadRequestResponse(t *testing.T) {
	HandleBadRequestResponse(nil, nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/", nil)
	HandleBadRequestResponse(w, r)
	if w.Result().StatusCode != 400 {
		t.Errorf("expected %d got %d", 400, w.Result().StatusCode)
	}
}

func TestHandleInternalServerError(t *testing.T) {
	HandleInternalServerError(nil, nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/", nil)
	HandleInternalServerError(w, r)
	if w.Result().StatusCode != 500 {
		t.Errorf("expected %d got %d", 500, w.Result().StatusCode)
	}
}

func TestHandleBadGateway(t *testing.T) {
	HandleBadGateway(nil, nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/trickster/", nil)
	HandleBadGateway(w, r)
	if w.Result().StatusCode != 502 {
		t.Errorf("expected %d got %d", 502, w.Result().StatusCode)
	}
}
