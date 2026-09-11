/*
 * Copyright 2026 The Trickster Authors
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

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
)

func TestMarkServed(t *testing.T) {
	var served bool
	h := MarkServed(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		served = tctx.IsServed(r.Context())
	}))
	r := httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !served {
		t.Error("expected the request to be marked as served")
	}
	if tctx.IsServed(r.Context()) {
		t.Error("the source request context should be left unmodified")
	}
}
