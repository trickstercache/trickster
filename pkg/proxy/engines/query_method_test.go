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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

const testQueryMediaType = "application/sql"

func runOPCQuery(rsc *request.Resources, url, body string, hdrs map[string]string) *http.Response {
	req := httptest.NewRequest(methods.MethodQuery, url, strings.NewReader(body))
	req.RequestURI = ""
	req.Header.Set(headers.NameContentType, testQueryMediaType)
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	// each request carries its own body cache, as it would when served
	rsc = rsc.Clone()
	rsc.RequestBody = nil
	req = request.SetResources(req, rsc)
	w := httptest.NewRecorder()
	ObjectProxyCacheRequest(w, req)
	return w.Result()
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestObjectProxyCacheQueryKeysOnContent(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != methods.MethodQuery {
			t.Errorf("origin got %s, expected QUERY", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		w.Header().Set(headers.NameCacheControl, "max-age=3600")
		w.Header().Set(headers.NameETag, `"q1"`)
		fmt.Fprintf(w, "result of %s", b)
	}))
	defer ts.Close()

	rsc, url, done := originResources(t, ts, "/search")
	defer done()

	if body := readAll(t, runOPCQuery(rsc, url, "SELECT 1", nil)); body != "result of SELECT 1" {
		t.Fatalf("got %q", body)
	}
	if body := readAll(t, runOPCQuery(rsc, url, "SELECT 1", nil)); body != "result of SELECT 1" {
		t.Fatalf("got %q", body)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("expected the repeated QUERY to be served from cache, origin saw %d", n)
	}
	if body := readAll(t, runOPCQuery(rsc, url, "SELECT 2", nil)); body != "result of SELECT 2" {
		t.Errorf("got %q expected the second query's own result", body)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("expected a different body to miss, origin saw %d", n)
	}

	// RFC 10008 2.6: a conditional QUERY is evaluated as for GET
	resp := runOPCQuery(rsc, url, "SELECT 1", map[string]string{headers.NameIfNoneMatch: `"q1"`})
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("got %d expected 304 for a matching If-None-Match", resp.StatusCode)
	}
}
