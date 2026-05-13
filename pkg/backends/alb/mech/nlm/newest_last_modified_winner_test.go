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

package nlm

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

func TestNLMFallbackPrefers2xxOver5xx(t *testing.T) {
	statusHandler := func(code int, body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(body))
		})
	}

	p, _, st := albpool.New(-1, []http.Handler{
		statusHandler(http.StatusInternalServerError, "body0"),
		statusHandler(http.StatusOK, "body1"),
	})
	st[0].Set(0)
	st[1].Set(0)
	time.Sleep(250 * time.Millisecond)

	h := &handler{}
	h.SetPool(p)
	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 got %d", w.Code)
	}
	if w.Body.String() != "body1" {
		t.Errorf("expected body 'body1' got %q", w.Body.String())
	}
}
