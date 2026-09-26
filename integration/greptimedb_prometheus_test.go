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

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const greptimePromPath = "/v1/prometheus/api/v1/"

type greptimePromResponse struct {
	Code   int             `json:"http_status"`
	Result string          `json:"trickster_result,omitempty"`
	Body   json.RawMessage `json:"body"`
	Text   string          `json:"text,omitempty"`
}

type greptimePromFixture struct {
	origin, proxy, dir string
	client             *http.Client
	start              time.Time
	databases          []string
	sequence           int
}

func (f *greptimePromFixture) fetch(t *testing.T, base, method, endpoint string, query, form url.Values, hdr http.Header) greptimePromResponse {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r, err := http.NewRequest(method, base+endpoint+"?"+query.Encode(), body)
	require.NoError(t, err)
	if hdr != nil {
		r.Header = hdr.Clone()
	}
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.SetBasicAuth("grafana_ro", "trickster-dev-grafana")
	resp, err := f.client.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	require.NoError(t, err)
	out := greptimePromResponse{Code: resp.StatusCode, Result: resp.Header.Get(headers.NameTricksterResult)}
	if json.Valid(raw) {
		out.Body = raw
	} else {
		out.Text = string(raw)
	}
	f.sequence++
	evidence, err := json.MarshalIndent(map[string]any{
		"test": t.Name(), "method": method, "url": r.URL.String(), "form": form, "response": out,
	}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("%03d.json", f.sequence)), evidence, 0o600))
	return out
}

func (f *greptimePromFixture) sql(t *testing.T, db, stmt string) {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, f.origin+"/v1/sql?db="+url.QueryEscape(db), strings.NewReader(url.Values{"sql": {stmt}}.Encode()))
	require.NoError(t, err)
	r.SetBasicAuth("seeder", "trickster-dev-seed")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.client.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, "%s: %s", stmt, raw)
	var result struct {
		Code   int
		Error  string
		Output []json.RawMessage
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Zero(t, result.Code, "%s: %s", stmt, raw)
	require.Empty(t, result.Error, "%s: %s", stmt, raw)
	require.NotEmpty(t, result.Output, "%s: %s", stmt, raw)
}

// This opt-in test writes only its unique temporary databases. It starts its own
// Trickster daemon on reserved ports and removes fixtures after stopping it.
func newGreptimePromFixture(t *testing.T) *greptimePromFixture {
	t.Helper()
	if os.Getenv("TRICKSTER_GREPTIMEDB_PROMQL_ACCEPTANCE") != "1" {
		t.Skip("set TRICKSTER_GREPTIMEDB_PROMQL_ACCEPTANCE=1 with isolated developer GreptimeDB")
	}
	origin := os.Getenv("GREPTIMEDB_HTTP_URL")
	if origin == "" {
		origin = "http://127.0.0.1:4000"
	}
	root := os.Getenv("GREPTIMEDB_REPORT_DIR")
	require.NotEmpty(t, root, "GREPTIMEDB_REPORT_DIR is required")
	require.NoError(t, os.MkdirAll(root, 0o700))
	dir, err := os.MkdirTemp(root, "promql-")
	require.NoError(t, err)
	f := &greptimePromFixture{origin: strings.TrimRight(origin, "/"), dir: dir, client: &http.Client{Timeout: 30 * time.Second}, start: time.Now().UTC().Add(-5 * time.Minute).Truncate(15 * time.Second).Add(125 * time.Millisecond)}
	t.Logf("PromQL evidence: %s", dir)
	prefix := "trickster_promql_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	for i, suffix := range []string{"a", "b", "all"} {
		db := prefix + "_" + suffix
		f.sql(t, "public", "CREATE DATABASE "+db)
		f.databases = append(f.databases, db)
		t.Cleanup(func() { f.sql(t, "public", "DROP DATABASE "+db) })
		f.sql(t, db, "CREATE TABLE fixture (greptime_timestamp TIMESTAMP(3) TIME INDEX, greptime_value DOUBLE, host STRING, PRIMARY KEY(host))")
		var values []string
		for point := 0; point < 20; point++ {
			for host, value := range []int{1, 3, 10} {
				if (i == 0 && host == 2) || (i == 1 && host != 2) {
					continue
				}
				values = append(values, fmt.Sprintf("(%d, %d, 'h%d')", f.start.Add(time.Duration(point)*15*time.Second).UnixMilli(), value, host))
			}
		}
		f.sql(t, db, "INSERT INTO fixture VALUES "+strings.Join(values, ","))
	}
	ports, release := portutil.Reserve(t, 3)
	rewriters := map[string]any{}
	backends := map[string]any{}
	for _, name := range []string{"direct", "a", "b"} {
		backend := map[string]any{
			"provider": "greptimedb", "origin_url": f.origin, "cache_name": "memory", "fast_forward_disable": true,
			"healthcheck": map[string]any{"path": "/health", "query": "", "interval": "100ms", "timeout": "2s", "failure_threshold": 1, "recovery_threshold": 1},
		}
		if name != "direct" {
			index := 0
			if name == "b" {
				index = 1
			}
			rewriters[name] = map[string]any{"instructions": [][]string{{"param", "set", "db", f.databases[index]}}}
			backend["req_rewriter_name"] = name
			backend["prometheus"] = map[string]any{"labels": map[string]string{"replica": name}}
		}
		backends[name] = backend
	}
	backends["merged"] = map[string]any{"provider": "alb", "alb": map[string]any{"mechanism": "tsm", "output_format": "greptimedb", "pool": []string{"a", "b"}}}
	cfg := map[string]any{
		"listeners": map[string]any{"default": map[string]any{"port": ports[0]}, "metrics": map[string]any{"port": ports[1]}, "mgmt": map[string]any{"port": ports[2]}},
		"logging":   map[string]any{"log_level": "error"}, "caches": map[string]any{"memory": map[string]string{"provider": "memory"}},
		"request_rewriters": rewriters, "backends": backends,
	}
	raw, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	path := filepath.Join(dir, "trickster.yaml")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	release()
	runTrickster(t, context.Background(), "-config", path)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", ports[1]))
	f.proxy = fmt.Sprintf("http://127.0.0.1:%d", ports[0])
	return f
}

