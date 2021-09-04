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

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/strings"
)

func emptyHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("trickster"))
}

func TestHandleCompression(t *testing.T) {
	f := HandleCompression(http.HandlerFunc(emptyHandler), strings.Lookup{"text/plain": nil})
	r, _ := http.NewRequest(http.MethodGet, "http://trickstercache.org/", nil)
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	if w.Body.String() != "trickster" {
		t.Error("writer data mismatch")
	}
	r.Header.Add(headers.NameCacheControl, "no-transform")
	w = httptest.NewRecorder()
	f.ServeHTTP(w, r)
	if w.Body.String() != "trickster" {
		t.Error("writer data mismatch")
	}

}
