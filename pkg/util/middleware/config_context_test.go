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
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	corso "github.com/trickstercache/trickster/v2/pkg/proxy/cors/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func TestWithResourcesContextAppliesLegacyCORS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameAllowOrigin, "https://origin.example.com")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.WriteHeader(http.StatusNoContent)
	})
	h := WithResourcesContext(nil, bo.New(), nil, po.New(), nil, next)
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	result := recorder.Result()
	if got := result.Header.Get(headers.NameAllowOrigin); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := result.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want true", got)
	}
}

func TestWithResourcesContextPathCORSOverridesBackend(t *testing.T) {
	backend := bo.New()
	backend.CORS = &corso.Options{Mode: corso.ModePreserve}
	path := po.New()
	path.CORS = &corso.Options{Mode: corso.ModeReplace}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameAllowOrigin, "https://origin.example.com")
		w.WriteHeader(http.StatusNoContent)
	})
	h := WithResourcesContext(nil, backend, nil, path, nil, next)
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := recorder.Result().Header.Get(headers.NameAllowOrigin); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}

func TestWithResourcesContextFinalizesCORSWithoutWrite(t *testing.T) {
	backend := bo.New()
	backend.CORS = &corso.Options{Mode: corso.ModeReplace}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameAllowOrigin, "https://origin.example.com")
	})
	h := WithResourcesContext(nil, backend, nil, po.New(), nil, next)
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := recorder.Result().Header.Get(headers.NameAllowOrigin); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}

func TestWithResourcesContextKeepsClientFacingCORSAcrossNestedRoute(t *testing.T) {
	outer := bo.New()
	outer.CORS = &corso.Options{Mode: corso.ModeReplace, Headers: types.EnvStringMap{
		headers.NameAllowOrigin: "https://public.example.com",
	}}
	inner := bo.New()
	inner.CORS = &corso.Options{Mode: corso.ModeReplace, Headers: types.EnvStringMap{
		headers.NameAllowOrigin: "https://member.example.com",
	}}

	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.GetResources(r)
		if rsc == nil {
			t.Fatal("missing request resources")
		}
		if rsc.BackendOptions != inner {
			t.Error("nested route must still update active backend resources")
		}
		if rsc.FrontendCORS != outer.CORS {
			t.Error("nested route replaced the client-facing CORS policy")
		}
		w.Header().Set(headers.NameAllowOrigin, "https://origin.example.com")
		w.WriteHeader(http.StatusNoContent)
	})
	innerHandler := WithResourcesContext(nil, inner, nil, po.New(), nil, final)
	outerHandler := WithResourcesContext(nil, outer, nil, po.New(), nil, innerHandler)
	recorder := httptest.NewRecorder()
	outerHandler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := recorder.Result().Header.Get(headers.NameAllowOrigin); got != "https://public.example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want client-facing policy", got)
	}
}
