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

package cors

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config/types"
	corso "github.com/trickstercache/trickster/v2/pkg/proxy/cors/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestApply(t *testing.T) {
	tests := []struct {
		name       string
		options    *corso.Options
		wantOrigin string
		wantCreds  string
		wantExpose string
	}{
		{
			name:       "legacy wildcard preserves other headers",
			wantOrigin: "*",
			wantCreds:  "true",
		},
		{
			name:       "preserve",
			options:    &corso.Options{Mode: corso.ModePreserve},
			wantOrigin: "https://origin.example.com",
			wantCreds:  "true",
		},
		{
			name: "merge",
			options: &corso.Options{Mode: corso.ModeMerge, Headers: types.EnvStringMap{
				headers.NameAllowOrigin:             "https://trickster.example.com",
				"-Access-Control-Allow-Credentials": "",
				"Access-Control-Expose-Headers":     "X-Trickster-Result",
			}},
			wantOrigin: "https://trickster.example.com",
			wantExpose: "X-Trickster-Result",
		},
		{
			name: "replace",
			options: &corso.Options{Mode: corso.ModeReplace, Headers: types.EnvStringMap{
				headers.NameAllowOrigin: "https://trickster.example.com",
			}},
			wantOrigin: "https://trickster.example.com",
		},
		{
			name:    "replace empty disables cors",
			options: &corso.Options{Mode: corso.ModeReplace},
		},
		{
			name:    "disable",
			options: &corso.Options{Mode: corso.ModeDisable},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{
				headers.NameAllowOrigin:            {"https://origin.example.com"},
				"access-control-allow-credentials": {"true"},
				"X-Unrelated":                      {"keep"},
			}
			Apply(h, tc.options)
			if got := h.Get(headers.NameAllowOrigin); got != tc.wantOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tc.wantOrigin)
			}
			if got := headerValue(h, "Access-Control-Allow-Credentials"); got != tc.wantCreds {
				t.Errorf("Access-Control-Allow-Credentials = %q, want %q", got, tc.wantCreds)
			}
			if got := h.Get("Access-Control-Expose-Headers"); got != tc.wantExpose {
				t.Errorf("Access-Control-Expose-Headers = %q, want %q", got, tc.wantExpose)
			}
			if got := h.Get("X-Unrelated"); got != "keep" {
				t.Errorf("X-Unrelated = %q, want keep", got)
			}
		})
	}
}

func headerValue(h http.Header, name string) string {
	for k, values := range h {
		if http.CanonicalHeaderKey(k) == http.CanonicalHeaderKey(name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func TestResponseWriterAppliesPolicyOnImplicitStatus(t *testing.T) {
	recorder := httptest.NewRecorder()
	w := Wrap(recorder, &corso.Options{Mode: corso.ModeReplace})
	w.Header().Set(headers.NameAllowOrigin, "https://origin.example.com")
	if _, err := w.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Result().Header.Get(headers.NameAllowOrigin); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestResponseWriterFinalizesWithoutWrite(t *testing.T) {
	recorder := httptest.NewRecorder()
	w := Wrap(recorder, &corso.Options{Mode: corso.ModeReplace})
	w.Header().Set(headers.NameAllowOrigin, "https://origin.example.com")
	w.Finalize()
	if got := recorder.Result().Header.Get(headers.NameAllowOrigin); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}

func TestResponseWriterAllowsInformationalBeforeFinalStatus(t *testing.T) {
	w := &statusResponseWriter{header: make(http.Header)}
	cw := Wrap(w, &corso.Options{Mode: corso.ModeReplace, Headers: types.EnvStringMap{
		headers.NameAllowOrigin: "https://trickster.example.com",
	}})
	cw.WriteHeader(http.StatusEarlyHints)
	cw.WriteHeader(http.StatusNoContent)

	if len(w.codes) != 2 || w.codes[0] != http.StatusEarlyHints || w.codes[1] != http.StatusNoContent {
		t.Fatalf("status codes = %v, want [%d %d]", w.codes,
			http.StatusEarlyHints, http.StatusNoContent)
	}
	if got := w.header.Get(headers.NameAllowOrigin); got != "https://trickster.example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

type statusResponseWriter struct {
	header http.Header
	codes  []int
}

func (w *statusResponseWriter) Header() http.Header {
	return w.header
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.codes = append(w.codes, code)
}

func (w *statusResponseWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

func TestResponseWriterUnwrap(t *testing.T) {
	recorder := httptest.NewRecorder()
	w := Wrap(recorder, nil)
	unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
	if !ok || unwrapper.Unwrap() != recorder {
		t.Fatal("wrapped response writer must expose its underlying writer")
	}
}
