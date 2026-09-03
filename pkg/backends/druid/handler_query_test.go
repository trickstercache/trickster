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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

type druidOriginCapture struct {
	mu        sync.Mutex
	intervals []string
	bodies    []string
}

type druidSQLOriginCapture struct {
	mu      sync.Mutex
	bodies  []string
	queries []string
}

func (c *druidSQLOriginCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query, ok := document["query"].(string)
	if !ok {
		http.Error(w, "missing query", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.bodies = append(c.bodies, string(body))
	c.queries = append(c.queries, query)
	c.mu.Unlock()
	// The test origin returns one row per hour for the rewritten half-open
	// bounds. It emits both tabular Druid SQL response shapes used by the
	// provider: object rows and array rows with a column-name header.
	const marker = "TIMESTAMP '"
	startAt := strings.Index(query, "__time >= "+marker)
	endAt := strings.Index(query, "__time < "+marker)
	if startAt < 0 || endAt < 0 {
		http.Error(w, "missing bounds", http.StatusBadRequest)
		return
	}
	startText := query[startAt+len("__time >= "+marker) : startAt+len("__time >= "+marker)+19]
	endText := query[endAt+len("__time < "+marker) : endAt+len("__time < "+marker)+19]
	start, startErr := time.ParseInLocation("2006-01-02 15:04:05", startText, time.UTC)
	end, endErr := time.ParseInLocation("2006-01-02 15:04:05", endText, time.UTC)
	if startErr != nil || endErr != nil || !end.After(start) {
		http.Error(w, "invalid bounds", http.StatusBadRequest)
		return
	}
	rows := make([]map[string]any, 0, int(end.Sub(start)/time.Hour))
	for current := start; current.Before(end); current = current.Add(time.Hour) {
		rows = append(rows, map[string]any{
			"bucket": current.UTC().Format("2006-01-02T15:04:05.000Z"),
			"host":   "a",
			"value":  current.Unix() / 3600,
		})
	}
	w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	if strings.EqualFold(fmt.Sprint(document["resultFormat"]), "array") &&
		booleanValue(document["header"]) {
		arrayRows := make([][]any, 0, len(rows)+1)
		arrayRows = append(arrayRows, []any{"bucket", "value", "host"})
		for _, row := range rows {
			arrayRows = append(arrayRows, []any{row["bucket"], row["value"], row["host"]})
		}
		_ = json.NewEncoder(w).Encode(arrayRows)
		return
	}
	_ = json.NewEncoder(w).Encode(rows)
}

func (c *druidSQLOriginCapture) snapshot() ([]string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...), append([]string(nil), c.queries...)
}

func (c *druidOriginCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.bodies = append(c.bodies, string(body))
	c.mu.Unlock()
	if strings.EqualFold(fmt.Sprint(document["queryType"]), "scan") {
		w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
		fmt.Fprint(w, `{"result":"scan"}`)
		return
	}
	intervalValues, ok := document["intervals"].([]any)
	if !ok || len(intervalValues) != 1 {
		http.Error(w, "invalid intervals", http.StatusBadRequest)
		return
	}
	interval, ok := intervalValues[0].(string)
	if !ok {
		http.Error(w, "invalid interval", http.StatusBadRequest)
		return
	}
	parts := strings.SplitN(interval, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid interval", http.StatusBadRequest)
		return
	}
	start, startErr := time.Parse(time.RFC3339Nano, parts[0])
	end, endErr := time.Parse(time.RFC3339Nano, parts[1])
	if startErr != nil || endErr != nil {
		http.Error(w, "invalid interval", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.intervals = append(c.intervals, interval)
	c.mu.Unlock()
	rows := make([]map[string]any, 0, int(end.Sub(start)/time.Minute))
	for current := start; current.Before(end); current = current.Add(time.Minute) {
		rows = append(rows, map[string]any{
			"timestamp": current.UTC().Format("2006-01-02T15:04:05.000Z"),
			"result":    map[string]any{"count": current.Unix() / 60},
		})
	}
	w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	json.NewEncoder(w).Encode(rows)
}

func (c *druidOriginCapture) snapshot() ([]string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.intervals...), append([]string(nil), c.bodies...)
}

type druidHandlerHarness struct {
	client    *Client
	resources *request.Resources
	cleanup   func()
}

func newDruidHandlerHarness(t *testing.T, origin *httptest.Server,
	method, path string,
) *druidHandlerHarness {
	t.Helper()
	seed, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	placeholder, _, initialRequest, _, err := tu.NewTestInstance("", seed.DefaultPathConfigs,
		http.StatusOK, `[]`, nil, providers.Druid, path, "error")
	if err != nil {
		t.Fatal(err)
	}
	initial := request.GetResources(initialRequest)
	options := initial.BackendOptions
	options.OriginURL = origin.URL
	if err := options.Initialize("default"); err != nil {
		placeholder.Close()
		t.Fatal(err)
	}
	backend, err := NewClient("default", options, nil, initial.CacheClient, nil, nil)
	if err != nil {
		placeholder.Close()
		t.Fatal(err)
	}
	client := backend.(*Client)
	pathConfig := client.DefaultPathConfigs(options).Match(method, path)
	if pathConfig == nil {
		placeholder.Close()
		t.Fatalf("no Druid route for %s %s", method, path)
	}
	initial.BackendClient = client
	initial.BackendOptions.HTTPClient = client.HTTPClient()
	return &druidHandlerHarness{
		client: client,
		resources: request.NewResources(options, pathConfig, initial.CacheConfig,
			initial.CacheClient, client, initial.Tracer),
		cleanup: placeholder.Close,
	}
}

func (h *druidHandlerHarness) query(t *testing.T, path, body string) (*http.Response, []byte) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://trickster"+path, strings.NewReader(body))
	r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	rsc := request.NewResources(h.resources.BackendOptions, h.resources.PathConfig,
		h.resources.CacheConfig, h.resources.CacheClient, h.client, h.resources.Tracer)
	r = request.SetResources(r, rsc)
	w := httptest.NewRecorder()
	h.client.QueryHandler(w, r)
	response := w.Result()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, responseBody
}

func (h *druidHandlerHarness) sqlQuery(t *testing.T, path, body string) (*http.Response, []byte) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://trickster"+path, strings.NewReader(body))
	r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	rsc := request.NewResources(h.resources.BackendOptions, h.resources.PathConfig,
		h.resources.CacheConfig, h.resources.CacheClient, h.client, h.resources.Tracer)
	r = request.SetResources(r, rsc)
	w := httptest.NewRecorder()
	h.client.SQLQueryHandler(w, r)
	response := w.Result()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, responseBody
}

