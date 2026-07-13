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

package elasticsearch

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestQueryHandlerUnsupportedSearchUsesObjectProxyCache(t *testing.T) {
	const originBody = `{"hits":{"hits":[]}}`
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
		http.StatusOK, originBody, nil, providers.Elasticsearch, "/_search", "debug")
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	r.Method = http.MethodPost
	request.SetBody(r, []byte(`{"query":{"match_all":{}}}`))
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := backendClient.(*Client)
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

	client.QueryHandler(w, r)
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != originBody {
		t.Fatalf("body = %s, want %s", got, originBody)
	}
}

func TestQueryHandlerFallbackCacheSeparatesTimeRanges(t *testing.T) {
	var originHits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"request": string(body)})
	}))
	defer origin.Close()

	prototype, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	testOrigin, _, baseRequest, _, err := tu.NewTestInstance("", prototype.DefaultPathConfigs,
		http.StatusOK, `{}`, nil, providers.Elasticsearch, "/_search", "debug")
	if err != nil {
		t.Fatal(err)
	}
	defer testOrigin.Close()

	base := request.GetResources(baseRequest)
	base.BackendOptions.OriginURL = origin.URL
	if err := base.BackendOptions.Initialize("test"); err != nil {
		t.Fatal(err)
	}
	backendClient, err := NewClient("test", base.BackendOptions, nil, base.CacheClient, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := backendClient.(*Client)
	base.BackendOptions.HTTPClient = backendClient.HTTPClient()

	serve := func(body string) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/_search", strings.NewReader(body))
		rsc := request.NewResources(base.BackendOptions, base.PathConfig, base.CacheConfig,
			base.CacheClient, client, base.Tracer)
		r = request.SetResources(r, rsc)
		w := httptest.NewRecorder()
		client.QueryHandler(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	firstRequest := `{"size":0,"query":{"range":{"@timestamp":` +
		`{"gte":1704067200000,"lte":1704067800000,"format":"epoch_millis"}}}}`
	secondRequest := `{"size":0,"query":{"range":{"@timestamp":` +
		`{"gte":1704067800000,"lte":1704068400000,"format":"epoch_millis"}}}}`
	first := serve(firstRequest)
	second := serve(secondRequest)
	firstCached := serve(firstRequest)

	if first == second {
		t.Fatalf("different time ranges returned the same cached body: %s", first)
	}
	if firstCached != first {
		t.Fatalf("repeated first range did not use its cached response: first=%s cached=%s", first, firstCached)
	}
	if got := originHits.Load(); got != 2 {
		t.Fatalf("origin requests = %d, want 2", got)
	}
}

func TestQueryHandlerCachesOnlyCompleteHistogramBuckets(t *testing.T) {
	type observedRange struct {
		start int64
		end   int64
	}
	var mu sync.Mutex
	var observed []observedRange
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		rangeBody := firstTimestampRange(body)
		start := int64(rangeBody["gte"].(float64))
		end := int64(rangeBody["lte"].(float64))
		mu.Lock()
		observed = append(observed, observedRange{start: start, end: end})
		mu.Unlock()

		buckets := make([]map[string]any, 0)
		for key := start; key <= end; key += time.Minute.Milliseconds() {
			buckets = append(buckets, map[string]any{"key": key, "doc_count": 1})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"took":      1,
			"timed_out": false,
			"_shards":   map[string]any{"total": 1, "successful": 1, "failed": 0},
			"hits": map[string]any{
				"total": map[string]any{"value": len(buckets), "relation": "eq"},
				"hits":  []any{},
			},
			"aggregations": map[string]any{"2": map[string]any{"buckets": buckets}},
		})
	}))
	defer origin.Close()

	prototype, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	testOrigin, _, baseRequest, _, err := tu.NewTestInstance("", prototype.DefaultPathConfigs,
		http.StatusOK, `{}`, nil, providers.Elasticsearch, "/_search", "debug")
	if err != nil {
		t.Fatal(err)
	}
	defer testOrigin.Close()

	base := request.GetResources(baseRequest)
	base.BackendOptions.OriginURL = origin.URL
	if err := base.BackendOptions.Initialize("test"); err != nil {
		t.Fatal(err)
	}
	backendClient, err := NewClient("test", base.BackendOptions, nil, base.CacheClient, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := backendClient.(*Client)
	base.BackendOptions.HTTPClient = backendClient.HTTPClient()

	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute).UnixMilli()
	search := func(bucketCount int) string {
		lastBucket := start + int64(bucketCount-1)*time.Minute.Milliseconds()
		return fmt.Sprintf(`{"size":0,"query":{"bool":{"filter":[{"range":{"@timestamp":`+
			`{"gte":%d,"lte":%d,"format":"epoch_millis"}}}]}},"aggs":{"2":{"date_histogram":`+
			`{"field":"@timestamp","fixed_interval":"1m","min_doc_count":0,`+
			`"extended_bounds":{"min":%d,"max":%d}}}}}`,
			start, lastBucket+time.Minute.Milliseconds()-1, start, lastBucket)
	}
	serve := func(bucketCount int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/_search", strings.NewReader(search(bucketCount)))
		r.Header.Set("Content-Type", "application/json")
		rsc := request.NewResources(base.BackendOptions, base.PathConfig, base.CacheConfig,
			base.CacheClient, client, base.Tracer)
		r = request.SetResources(r, rsc)
		w := httptest.NewRecorder()
		client.QueryHandler(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	first := serve(3)
	second := serve(4)
	again := serve(3)
	for _, tt := range []struct {
		response map[string]any
		want     int
	}{{first, 3}, {second, 4}, {again, 3}} {
		buckets := tt.response["aggregations"].(map[string]any)["2"].(map[string]any)["buckets"].([]any)
		if got := len(buckets); got != tt.want {
			t.Fatalf("buckets = %d, want %d", got, tt.want)
		}
		total := tt.response["hits"].(map[string]any)["total"].(map[string]any)["value"].(float64)
		if got := int(total); got != tt.want {
			t.Fatalf("total hits = %d, want %d", got, tt.want)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if got := len(observed); got != 2 {
		t.Fatalf("origin requests = %d, want 2", got)
	}
	wantRanges := []observedRange{
		{start: start, end: start + 3*time.Minute.Milliseconds() - 1},
		{start: start + 3*time.Minute.Milliseconds(), end: start + 4*time.Minute.Milliseconds() - 1},
	}
	for i := range wantRanges {
		if observed[i] != wantRanges[i] {
			t.Fatalf("origin range %d = %+v, want %+v", i, observed[i], wantRanges[i])
		}
	}
}
