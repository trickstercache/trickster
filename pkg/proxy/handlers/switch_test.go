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

package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewSwitchHandler(t *testing.T) {
	router := http.NewServeMux()
	sh := NewSwitchHandler(router)
	if sh == nil {
		t.Error("expected non-nill handler")
	}
}

func TestServeHTTP(t *testing.T) {
	router := http.NewServeMux()
	sh := NewSwitchHandler(router)
	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "/", nil)
	sh.setReloading(true)
	sh.ServeHTTP(w, r)
	sh.setReloading(false)
	sh.ServeHTTP(w, r)
}

func TestUpdate(t *testing.T) {
	router := http.NewServeMux()
	sh := NewSwitchHandler(router)
	sh.Update(router)
}

func TestHandler(t *testing.T) {

	router := http.NewServeMux()
	sh := NewSwitchHandler(router)

	x := sh.Handler()
	if x != router {
		t.Error("router mismatch")
	}

	sh.reloading.Store(1)
	x = sh.Handler()
	if x != router {
		t.Error("router mismatch")
	}
}