func nativeTimeseriesBody(start, end time.Time, queryID string) string {
	return fmt.Sprintf(`{"queryType":"timeseries","dataSource":"wiki","intervals":[%q],"granularity":"minute","aggregations":[{"type":"count","name":"count"}],"context":{"queryId":%q,"skipEmptyBuckets":true}}`,
		start.Format(time.RFC3339)+"/"+end.Format(time.RFC3339), queryID)
}

func resultStatus(t *testing.T, response *http.Response) string {
	t.Helper()
	engine, cacheStatus := headers.ParseResultEngineStatus(
		response.Header.Get(headers.NameTricksterResult))
	if engine == "" {
		t.Fatal("missing Trickster result engine")
	}
	return cacheStatus
}

func TestQueryHandlerDeltaCacheIntegration(t *testing.T) {
	capture := &druidOriginCapture{}
	origin := httptest.NewServer(capture)
	defer origin.Close()
	harness := newDruidHandlerHarness(t, origin, http.MethodPost, "/druid/v2")
	defer harness.cleanup()

	start := time.Now().UTC().Truncate(time.Minute).Add(-10 * time.Minute)
	firstBody := nativeTimeseriesBody(start, start.Add(3*time.Minute), "first")
	response, body := harness.query(t, "/druid/v2", firstBody)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusKeyMiss {
		t.Fatalf("first response: code=%d header=%q body=%s", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult), body)
	}
	if rows := decodeRows(t, body); len(rows) != 3 {
		t.Fatalf("first response has %d rows", len(rows))
	}

	secondStart, secondEnd := start.Add(-time.Minute), start.Add(4*time.Minute)
	secondBody := nativeTimeseriesBody(secondStart, secondEnd, "second")
	response, body = harness.query(t, "/druid/v2", secondBody)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusPartialHit {
		t.Fatalf("second response: code=%d header=%q body=%s", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult), body)
	}
	if rows := decodeRows(t, body); len(rows) != 5 {
		t.Fatalf("second response has %d rows", len(rows))
	}

	response, body = harness.query(t, "/druid/v2", secondBody)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusHit {
		t.Fatalf("third response: code=%d header=%q body=%s", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult), body)
	}
	intervals, bodies := capture.snapshot()
	if len(intervals) != 3 {
		t.Fatalf("origin intervals = %v", intervals)
	}
	wantIntervals := map[string]bool{
		start.Format(time.RFC3339) + "/" + start.Add(3*time.Minute).Format(time.RFC3339):                    true,
		secondStart.Format(time.RFC3339) + "/" + start.Format(time.RFC3339):                                 true,
		start.Add(3*time.Minute).Format(time.RFC3339) + "/" + start.Add(4*time.Minute).Format(time.RFC3339): true,
	}
	for _, interval := range intervals {
		if !wantIntervals[interval] {
			t.Errorf("unexpected origin interval %q; all=%v", interval, intervals)
		}
		delete(wantIntervals, interval)
	}
	if len(wantIntervals) != 0 {
		t.Errorf("origin did not fetch intervals %v", wantIntervals)
	}
	if len(bodies) != 3 || !strings.Contains(bodies[0], `"queryId":"first"`) ||
		!strings.Contains(bodies[1], `"queryId":"second"`) ||
		!strings.Contains(bodies[2], `"queryId":"second"`) {
		t.Fatalf("origin did not receive the exact request context: %v", bodies)
	}
}

