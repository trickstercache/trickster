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
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

const resultValue = "engine=ObjectProxyCache; status=hit"

func hiding() *request.Resources {
	p := po.New()
	p.HideResultHeader = true
	return &request.Resources{PathConfig: p}
}

func TestHideResultHeaderWithholdsAndKeeps(t *testing.T) {
	// The client never sees the header, and the value is kept for the access log
	rsc := hiding()
	rec := httptest.NewRecorder()
	w := HideResultHeader(rec, rsc)
	w.Header().Set(headers.NameTricksterResult, resultValue)
	w.Header().Set("X-Other", "kept")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	res := rec.Result()
	if got := res.Header.Get(headers.NameTricksterResult); got != "" {
		t.Fatalf("X-Trickster-Result reached the client: %q", got)
	}
	if got := res.Header.Get("X-Other"); got != "kept" {
		t.Fatalf("X-Other = %q, want kept", got)
	}
	if rsc.HiddenResult != resultValue {
		t.Fatalf("HiddenResult = %q, want %q", rsc.HiddenResult, resultValue)
	}
	if rec.Body.String() != "body" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if u, ok := w.(interface{ Unwrap() http.ResponseWriter }); !ok || u.Unwrap() != rec {
		t.Fatal("the wrapper must unwrap to the writer it wraps")
	}
}

func TestHideResultHeaderOnImplicitWrite(t *testing.T) {
	// A handler that never calls WriteHeader still has the header taken at its first Write
	rsc := hiding()
	rec := httptest.NewRecorder()
	w := HideResultHeader(rec, rsc)
	w.Header().Set(headers.NameTricksterResult, resultValue)
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := rec.Result().Header.Get(headers.NameTricksterResult); got != "" {
		t.Fatalf("X-Trickster-Result reached the client: %q", got)
	}
	if rsc.HiddenResult != resultValue {
		t.Fatalf("HiddenResult = %q", rsc.HiddenResult)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// finalWriter records the header as sent at the final status, which a recorder does not: it
// snapshots at the first WriteHeader, informational or not
type finalWriter struct {
	header http.Header
	sent   http.Header
	codes  []int
}

func (w *finalWriter) Header() http.Header { return w.header }

func (w *finalWriter) WriteHeader(code int) {
	w.codes = append(w.codes, code)
	if code >= 200 {
		w.sent = w.header.Clone()
	}
}

func (w *finalWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestHideResultHeaderPassesInformationalResponses(t *testing.T) {
	// A 1xx carries no result; the header is taken at the final status, and a nil
	// resources object only drops the value
	fw := &finalWriter{header: make(http.Header)}
	w := HideResultHeader(fw, nil)
	w.Header().Set(headers.NameTricksterResult, resultValue)
	w.WriteHeader(http.StatusContinue)
	if got := w.Header().Get(headers.NameTricksterResult); got != resultValue {
		t.Fatalf("an informational response must not take the header (got %q)", got)
	}
	w.WriteHeader(http.StatusOK)
	if got := fw.sent.Get(headers.NameTricksterResult); got != "" {
		t.Fatalf("X-Trickster-Result reached the client: %q", got)
	}
	if len(fw.codes) != 2 {
		t.Fatalf("codes = %v, want both delegated", fw.codes)
	}
}

func TestWithResourcesContextHidesTheResultHeader(t *testing.T) {
	// The path option is honored whether the request arrives with resources, as one does
	// from the access log or an outer route, or without
	path := po.New()
	path.HideResultHeader = true
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameTricksterResult, resultValue)
		w.WriteHeader(http.StatusOK)
	})
	h := WithResourcesContext(nil, bo.New(), nil, path, nil, next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Result().Header.Get(headers.NameTricksterResult); got != "" {
		t.Fatalf("X-Trickster-Result reached the client: %q", got)
	}

	outer := &request.Resources{}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, request.SetResources(httptest.NewRequest(http.MethodGet, "/", nil), outer))
	if got := rec.Result().Header.Get(headers.NameTricksterResult); got != "" {
		t.Fatalf("X-Trickster-Result reached the client: %q", got)
	}
	if outer.HiddenResult != resultValue {
		t.Fatalf("the outer resources must carry the withheld value (got %q)", outer.HiddenResult)
	}

	// a path that does not hide it leaves it alone
	shown := WithResourcesContext(nil, bo.New(), nil, po.New(), nil, next)
	rec = httptest.NewRecorder()
	shown.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Result().Header.Get(headers.NameTricksterResult); got != resultValue {
		t.Fatalf("X-Trickster-Result = %q, want it exposed", got)
	}
}

func TestHideResultHeaderFollowsTheServingPath(t *testing.T) {
	// The decision is the serving path's, read at write time: a dispatch from a hiding path to
	// one that exposes the header leaves it, and one to a hiding path takes it
	exposing := po.New()
	rsc := hiding()
	rec := httptest.NewRecorder()
	w := HideResultHeader(rec, rsc)
	w.Header().Set(headers.NameTricksterResult, resultValue)
	rsc.PathConfig = exposing
	w.WriteHeader(http.StatusOK)
	if got := rec.Result().Header.Get(headers.NameTricksterResult); got != resultValue {
		t.Fatalf("an exposing path must keep the header (got %q)", got)
	}
	if rsc.HiddenResult != "" {
		t.Fatalf("nothing was withheld, so nothing is kept (got %q)", rsc.HiddenResult)
	}

	// without a path to consult the wrapper hides, since it was installed to
	rec = httptest.NewRecorder()
	w = HideResultHeader(rec, &request.Resources{})
	w.Header().Set(headers.NameTricksterResult, resultValue)
	w.WriteHeader(http.StatusOK)
	if got := rec.Result().Header.Get(headers.NameTricksterResult); got != "" {
		t.Fatalf("X-Trickster-Result reached the client: %q", got)
	}
}

func TestWithResourcesContextLetsANestedPathExposeTheResult(t *testing.T) {
	// A hiding path dispatching to an exposing one, as a weighted rule's ALB dispatches to a
	// member whose Service policy exposes, serves the header; a hiding member withholds it
	hide := po.New()
	hide.HideResultHeader = true
	answer := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameTricksterResult, resultValue)
		w.WriteHeader(http.StatusOK)
	})
	for name, member := range map[string]*po.Options{"exposing": po.New(), "hiding": hide} {
		t.Run(name, func(t *testing.T) {
			inner := WithResourcesContext(nil, bo.New(), nil, member, nil, answer)
			outer := WithResourcesContext(nil, bo.New(), nil, hide, nil, inner)
			rec := httptest.NewRecorder()
			outer.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			got := rec.Result().Header.Get(headers.NameTricksterResult)
			if name == "exposing" && got != resultValue {
				t.Fatalf("the member's policy must decide (got %q)", got)
			}
			if name == "hiding" && got != "" {
				t.Fatalf("X-Trickster-Result reached the client: %q", got)
			}
		})
	}
}
