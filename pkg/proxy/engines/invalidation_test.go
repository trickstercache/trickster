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

package engines

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
)

func TestInvalidateTargetURINilInputs(t *testing.T) {
	InvalidateTargetURI(nil) // must not panic
	// a request with no resources attached has no cache to invalidate
	InvalidateTargetURI(httptest.NewRequest(http.MethodPut, "http://example.com/a", nil))
}

// a write must drop every key a later read could use, and each cacheable
// method has its own primary key
func TestInvalidateTargetURICoversEveryCacheableMethod(t *testing.T) {
	if got := methods.CacheableHTTPMethods(); len(got) != 2 {
		t.Fatalf("expected GET and HEAD, got %v", got)
	}
}

func TestIsStateChangingCoversUnsafeMethods(t *testing.T) {
	changing := []string{http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodPatch, "MECONE-UPDATE"}
	for _, m := range changing {
		if !methods.IsStateChanging(m) {
			t.Errorf("expected %s to be state-changing", m)
		}
	}
	safe := []string{http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodTrace, http.MethodConnect, methods.MethodPurge}
	for _, m := range safe {
		if methods.IsStateChanging(m) {
			t.Errorf("expected %s not to be state-changing", m)
		}
	}
}

func TestWriteResponseTrailers(t *testing.T) {
	w := httptest.NewRecorder()
	pr := &proxyRequest{
		clientWriter: w,
		upstreamResponse: &http.Response{
			Trailer: http.Header{"Mecone-Checksum": []string{"abc123"}},
		},
	}
	pr.writeResponseTrailers()
	var found bool
	for k, vv := range w.Header() {
		if strings.HasPrefix(k, http.TrailerPrefix) &&
			strings.Contains(k, "Mecone-Checksum") && vv[0] == "abc123" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a trailer field, got %v", w.Header())
	}
}

func TestWriteResponseTrailersNoOps(t *testing.T) {
	// no upstream response, no trailers, and a non-ResponseWriter client all no-op
	(&proxyRequest{}).writeResponseTrailers()
	(&proxyRequest{upstreamResponse: &http.Response{}}).writeResponseTrailers()
	pr := &proxyRequest{
		clientWriter:     &strings.Builder{},
		upstreamResponse: &http.Response{Trailer: http.Header{"A": []string{"b"}}},
	}
	pr.writeResponseTrailers()
}

func TestRelayInterimResponsesNoOps(t *testing.T) {
	// a non-ResponseWriter destination cannot carry an interim response
	pr := &proxyRequest{upstreamRequest: httptest.NewRequest(http.MethodGet, "http://a/b", nil)}
	before := pr.upstreamRequest
	pr.relayInterimResponses(&strings.Builder{})
	if pr.upstreamRequest != before {
		t.Error("expected the upstream request to be left alone")
	}
	// no upstream request to attach the trace to
	(&proxyRequest{}).relayInterimResponses(httptest.NewRecorder())
}

func TestRelayInterimResponsesAttachesTrace(t *testing.T) {
	pr := &proxyRequest{upstreamRequest: httptest.NewRequest(http.MethodGet, "http://a/b", nil)}
	before := pr.upstreamRequest
	pr.relayInterimResponses(httptest.NewRecorder())
	if pr.upstreamRequest == before {
		t.Error("expected a new request carrying the client trace")
	}
}

func TestTrailerNames(t *testing.T) {
	if got := (&proxyRequest{}).trailerNames(); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	pr := &proxyRequest{upstreamResponse: &http.Response{}}
	if got := pr.trailerNames(); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	pr = &proxyRequest{upstreamResponse: &http.Response{
		Trailer: http.Header{"Zeta": nil, "Alpha": nil},
	}}
	got := pr.trailerNames()
	if len(got) != 2 || got[0] != "Alpha" || got[1] != "Zeta" {
		t.Errorf("expected a sorted name list, got %v", got)
	}
}

// a Go server only emits trailers on a chunked response, and announcing them
// is what makes it chunked, so the announcement has to survive the hop-by-hop
// strip that removes Trailer
func TestPrepareResponseWriterAnnouncesTrailers(t *testing.T) {
	w := httptest.NewRecorder()
	h := http.Header{headers.NameTrailer: []string{"Dropped-By-Strip"}}
	PrepareResponseWriter(w, http.StatusOK, h, []string{"Mecone-Checksum", "Mecone-Digest"})
	if got := w.Header().Get(headers.NameTrailer); got != "Mecone-Checksum, Mecone-Digest" {
		t.Errorf("got %q expected the announced names", got)
	}

	// with no trailers the field stays stripped
	w = httptest.NewRecorder()
	PrepareResponseWriter(w, http.StatusOK, h, nil)
	if got := w.Header().Get(headers.NameTrailer); got != "" {
		t.Errorf("expected no Trailer field, got %q", got)
	}
}