func decodeRows(t *testing.T, body []byte) []any {
	t.Helper()
	var rows []any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestQueryHandlerUnsupportedNativeUsesObjectCache(t *testing.T) {
	capture := &druidOriginCapture{}
	origin := httptest.NewServer(capture)
	defer origin.Close()
	harness := newDruidHandlerHarness(t, origin, http.MethodPost, "/druid/v2")
	defer harness.cleanup()
	body := `{"queryType":"scan","dataSource":"wiki","intervals":["2024-01-01/2024-01-02"]}`

	response, got := harness.query(t, "/druid/v2", body)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusKeyMiss ||
		string(got) != `{"result":"scan"}` {
		t.Fatalf("first OPC response: code=%d header=%q body=%s", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult), got)
	}
	response, got = harness.query(t, "/druid/v2", body)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusHit ||
		string(got) != `{"result":"scan"}` {
		t.Fatalf("second OPC response: code=%d header=%q body=%s", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult), got)
	}
	_, bodies := capture.snapshot()
	if len(bodies) != 1 || bodies[0] != body {
		t.Fatalf("origin bodies = %v", bodies)
	}
}

func TestSQLQueryHandlerDeltaCacheIntegration(t *testing.T) {
	capture := &druidSQLOriginCapture{}
	origin := httptest.NewServer(capture)
	defer origin.Close()
	harness := newDruidHandlerHarness(t, origin, http.MethodPost, "/druid/v2/sql")
	defer harness.cleanup()
	start := time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour)
	statement := func(from, to time.Time) string {
		return fmt.Sprintf("SELECT TIME_FLOOR(__time, 'PT1H') AS bucket, SUM(v) AS value, host FROM foo WHERE __time >= TIMESTAMP '%s' AND __time < TIMESTAMP '%s' GROUP BY 1, host", from.Format("2006-01-02 15:04:05"), to.Format("2006-01-02 15:04:05"))
	}
	body := func(from, to time.Time, id string) string {
		return fmt.Sprintf(`{"query":%q,"resultFormat":"object","context":{"queryId":%q,"sqlTimeZone":"UTC"}}`, statement(from, to), id)
	}
	firstStart, firstEnd := start, start.Add(2*time.Hour)
	firstRequestBody := body(firstStart, firstEnd, "first")
	secondStart, secondEnd := start.Add(-time.Hour), start.Add(3*time.Hour)
	secondRequestBody := body(secondStart, secondEnd, "second")
	response, got := harness.query(t, "/druid/v2/sql", firstRequestBody)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusKeyMiss {
		t.Fatalf("first response: code=%d header=%q body=%s", response.StatusCode, response.Header.Get(headers.NameTricksterResult), got)
	}
	if rows := decodeRows(t, got); len(rows) != 2 {
		t.Fatalf("first response has %d rows: %s", len(rows), got)
	}
	response, got = harness.query(t, "/druid/v2/sql", secondRequestBody)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusPartialHit {
		t.Fatalf("second response: code=%d header=%q body=%s", response.StatusCode, response.Header.Get(headers.NameTricksterResult), got)
	}
	if rows := decodeRows(t, got); len(rows) != 4 {
		t.Fatalf("second response has %d rows: %s", len(rows), got)
	}
	response, got = harness.query(t, "/druid/v2/sql", secondRequestBody)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusHit {
		t.Fatalf("third response: code=%d header=%q body=%s", response.StatusCode, response.Header.Get(headers.NameTricksterResult), got)
	}
	bodies, queries := capture.snapshot()
	if len(bodies) != 3 || len(queries) != 3 {
		t.Fatalf("origin calls = %d bodies / %d queries", len(bodies), len(queries))
	}
	if !strings.Contains(bodies[0], `"queryId":"first"`) ||
		!strings.Contains(bodies[1], `"queryId":"second"`) ||
		!strings.Contains(bodies[2], `"queryId":"second"`) {
		t.Fatalf("origin context was not preserved: %v", bodies)
	}
}

