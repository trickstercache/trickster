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

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

func TestNLMAcceptsAllRFC7231DateFormats(t *testing.T) {
	cases := []struct {
		name   string
		newest string
		older  string
	}{
		{"IMF-fixdate GMT", "Sun, 06 Nov 1994 08:49:37 GMT", "Sun, 06 Nov 1994 08:00:00 GMT"},
		{"RFC 850", "Sunday, 06-Nov-94 08:49:37 GMT", "Sunday, 06-Nov-94 08:00:00 GMT"},
		{"ANSI C asctime", "Sun Nov  6 08:49:37 1994", "Sun Nov  6 08:00:00 1994"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mk := func(lm, body string) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set(headers.NameLastModified, lm)
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(body))
				})
			}
			p, _, st := albpool.New(-1, []http.Handler{
				mk(tc.older, "older"),
				mk(tc.newest, "newest"),
			})
			st[0].Set(0)
			st[1].Set(0)
			time.Sleep(250 * time.Millisecond)

			h := &handler{}
			h.SetPool(p)
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
			h.ServeHTTP(w, r)

			if w.Body.String() != "newest" {
				t.Errorf("%s: expected newest body, got %q", tc.name, w.Body.String())
			}
		})
	}
}

func TestNLMTieBreakPrefersFirstSeen(t *testing.T) {
	lm := "Sun, 06 Nov 1994 08:49:37 GMT"
	mk := func(body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(headers.NameLastModified, lm)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(body))
		})
	}
	p, _, st := albpool.New(-1, []http.Handler{mk("first"), mk("second")})
	st[0].Set(0)
	st[1].Set(0)
	time.Sleep(250 * time.Millisecond)

	h := &handler{}
	h.SetPool(p)
	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
	h.ServeHTTP(w, r)

	if w.Body.String() != "first" {
		t.Errorf("tie should resolve to first-seen, got %q", w.Body.String())
	}
}
