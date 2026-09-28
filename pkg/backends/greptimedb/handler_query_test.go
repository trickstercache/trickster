/*
 * Copyright 2026 The Trickster Authors
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

package greptimedb

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

type httpOrigin struct {
	mu         sync.Mutex
	statements []string
	fault      string
	failStart  time.Time
}

var httpBound = regexp.MustCompile(`ts\s*(>=|<)\s*(?:TIMESTAMPTZ\s*)?'([^']+)'`)

func (o *httpOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	statement := r.URL.Query().Get("sql")
	if _, exists := r.URL.Query()["sql"]; !exists {
		statement = r.PostForm.Get("sql")
	}
	o.mu.Lock()
	o.statements = append(o.statements, statement)
	fault := o.fault
	failStart := o.failStart
	o.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Greptime-Execution-Time", "37")
	w.Header().Set("X-Greptime-Metrics", `{"cpu":5}`)
	if !strings.Contains(strings.ToLower(statement), "date_bin") || fault == "unsupported" {
		fmt.Fprint(w, `{"output":[{"records":{"schema":{"column_schemas":[{"name":"value","data_type":"String"}]},"rows":[["original"]],"total_rows":1}}],"execution_time_ms":37}`)
		return
	}
	bounds := httpBound.FindAllStringSubmatch(statement, -1)
	if len(bounds) != 2 {
		http.Error(w, "missing SQL bounds: "+statement, 400)
		return
	}
	start, e1 := time.Parse(time.RFC3339Nano, bounds[0][2])
	end, e2 := time.Parse(time.RFC3339Nano, bounds[1][2])
	if e1 != nil || e2 != nil {
		http.Error(w, "bad SQL timestamps: "+statement, 400)
		return
	}
	if !failStart.IsZero() && start.Equal(failStart) {
		http.Error(w, "fixture gap failed", http.StatusServiceUnavailable)
		return
	}
	rows := make([][]any, 0)
	if fault != "empty" {
		for current := start; current.Before(end); current = current.Add(15 * time.Minute) {
			rows = append(rows, []any{current.UnixNano(), "a", current.Unix() / 900})
		}
	}
	kind := "Int64"
	if fault == "schema" {
		kind = "Float64"
	}
	schema := []map[string]string{{"name": "time", "data_type": "TimestampNanosecond"}, {"name": "host", "data_type": "String"}, {"name": "value", "data_type": kind}}
	_ = json.NewEncoder(w).Encode(map[string]any{"output": []any{map[string]any{"records": map[string]any{
		"schema": map[string]any{"column_schemas": schema}, "rows": rows, "total_rows": len(rows),
	}}}, "execution_time_ms": 37})
}

func (o *httpOrigin) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.statements...)
}

func (o *httpOrigin) setFault(fault string) { o.mu.Lock(); o.fault = fault; o.mu.Unlock() }

type httpHarness struct {
	client    *Client
	resources *request.Resources
}

func newHTTPHarness(t *testing.T, origin http.Handler) *httpHarness {
	t.Helper()
	ts := httptest.NewServer(origin)
	t.Cleanup(ts.Close)
	placeholder, _, req, _, err := tu.NewTestInstance("", (&Client{}).DefaultPathConfigs, 200, "{}", nil, "greptimedb", "/v1/sql", "error")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(placeholder.Close)
	initial := request.GetResources(req)
	o := initial.BackendOptions
	o.OriginURL = ts.URL
	if err := o.Initialize("default"); err != nil {
		t.Fatal(err)
	}
	b, err := NewClient("default", o, nil, initial.CacheClient, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	o.HTTPClient = c.HTTPClient()
	pc := c.DefaultPathConfigs(o).Match("GET", "/v1/sql")
	return &httpHarness{c, request.NewResources(o, pc, initial.CacheConfig, initial.CacheClient, c, initial.Tracer)}
}

func (h *httpHarness) query(t *testing.T, method, statement string, extra url.Values, hdr http.Header) *httptest.ResponseRecorder {
	t.Helper()
	values := url.Values{"sql": {statement}, "db": {"public"}}
	for k, v := range extra {
		values[k] = v
	}
	path := "/v1/sql"
	var body io.Reader
	if method == "POST" {
		body = strings.NewReader(values.Encode())
	} else {
		path += "?" + values.Encode()
	}
	r := httptest.NewRequest(method, "http://trickster"+path, body)
	r.Header = hdr.Clone()
	if r.Header == nil {
		r.Header = make(http.Header)
	}
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res := h.resources
	r = request.SetResources(r, request.NewResources(res.BackendOptions, res.PathConfig, res.CacheConfig, res.CacheClient, h.client, res.Tracer))
	w := httptest.NewRecorder()
	h.client.QueryHandler(w, r)
	return w
}

func liveRange(start, end time.Time) string {
	return fmt.Sprintf("SELECT date_bin('15m', ts) AS time, host, SUM(value) AS value FROM metrics WHERE ts >= '%s' AND ts < '%s' GROUP BY 1,2 ORDER BY time,host", start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
}

func assertHTTPResult(t *testing.T, w *httptest.ResponseRecorder, engine, status string, rows int) {
	t.Helper()
	gotEngine, gotStatus := headers.ParseResultEngineStatus(w.Header().Get(headers.NameTricksterResult))
	if w.Code != 200 || gotEngine != engine || gotStatus != status {
		t.Fatalf("HTTP %d engine=%s status=%s body=%s", w.Code, gotEngine, gotStatus, w.Body.String())
	}
	if rows < 0 {
		return
	}
	var doc struct {
		Output []struct {
			Records struct {
				Rows   [][]any
				Total  int `json:"total_rows"`
				Schema struct {
					Columns []any `json:"column_schemas"`
				}
			}
		}
		Execution int `json:"execution_time_ms"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Output) != 1 || len(doc.Output[0].Records.Rows) != rows || doc.Output[0].Records.Total != rows || len(doc.Output[0].Records.Schema.Columns) != 3 {
		t.Fatalf("bad reconstructed shape: %s", w.Body.String())
	}
	if engine == "DeltaProxyCache" && (doc.Execution != 0 || w.Header().Get("X-Greptime-Execution-Time") != "0" || w.Header().Get("X-Greptime-Metrics") != "") {
		t.Fatalf("stale execution metadata: %s %v", w.Body.String(), w.Header())
	}
}

func TestHTTPSQLCacheFlow(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			origin := &httpOrigin{}
			h := newHTTPHarness(t, origin)
			start := time.Now().UTC().Truncate(15 * time.Minute).Add(-3 * time.Hour)
			first := liveRange(start, start.Add(time.Hour))
			assertHTTPResult(t, h.query(t, method, first, nil, nil), "DeltaProxyCache", "kmiss", 4)
			wide := liveRange(start.Add(-15*time.Minute), start.Add(75*time.Minute))
			assertHTTPResult(t, h.query(t, method, wide, nil, nil), "DeltaProxyCache", "phit", 6)
			assertHTTPResult(t, h.query(t, method, wide, nil, nil), "DeltaProxyCache", "hit", 6)
			assertHTTPResult(t, h.query(t, method, first, nil, nil), "DeltaProxyCache", "hit", 4)
			if queries := origin.snapshot(); len(queries) != 3 {
				t.Fatalf("expected only initial and two gap queries: %v", queries)
			}
		})
	}
}

func TestHTTPSQLCacheIdentity(t *testing.T) {
	for _, statement := range []string{"SELECT 9 AS value", liveRange(time.Now().UTC().Truncate(15*time.Minute).Add(-3*time.Hour), time.Now().UTC().Truncate(15*time.Minute).Add(-2*time.Hour))} {
		t.Run(statement, func(t *testing.T) {
			origin := &httpOrigin{}
			h := newHTTPHarness(t, origin)
			for _, test := range []struct {
				name    string
				extra   url.Values
				headers http.Header
			}{
				{"default", nil, nil},
				{"database", url.Values{"db": {"other"}}, nil},
				{"database_header", nil, http.Header{"X-Greptime-Db-Name": {"other"}}},
				{"timezone", nil, http.Header{"X-Greptime-Timezone": {"+08:00"}}},
				{"authorization", nil, http.Header{"Authorization": {"Basic Zm9vOmJhcg=="}}},
				{"greptime_auth", nil, http.Header{"X-Greptime-Auth": {"Basic YmFyOmJheg=="}}},
			} {
				t.Run(test.name, func(t *testing.T) {
					before := len(origin.snapshot())
					for i := 0; i < 2; i++ {
						w := h.query(t, "POST", statement, test.extra, test.headers)
						_, status := headers.ParseResultEngineStatus(w.Header().Get(headers.NameTricksterResult))
						want := "kmiss"
						if i > 0 {
							want = "hit"
						}
						if w.Code != 200 || status != want {
							t.Fatalf("identity %s response: %d %s %s", test.name, w.Code, status, w.Body.String())
						}
					}
					if len(origin.snapshot()) != before+1 {
						t.Fatal("identity did not fetch exactly once")
					}
				})
			}
		})
	}
}

func TestHTTPSQLFallbackFlow(t *testing.T) {
	start := time.Now().UTC().Truncate(15 * time.Minute).Add(-3 * time.Hour)
	for _, test := range []struct {
		name, statement, fault string
		extra                  url.Values
		cacheControl           string
	}{
		{"unsupported_response", liveRange(start, start.Add(time.Hour)), "unsupported", nil, ""},
		{"uncached_unsupported", liveRange(start, start.Add(time.Hour)), "unsupported", nil, "no-cache"},
		{"multiple", "SELECT 1; SELECT 2", "", nil, ""},
		{"write", "DELETE FROM metrics", "", nil, ""},
		{"volatile", "SELECT random()", "", nil, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			origin := &httpOrigin{fault: test.fault}
			h := newHTTPHarness(t, origin)
			for range 2 {
				w := h.query(t, "POST", test.statement, test.extra, http.Header{"Cache-Control": {test.cacheControl}})
				engine, _ := headers.ParseResultEngineStatus(w.Header().Get(headers.NameTricksterResult))
				if w.Code != 200 || engine == "DeltaProxyCache" || engine == "ObjectProxyCache" || !strings.Contains(w.Body.String(), "original") {
					t.Fatalf("not proxied: %s %s", engine, w.Body.String())
				}
			}
			queries := origin.snapshot()
			if queries[len(queries)-1] != test.statement {
				t.Fatalf("fallback did not restore original SQL: %v", queries)
			}
		})
	}
}

func TestHTTPSQLEmptyAndSchemaChangeFlow(t *testing.T) {
	start := time.Now().UTC().Truncate(15 * time.Minute).Add(-3 * time.Hour)
	for _, fault := range []string{"empty", "schema", "unsupported"} {
		t.Run(fault, func(t *testing.T) {
			origin := &httpOrigin{}
			if fault == "empty" {
				origin.setFault(fault)
			}
			h := newHTTPHarness(t, origin)
			statement := liveRange(start, start.Add(time.Hour))
			rows := 4
			if fault == "empty" {
				rows = 0
			}
			assertHTTPResult(t, h.query(t, "POST", statement, nil, nil), "DeltaProxyCache", "kmiss", rows)
			origin.setFault(fault)
			wide := liveRange(start.Add(-15*time.Minute), start.Add(75*time.Minute))
			w := h.query(t, "POST", wide, nil, nil)
			if fault == "empty" {
				assertHTTPResult(t, w, "DeltaProxyCache", "phit", 0)
				assertHTTPResult(t, h.query(t, "POST", wide, nil, nil), "DeltaProxyCache", "hit", 0)
			} else {
				engine, _ := headers.ParseResultEngineStatus(w.Header().Get(headers.NameTricksterResult))
				queries := origin.snapshot()
				if w.Code != 200 || engine == "DeltaProxyCache" || queries[len(queries)-1] != wide {
					t.Fatalf("schema or gap failure not proxied: %s %s %v", engine, w.Body.String(), queries)
				}
			}
		})
	}
}

func TestHTTPSQLSingleFailedGap(t *testing.T) {
	start := time.Now().UTC().Truncate(15 * time.Minute).Add(-3 * time.Hour)
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			origin := &httpOrigin{failStart: start.Add(time.Hour)}
			h := newHTTPHarness(t, origin)
			first := liveRange(start, start.Add(time.Hour))
			assertHTTPResult(t, h.query(t, method, first, nil, nil), "DeltaProxyCache", "kmiss", 4)
			wide := liveRange(start.Add(-15*time.Minute), start.Add(75*time.Minute))
			for range 2 {
				assertHTTPResult(t, h.query(t, method, wide, nil, nil), "HTTPProxy", "proxy-only", 6)
				queries := origin.snapshot()
				if queries[len(queries)-1] != wide {
					t.Fatal("a partial failure did not restore the complete original query")
				}
			}
			assertHTTPResult(t, h.query(t, method, first, nil, nil), "DeltaProxyCache", "hit", 4)
		})
	}
}

func TestHTTPSQLShardedFlow(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			origin := &httpOrigin{}
			h := newHTTPHarness(t, origin)
			h.resources.BackendOptions.DoesShard = true
			h.resources.BackendOptions.MaxShardSizePoints = 2
			start := time.Now().UTC().Truncate(15 * time.Minute).Add(-3 * time.Hour)
			statement := liveRange(start, start.Add(90*time.Minute))
			assertHTTPResult(t, h.query(t, method, statement, nil, nil), "DeltaProxyCache", "kmiss", 6)
			assertHTTPResult(t, h.query(t, method, statement, nil, nil), "DeltaProxyCache", "hit", 6)
			if queries := origin.snapshot(); len(queries) != 3 {
				t.Fatalf("expected three two-point shards: %v", queries)
			}
		})
	}
}

func TestHTTPSQLAuthenticatedGETPolicy(t *testing.T) {
	for _, shareable := range []bool{false, true} {
		t.Run(fmt.Sprint(shareable), func(t *testing.T) {
			origin := &httpOrigin{}
			h := newHTTPHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if shareable {
					w.Header().Set("Cache-Control", "public, max-age=60")
				}
				origin.ServeHTTP(w, r)
			}))
			for i := range 2 {
				status := "kmiss"
				if shareable && i == 1 {
					status = "hit"
				}
				assertHTTPResult(t, h.query(t, "GET", "SELECT 9", nil, http.Header{"Authorization": {"Basic Zm9vOmJhcg=="}}), "ObjectProxyCache", status, -1)
			}
		})
	}
}
