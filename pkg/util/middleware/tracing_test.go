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

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestTraceAddsResourceAttributesToRequestSpan(t *testing.T) {
	tr, sr := tu.NewRecordingTracer(t)
	rsc := request.NewResources(
		&bo.Options{Name: "origin-a", Provider: "prometheus"},
		&po.Options{Path: "/api/v1/query", HandlerName: "proxycache"},
		&co.Options{Name: "memory-cache", Provider: "memory"},
		nil,
		nil,
		tr,
	)

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := Trace(tr, next)

	r := httptest.NewRequest(http.MethodGet, "http://example.com/api/v1/query?query=up", nil)
	r = r.WithContext(tc.WithResources(r.Context(), rsc))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	tu.RequireSpanAttributes(t, sr, "request", map[string]string{
		"backend.name":     "origin-a",
		"backend.provider": "prometheus",
		"cache.name":       "memory-cache",
		"cache.provider":   "memory",
		"router.path":      "/api/v1/query",
		"router.handler":   "proxycache",
	})
}