// Compare numeric sample values rather than their JSON spelling (2 vs 2.0).
// Series order is insignificant; point order, labels and timestamps are exact.
func greptimePromData(t *testing.T, response greptimePromResponse) map[string]any {
	t.Helper()
	require.Equal(t, 200, response.Code, "%s", response.Body)
	var doc map[string]any
	d := json.NewDecoder(bytes.NewReader(response.Body))
	d.UseNumber()
	require.NoError(t, d.Decode(&doc))
	require.Equal(t, "success", doc["status"], "%s", response.Body)
	data, ok := doc["data"].(map[string]any)
	require.True(t, ok, "%s", response.Body)
	results, ok := data["result"].([]any)
	require.True(t, ok, "%s", response.Body)
	for _, result := range results {
		series := result.(map[string]any)
		points, _ := series["values"].([]any)
		if point, ok := series["value"].([]any); ok {
			points = []any{point}
		}
		for _, raw := range points {
			point := raw.([]any)
			require.Len(t, point, 2)
			value, err := strconv.ParseFloat(point[1].(string), 64)
			require.NoError(t, err)
			if !math.IsNaN(value) {
				point[1] = value
			}
		}
	}
	slices.SortFunc(results, func(a, b any) int {
		la, _ := json.Marshal(a.(map[string]any)["metric"])
		lb, _ := json.Marshal(b.(map[string]any)["metric"])
		return bytes.Compare(la, lb)
	})
	return doc
}

