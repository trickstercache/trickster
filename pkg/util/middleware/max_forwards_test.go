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

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestMaxForwards(t *testing.T) {
	tests := []struct {
		name            string
		method          string
		value           string
		expectForwarded bool
		expectStatus    int
		expectFwdValue  string
	}{
		{"options zero answered here", http.MethodOptions, "0", false, http.StatusOK, ""},
		{"trace zero rejected here", http.MethodTrace, "0", false, http.StatusMethodNotAllowed, ""},
		{"options five is decremented", http.MethodOptions, "5", true, http.StatusTeapot, "4"},
		{"get passes through", http.MethodGet, "0", true, http.StatusTeapot, "0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var forwarded bool
			var fwdValue string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded = true
				fwdValue = r.Header.Get(headers.NameMaxForwards)
				w.WriteHeader(http.StatusTeapot)
			})
			r := httptest.NewRequest(test.method, "http://example.com/", nil)
			r.Header.Set(headers.NameMaxForwards, test.value)
			w := httptest.NewRecorder()
			MaxForwards(next).ServeHTTP(w, r)

			if forwarded != test.expectForwarded {
				t.Errorf("forwarded got %t expected %t", forwarded, test.expectForwarded)
			}
			if w.Code != test.expectStatus {
				t.Errorf("status got %d expected %d", w.Code, test.expectStatus)
			}
			if forwarded && fwdValue != test.expectFwdValue {
				t.Errorf("Max-Forwards got %q expected %q", fwdValue, test.expectFwdValue)
			}
			if !test.expectForwarded {
				if got := w.Header().Get(headers.NameAllow); got != AllowedMethods {
					t.Errorf("Allow got %q expected %q", got, AllowedMethods)
				}
				if w.Header().Get(headers.NameVia) == "" {
					t.Error("expected Via on a locally generated response")
				}
			}
		})
	}
}
