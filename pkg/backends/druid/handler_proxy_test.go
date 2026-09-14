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

package druid

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func (h *druidHandlerHarness) objectQuery(t *testing.T, path, body string) (*http.Response, []byte) {
	return h.objectRequest(t, http.MethodPost, path, body)
}

func (h *druidHandlerHarness) objectRequest(t *testing.T, method, path,
	body string,
) (*http.Response, []byte) {
	t.Helper()
	r := httptest.NewRequest(method, "http://trickster"+path, strings.NewReader(body))
	r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	rsc := request.NewResources(h.resources.BackendOptions, h.resources.PathConfig,
		h.resources.CacheConfig, h.resources.CacheClient, h.client, h.resources.Tracer)
	r = request.SetResources(r, rsc)
	w := httptest.NewRecorder()
	h.client.ObjectProxyCacheHandler(w, r)
	response := w.Result()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, responseBody
}

func TestDataSourcesObjectCacheKeysIncludeQueryParameters(t *testing.T) {
	var mu sync.Mutex
	originRequests := make([]string, 0, 2)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		originRequests = append(originRequests, r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
		fmt.Fprintf(w, `{"request":%q}`, r.URL.RequestURI())
	}))
	defer origin.Close()
	harness := newDruidHandlerHarness(t, origin, http.MethodGet,
		"/druid/v2/datasources/wiki")
	defer harness.cleanup()

	firstPath := "/druid/v2/datasources/wiki?full=true"
	response, firstBody := harness.objectRequest(t, http.MethodGet, firstPath, "")
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusKeyMiss {
		t.Fatalf("first response: code=%d header=%q body=%s", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult), firstBody)
	}
	response, repeatedBody := harness.objectRequest(t, http.MethodGet, firstPath, "")
	if resultStatus(t, response) != status.StatusHit || string(repeatedBody) != string(firstBody) {
		t.Fatalf("repeat response: header=%q body=%s",
			response.Header.Get(headers.NameTricksterResult), repeatedBody)
	}
	secondPath := "/druid/v2/datasources/wiki?full=false"
	response, secondBody := harness.objectRequest(t, http.MethodGet, secondPath, "")
	if resultStatus(t, response) != status.StatusKeyMiss || string(secondBody) == string(firstBody) {
		t.Fatalf("different-query response: header=%q body=%s",
			response.Header.Get(headers.NameTricksterResult), secondBody)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(originRequests) != 2 || originRequests[0] != firstPath || originRequests[1] != secondPath {
		t.Fatalf("origin requests = %v", originRequests)
	}
}

func TestSQLObjectCacheKeysIncludePOSTBody(t *testing.T) {
	var mu sync.Mutex
	originBodies := make([]string, 0, 2)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		originBodies = append(originBodies, string(body))
		mu.Unlock()
		w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
		fmt.Fprintf(w, `{"request":%q}`, body)
	}))
	defer origin.Close()
	harness := newDruidHandlerHarness(t, origin, http.MethodPost, "/druid/v2/sql")
	defer harness.cleanup()

	first := `{"query":"SELECT 1","resultFormat":"object"}`
	second := `{"query":"SELECT 2","resultFormat":"object"}`
	response, firstResponse := harness.objectQuery(t, "/druid/v2/sql", first)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusKeyMiss {
		t.Fatalf("first response: code=%d header=%q body=%s", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult), firstResponse)
	}
	response, secondResponse := harness.objectQuery(t, "/druid/v2/sql", second)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusKeyMiss {
		t.Fatalf("second response: code=%d header=%q body=%s", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult), secondResponse)
	}
	if string(firstResponse) == string(secondResponse) {
		t.Fatal("different SQL bodies returned the same cached response")
	}
	response, repeatedResponse := harness.objectQuery(t, "/druid/v2/sql", first)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusHit ||
		string(repeatedResponse) != string(firstResponse) {
		t.Fatalf("repeat response: code=%d header=%q body=%s", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult), repeatedResponse)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(originBodies) != 2 || originBodies[0] != first || originBodies[1] != second {
		t.Fatalf("origin bodies = %v", originBodies)
	}
}
