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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// originOPC points a harness-built request at ts and returns a func that runs
// one request through the object proxy cache, returning status and body.
func originOPC(t *testing.T, ts *httptest.Server, path string,
) (func(method string) (int, string), func()) {
	t.Helper()
	h, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusOK, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	u, err := r.URL.Parse(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	rsc.BackendOptions.Scheme = u.Scheme
	rsc.BackendOptions.Host = u.Host

	run := func(method string) (int, string) {
		return runOPCWith(rsc, method, u.String(), nil)
	}
	return run, func() { closeTestHarness(h, r) }
}

// runOPCWith drives one request through the proxy, routing reads through the
// object cache and writes through the invalidating proxy, as the backend's
// path configs do.
func runOPCWith(rsc *request.Resources, method, url string,
	hdrs map[string]string,
) (int, string) {
	req := httptest.NewRequest(method, url, nil)
	req.RequestURI = ""
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	req = request.SetResources(req, rsc)
	w := httptest.NewRecorder()
	if method == http.MethodGet || method == http.MethodHead {
		ObjectProxyCacheRequest(w, req)
	} else {
		ProxyAndInvalidate(w, req)
	}
	res := w.Result()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// originResources points a harness-built resource set at ts.
func originResources(t *testing.T, ts *httptest.Server, path string) (*request.Resources, string, func()) {
	t.Helper()
	h, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusOK, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	u, err := r.URL.Parse(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	rsc.BackendOptions.Scheme = u.Scheme
	rsc.BackendOptions.Host = u.Host
	return rsc, u.String(), func() { closeTestHarness(h, r) }
}

func TestStaleWhileRevalidateServesStaleAndRefreshes(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := hits.Add(1)
		w.Header().Set(headers.NameCacheControl, "max-age=1, stale-while-revalidate=60")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "body-%d", n)
	}))
	defer ts.Close()

	run, done := originOPC(t, ts, "/stale-reval")
	defer done()

	if _, body := run(http.MethodGet); body != "body-1" {
		t.Fatalf("got %s expected body-1", body)
	}
	// past the freshness lifetime but inside the stale window
	time.Sleep(1100 * time.Millisecond)
	if _, body := run(http.MethodGet); body != "body-1" {
		t.Errorf("got %s expected the stale body to be served as it is", body)
	}
	// the refresh runs behind that response, so the next request sees it
	for range 40 {
		if hits.Load() > 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if hits.Load() < 2 {
		t.Fatal("expected a background revalidation to reach the origin")
	}
	if _, body := run(http.MethodGet); body == "body-1" {
		t.Error("expected the refreshed body after the background revalidation")
	}
}

func TestStaleIfErrorServesStale(t *testing.T) {
	var fail atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("the origin is unwell"))
			return
		}
		w.Header().Set(headers.NameCacheControl, "max-age=1, stale-if-error=60")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("the original body"))
	}))
	defer ts.Close()

	run, done := originOPC(t, ts, "/stale-err")
	defer done()

	if _, body := run(http.MethodGet); body != "the original body" {
		t.Fatalf("got %s", body)
	}
	time.Sleep(1100 * time.Millisecond)
	fail.Store(true)
	code, body := run(http.MethodGet)
	if code != http.StatusOK || body != "the original body" {
		t.Errorf("got %d %q expected the stale body to stand in for the failing origin", code, body)
	}
}

func TestUnsafeMethodInvalidatesStoredResponse(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		n := hits.Add(1)
		w.Header().Set(headers.NameCacheControl, "max-age=3600")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "body-%d", n)
	}))
	defer ts.Close()

	run, done := originOPC(t, ts, "/inv")
	defer done()

	if _, body := run(http.MethodGet); body != "body-1" {
		t.Fatalf("got %s expected body-1", body)
	}
	if _, body := run(http.MethodGet); body != "body-1" {
		t.Fatalf("got %s expected the stored response", body)
	}
	if code, _ := run(http.MethodPut); code != http.StatusNoContent {
		t.Fatalf("got %d expected the write to reach the origin", code)
	}
	// RFC 9111 4.4: the write superseded what was stored for this URI
	if _, body := run(http.MethodGet); body != "body-2" {
		t.Errorf("got %s expected the write to have invalidated the stored response", body)
	}
}

// on a cold miss there is no stored validator to compare against, so the
// origin evaluates If-Range; a 206 it returns must reach the client as a 206
func TestColdMissIfRangeLetsOriginDecide(t *testing.T) {
	const full = "0123456789abcdefghij"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headers.NameCacheControl, "max-age=3600")
		w.Header().Set(headers.NameETag, `"current"`)
		// the origin honors the range only while the validator still matches
		if r.Header.Get(headers.NameIfRange) == `"current"` && r.Header.Get(headers.NameRange) != "" {
			w.Header().Set(headers.NameContentRange, "bytes 0-4/20")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte(full[:5]))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(full))
	}))
	defer ts.Close()

	tests := []struct {
		name     string
		ifRange  string
		wantCode int
		wantBody string
	}{
		{"matching validator gets partial content", `"current"`, http.StatusPartialContent, full[:5]},
		{"stale validator gets the whole representation", `"obsolete"`, http.StatusOK, full},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rsc, url, done := originResources(t, ts, "/coldifrange-"+test.ifRange)
			defer done()
			code, body := runOPCWith(rsc, http.MethodGet, url, map[string]string{
				headers.NameRange:   "bytes=0-4",
				headers.NameIfRange: test.ifRange,
			})
			if code != test.wantCode || body != test.wantBody {
				t.Errorf("got %d %q expected %d %q", code, body, test.wantCode, test.wantBody)
			}
		})
	}
}