func TestGreptimeDBPrometheus(t *testing.T) {
	f := newGreptimePromFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			for _, step := range []time.Duration{15 * time.Second, 500 * time.Millisecond} {
				t.Run("cache_"+step.String(), func(t *testing.T) {
					v := url.Values{"query": {"sum(fixture)"}, "db": {f.databases[2]}, "start": {f.start.Format(time.RFC3339Nano)}, "step": {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)}}
					for i, status := range []string{"kmiss", "hit", "phit", "hit"} {
						points := 3
						if i >= 2 {
							points = 5
						}
						v.Set("end", f.start.Add(time.Duration(points-1)*step).Format(time.RFC3339Nano))
						query, form := v, url.Values(nil)
						if method == http.MethodPost {
							query = url.Values{"db": v["db"]}
							form = v
						}
						origin := f.fetch(t, f.origin, method, greptimePromPath+"query_range", query, form, nil)
						proxy := f.fetch(t, f.proxy+"/direct", method, greptimePromPath+"query_range", query, form, nil)
						require.Equal(t, greptimePromData(t, origin), greptimePromData(t, proxy))
						engine, got := headers.ParseResultEngineStatus(proxy.Result)
						require.Equal(t, "DeltaProxyCache", engine)
						require.Equal(t, status, got)
						var matrix struct {
							Data struct{ Result []struct{ Values [][]any } }
						}
						require.NoError(t, json.Unmarshal(proxy.Body, &matrix))
						require.Len(t, matrix.Data.Result, 1)
						require.Len(t, matrix.Data.Result[0].Values, points)
						for n, point := range matrix.Data.Result[0].Values {
							require.Equal(t, float64(f.start.Add(time.Duration(n)*step).UnixMilli())/1000, point[0])
							value, err := strconv.ParseFloat(point[1].(string), 64)
							require.NoError(t, err)
							require.Equal(t, float64(14), value)
						}
					}
				})
			}
			for _, endpoint := range []string{"query", "query_range"} {
				for _, expression := range []string{"sum(fixture)", "count(fixture)", "avg(fixture)", "min(fixture)", "max(fixture)", "group(fixture)", "quantile(0.5, fixture)", "quantile by (host) (0.5, fixture)", "topk(2, fixture)", "bottomk(2, fixture)", "sort(count(fixture) or vector(0))", "sum(fixture) / 2", "avg by (host) (fixture)"} {
					t.Run("merge_"+endpoint+"_"+expression, func(t *testing.T) {
						v := url.Values{"query": {expression}, "db": {f.databases[2]}}
						if endpoint == "query" {
							v.Set("time", f.start.Add(30*time.Second).Format(time.RFC3339Nano))
						} else {
							v.Set("start", f.start.Format(time.RFC3339Nano))
							v.Set("end", f.start.Add(30*time.Second).Format(time.RFC3339Nano))
							v.Set("step", "15")
						}
						query, form := v, url.Values(nil)
						if method == http.MethodPost {
							query = url.Values{"db": v["db"]}
							form = v
						}
						origin := f.fetch(t, f.origin, method, greptimePromPath+endpoint, query, form, nil)
						proxy := f.fetch(t, f.proxy+"/merged", method, greptimePromPath+endpoint, query, form, nil)
						want := greptimePromData(t, origin)
						if endpoint == "query_range" && strings.HasPrefix(expression, "sort(") {
							// Shared TSM finalization adds this exact advisory. Other
							// warnings remain part of the strict comparison.
							want["warnings"] = []any{"PromQL warning: sort is ineffective for range queries since results are always ordered by labels"}
						}
						require.Equal(t, want, greptimePromData(t, proxy), "origin=%s proxy=%s", origin.Body, proxy.Body)
					})
				}
			}
		})
	}
	for _, useHeader := range []bool{false, true} {
		for i, db := range f.databases[:2] {
			t.Run(fmt.Sprintf("database_%t_%d", useHeader, i), func(t *testing.T) {
				v := url.Values{"query": {"sum(fixture)"}, "start": {f.start.Format(time.RFC3339Nano)}, "end": {f.start.Add(30 * time.Second).Format(time.RFC3339Nano)}, "step": {"15"}}
				hdr := make(http.Header)
				if useHeader {
					hdr.Set("X-Greptime-Db-Name", db)
				} else {
					v.Set("db", db)
				}
				origin := f.fetch(t, f.origin, http.MethodGet, greptimePromPath+"query_range", v, nil, hdr)
				for range 2 {
					proxy := f.fetch(t, f.proxy+"/direct", http.MethodGet, greptimePromPath+"query_range", v, nil, hdr)
					require.Equal(t, greptimePromData(t, origin), greptimePromData(t, proxy))
				}
			})
		}
	}
	t.Run("url_form_precedence", func(t *testing.T) {
		v := url.Values{"query": {"avg(fixture)"}, "db": {f.databases[2]}, "time": {f.start.Add(30 * time.Second).Format(time.RFC3339Nano)}}
		form := maps.Clone(v)
		form.Set("query", "min(fixture)")
		for _, backend := range []string{"direct", "merged"} {
			origin := f.fetch(t, f.origin, http.MethodPost, greptimePromPath+"query", v, form, nil)
			proxy := f.fetch(t, f.proxy+"/"+backend, http.MethodPost, greptimePromPath+"query", v, form, nil)
			require.Equal(t, greptimePromData(t, origin), greptimePromData(t, proxy))
		}
	})
	for _, lookback := range []string{"1s", "1m"} {
		t.Run("lookback_"+lookback, func(t *testing.T) {
			v := url.Values{"db": {f.databases[2]}, "query": {"fixture"}, "start": {f.start.Add(5 * time.Second).Format(time.RFC3339Nano)}, "end": {f.start.Add(35 * time.Second).Format(time.RFC3339Nano)}, "step": {"15"}, "lookback": {lookback}}
			origin := f.fetch(t, f.origin, "GET", greptimePromPath+"query_range", v, nil, nil)
			for range 2 {
				proxy := f.fetch(t, f.proxy+"/direct", "GET", greptimePromPath+"query_range", v, nil, nil)
				require.Equal(t, greptimePromData(t, origin), greptimePromData(t, proxy))
			}
		})
	}
	for _, endpoint := range []string{"series", "labels", "label/host/values"} {
		t.Run(endpoint, func(t *testing.T) {
			v := url.Values{"db": {f.databases[2]}, "match[]": {"fixture"}, "start": {f.start.Format(time.RFC3339Nano)}, "end": {f.start.Add(30 * time.Second).Format(time.RFC3339Nano)}}
			origin := f.fetch(t, f.origin, "GET", greptimePromPath+endpoint, v, nil, nil)
			proxy := f.fetch(t, f.proxy+"/direct", "GET", greptimePromPath+endpoint, v, nil, nil)
			require.Equal(t, 200, origin.Code)
			require.Equal(t, origin.Code, proxy.Code)
			var a, b struct {
				Status string
				Data   []any
			}
			require.NoError(t, json.Unmarshal(origin.Body, &a))
			require.NoError(t, json.Unmarshal(proxy.Body, &b))
			require.Equal(t, "success", a.Status)
			require.Equal(t, a.Status, b.Status)
			require.ElementsMatch(t, a.Data, b.Data)
		})
	}
	for _, query := range []string{`fixture{host="missing"}`, "sum("} {
		t.Run("empty_or_error_"+query, func(t *testing.T) {
			v := url.Values{"db": {f.databases[2]}, "query": {query}, "start": {f.start.Format(time.RFC3339Nano)}, "end": {f.start.Add(30 * time.Second).Format(time.RFC3339Nano)}, "step": {"15"}}
			origin := f.fetch(t, f.origin, "GET", greptimePromPath+"query_range", v, nil, nil)
			proxy := f.fetch(t, f.proxy+"/direct", "GET", greptimePromPath+"query_range", v, nil, nil)
			require.Equal(t, origin.Code, proxy.Code)
			require.JSONEq(t, string(origin.Body), string(proxy.Body))
		})
	}
	t.Run("count_values_origin_limitation", func(t *testing.T) {
		for _, endpoint := range []string{"query", "query_range"} {
			v := url.Values{"db": {f.databases[2]}, "query": {`count_values("value", fixture)`}, "start": {f.start.Format(time.RFC3339Nano)}, "end": {f.start.Add(30 * time.Second).Format(time.RFC3339Nano)}, "step": {"15"}, "time": {f.start.Format(time.RFC3339Nano)}}
			origin := f.fetch(t, f.origin, "GET", greptimePromPath+endpoint, v, nil, nil)
			for range 2 {
				proxy := f.fetch(t, f.proxy+"/direct", "GET", greptimePromPath+endpoint, v, nil, nil)
				require.Equal(t, origin.Code, proxy.Code)
				require.JSONEq(t, string(origin.Body), string(proxy.Body))
				engine, _ := headers.ParseResultEngineStatus(proxy.Result)
				require.Equal(t, "HTTPProxy", engine)
			}
			merged := f.fetch(t, f.proxy+"/merged", "GET", greptimePromPath+endpoint, v, nil, nil)
			require.Equal(t, http.StatusBadGateway, merged.Code, "must not merge malformed upstream series")
		}
	})
}