func TestSQLQueryHandlerArrayHeaderIntegration(t *testing.T) {
	capture := &druidSQLOriginCapture{}
	origin := httptest.NewServer(capture)
	defer origin.Close()
	harness := newDruidHandlerHarness(t, origin, http.MethodPost, "/druid/v2/sql")
	defer harness.cleanup()
	start := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	statement := func(from, to time.Time) string {
		return fmt.Sprintf("SELECT TIME_FLOOR(__time, 'PT1H') AS bucket, SUM(v) AS value, host FROM foo WHERE __time >= TIMESTAMP '%s' AND __time < TIMESTAMP '%s' GROUP BY 1, host", from.Format("2006-01-02 15:04:05"), to.Format("2006-01-02 15:04:05"))
	}
	body := fmt.Sprintf(`{"query":%q,"resultFormat":"array","header":true,"context":{"sqlTimeZone":"UTC"}}`,
		statement(start, start.Add(2*time.Hour)))
	response, got := harness.sqlQuery(t, "/druid/v2/sql", body)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusKeyMiss {
		t.Fatalf("array response: code=%d header=%q body=%s", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult), got)
	}
	var rows [][]any
	if err := json.Unmarshal(got, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || len(rows[0]) != 3 || rows[0][0] != "bucket" || rows[0][1] != "value" || rows[0][2] != "host" {
		t.Fatalf("unexpected array response: %s", got)
	}
	if bodies, _ := capture.snapshot(); len(bodies) != 1 || !strings.Contains(bodies[0], `"resultFormat":"array"`) {
		t.Fatalf("origin body did not preserve array shape: %v", bodies)
	}
}

func TestSQLQueryHandlerFallbackCanonicalizesTransientContext(t *testing.T) {
	var mu sync.Mutex
	var originBodies []string
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
		_, _ = w.Write([]byte(`[{"value":1}]`))
	}))
	defer origin.Close()
	harness := newDruidHandlerHarness(t, origin, http.MethodPost, "/druid/v2/sql")
	defer harness.cleanup()
	first := `{"query":"SELECT 1","context":{"queryId":"one"}}`
	second := `{"query":"SELECT 1","context":{"queryId":"two"}}`
	response, _ := harness.sqlQuery(t, "/druid/v2/sql", first)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusKeyMiss {
		t.Fatalf("first response: code=%d header=%q", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult))
	}
	response, _ = harness.sqlQuery(t, "/druid/v2/sql", second)
	if response.StatusCode != http.StatusOK || resultStatus(t, response) != status.StatusHit {
		t.Fatalf("fallback repeat: code=%d header=%q", response.StatusCode,
			response.Header.Get(headers.NameTricksterResult))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(originBodies) != 1 || !strings.Contains(originBodies[0], `"queryId":"one"`) {
		t.Fatalf("origin bodies = %v", originBodies)
	}
}
