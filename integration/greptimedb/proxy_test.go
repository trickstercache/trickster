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

package greptimedb_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// TestProxyEnvironment compares the same origin through different transports.
// Cross-engine rounding allowances from the direct suite never apply here.
func TestProxyEnvironment(t *testing.T) {
	if os.Getenv("TRICKSTER_GREPTIMEDB_PROXY_ACCEPTANCE") != "1" {
		t.Skip("set TRICKSTER_GREPTIMEDB_PROXY_ACCEPTANCE=1 with running proxy listeners")
	}
	out := os.Getenv("GREPTIMEDB_REPORT_DIR")
	if out == "" {
		t.Fatal("GREPTIMEDB_REPORT_DIR is required")
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(out, "proxy-report.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	r := report{
		StartedAt: time.Now().UTC(), GoVersion: runtime.Version(), BuildNote: os.Getenv("GREPTIMEDB_BUILD_NOTE"),
		Hashes: map[string]string{}, Facts: map[string]any{},
	}
	t.Cleanup(func() {
		if err := writeJSON(filepath.Join(out, "proxy-report.json"), r); err != nil {
			t.Error(err)
		}
	})
	run := func(name string, fn func() error) {
		t.Run(name, func(t *testing.T) {
			c := check{Name: name, Status: "PASS"}
			if err := fn(); err != nil {
				c.Status, c.Detail = "FAIL", err.Error()
				t.Error(err)
			}
			r.Checks = append(r.Checks, c)
		})
	}
	raw, err := os.ReadFile(filepath.Join(environment, "seed-data", "seed-window.env"))
	if err != nil {
		t.Fatal(err)
	}
	seed, err := readSeed(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.Hashes["seed-window.env"] = fmt.Sprintf("%x", sha256.Sum256(raw))
	r.To = time.Unix(seed["SEED_EPOCH"], 0).UTC().Truncate(24 * time.Hour)
	r.From = r.To.Add(-48 * time.Hour)
	raw, err = os.ReadFile(filepath.Join(environment, "dashboards", "trickster-greptimedb.json"))
	if err != nil {
		t.Fatal(err)
	}
	var db dashboard
	if err = json.Unmarshal(raw, &db); err != nil {
		t.Fatal(err)
	}
	r.Hashes["greptimedb-dashboard"] = fmt.Sprintf("%x", sha256.Sum256(raw))
	g := grafanaClient{strings.TrimRight(envOr("GRAFANA_URL", "http://127.0.0.1:3000"), "/"), &http.Client{Timeout: 30 * time.Second}}
	user, password := envOr("GREPTIMEDB_USER", "grafana_ro"), envOr("GREPTIMEDB_PASSWORD", "trickster-dev-grafana")
	for _, mode := range []struct{ name, addr, uid, backend string }{
		{"terminated", envOr("GREPTIMEDB_PROXY_PG_ADDR", "127.0.0.1:8489"), envOr("GREPTIMEDB_PROXY_SQL_UID", "ds_greptimedb_trickster"), envOr("GREPTIMEDB_PROXY_BACKEND", "greptimedb1")},
		{"passthrough", os.Getenv("GREPTIMEDB_PASSTHROUGH_PG_ADDR"), os.Getenv("GREPTIMEDB_PASSTHROUGH_SQL_UID"), envOr("GREPTIMEDB_PASSTHROUGH_BACKEND", "greptimedb-pass")},
	} {
		if mode.addr == "" || mode.uid == "" {
			r.Checks = append(r.Checks, check{mode.name, "UNVERIFIED", "No passthrough listener/datasource supplied"})
			continue
		}
		run(mode.name+"_pgwire", func() error { return comparePGProxy(mode.addr, user, password) })
		run(mode.name+"_grafana_health", func() error {
			var health struct{ Status, Message string }
			if err := g.request(http.MethodGet, "/api/datasources/uid/"+url.PathEscape(mode.uid)+"/health", nil, &health); err != nil {
				return err
			}
			if health.Status != "OK" {
				return fmt.Errorf("datasource status %q: %s", health.Status, health.Message)
			}
			return nil
		})
		for _, panel := range db.Panels {
			if !((panel.ID >= 1 && panel.ID <= 6) || (panel.ID >= 20 && panel.ID <= 25)) {
				continue
			}
			cacheMode := "delta"
			if panel.ID == 5 || panel.ID == 22 || panel.ID == 23 || panel.ID == 24 {
				cacheMode = "object"
			}
			// an inclusive upper bound adds the bucket starting at the window's end, which a delta
			// answer, made of complete buckets only, leaves out
			inclusive := cacheMode == "delta" && slices.ContainsFunc(panel.Targets, hasInclusiveMacro)
			// the third scenario overlaps the first, earlier, so it can never reach past now into seeded future rows
			for scenario, shift := range []time.Duration{0, 0, -time.Hour} {
				attempt := func() error {
					before, err := sqlCacheCounts(g.client, mode.backend, cacheMode)
					if err != nil {
						return err
					}
					var docs []queryResponse
					for _, uid := range []string{envOr("GREPTIMEDB_SQL_UID", "ds_greptimedb_direct"), mode.uid} {
						doc, err := g.query(panel.Targets, uid, "grafana-postgresql-datasource", r.From.Add(shift), r.To.Add(shift), 5*time.Minute)
						if writeErr := writeJSON(filepath.Join(out, fmt.Sprintf("proxy-%s-panel-%d-%d-%s.json", mode.name, panel.ID, scenario, uid)), doc); writeErr != nil {
							return writeErr
						}
						if err != nil {
							return err
						}
						docs = append(docs, doc)
					}
					after, err := sqlCacheCounts(g.client, mode.backend, cacheMode)
					if err != nil {
						return err
					}
					status, err := cacheTransition(before, after, cacheMode, scenario)
					r.Facts[fmt.Sprintf("%s_panel_%d_%d_cache", mode.name, panel.ID, scenario)] = map[string]any{"mode": cacheMode, "status": status, "before": before, "after": after}
					if inclusive {
						docs[0] = withoutRowsAt(docs[0], r.To.Add(shift))
					}
					return errors.Join(err, compareResponses(docs[0], docs[1]))
				}
				run(fmt.Sprintf("%s_panel_%d_%d", mode.name, panel.ID, scenario), func() error {
					// the developer Trickster is shared, so a scenario that counted another client's request
					// alongside its own is run again; a query counted twice on its own fails every attempt
					var err error
					for range sharedCounterAttempts {
						if err = attempt(); !errors.Is(err, errOtherRequests) {
							return err
						}
					}
					return err
				})
			}
		}
	}
	originHTTP := strings.TrimRight(envOr("GREPTIMEDB_HTTP_URL", "http://127.0.0.1:4000"), "/")
	proxyHTTP := strings.TrimRight(envOr("GREPTIMEDB_PROXY_HTTP_URL", "http://127.0.0.1:8480/greptimedb1"), "/")
	to := time.Now().UTC().Add(-30 * time.Second).Truncate(15 * time.Second)
	from := to.Add(-time.Minute)
	r.Facts["promql_window"] = map[string]any{"from": from, "to": to, "step_seconds": 15}
	for _, query := range []struct {
		name, path string
		form       url.Values
	}{
		{"http_sql", "/v1/sql", url.Values{"db": {"public"}, "sql": {"SELECT COUNT(*) AS rows, MIN(pickup_epoch) AS first, MAX(pickup_epoch) AS last FROM trips"}}},
		{"http_promql", "/v1/prometheus/api/v1/query_range", url.Values{"query": {`up{job="prometheus"}`}, "start": {strconv.FormatInt(from.Unix(), 10)}, "end": {strconv.FormatInt(to.Unix(), 10)}, "step": {"15"}}},
	} {
		run(query.name, func() error {
			want, err := proxyHTTPPayload(originHTTP+query.path, query.form, user, password, query.name == "http_sql")
			if err != nil {
				return err
			}
			if err := writeJSON(filepath.Join(out, "origin-"+query.name+".json"), want); err != nil {
				return err
			}
			for n := range 2 {
				got, err := proxyHTTPPayload(proxyHTTP+query.path, query.form, user, password, query.name == "http_sql")
				if err != nil {
					return err
				}
				if err := writeJSON(filepath.Join(out, fmt.Sprintf("proxy-%s-%d.json", query.name, n)), got); err != nil {
					return err
				}
				if query.name == "http_promql" {
					// JSON timestamps may be spelled as 1 or 1.0. Compare their
					// exact numeric value without rounding large integers.
					want, got = normalizeHTTPNumbers(want), normalizeHTTPNumbers(got)
				}
				if !reflect.DeepEqual(want, got) {
					return fmt.Errorf("direct and proxied %s payloads differ", query.name)
				}
			}
			return nil
		})
	}
	run("grafana_promql", func() error {
		targets := []map[string]any{{"refId": "A", "expr": `up{job="prometheus"}`, "range": true, "instant": false, "interval": "15s"}}
		want, err := g.query(targets, envOr("GREPTIMEDB_PROM_UID", "ds_greptimedb_prom_direct"), "prometheus", from, to, 15*time.Second)
		if err != nil {
			return err
		}
		got, err := g.query(targets, envOr("GREPTIMEDB_PROXY_PROM_UID", "ds_greptimedb_prom_trickster"), "prometheus", from, to, 15*time.Second)
		if err != nil {
			return err
		}
		return compareResponses(want, got)
	})
	run("provider_metrics", func() error {
		endpoint := envOr("GREPTIMEDB_PROXY_METRICS_URL", "http://127.0.0.1:8481/metrics")
		resp, err := g.client.Get(endpoint)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("metrics HTTP %d", resp.StatusCode)
		}
		parser := expfmt.NewTextParser(model.UTF8Validation)
		families, err := parser.TextToMetricFamilies(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return err
		}
		var httpRequests, pgRequests float64
		for _, m := range families["trickster_proxy_requests_total"].GetMetric() {
			labels := make(map[string]string)
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["provider"] != "greptimedb" || labels["backend_name"] != envOr("GREPTIMEDB_PROXY_BACKEND", "greptimedb1") {
				continue
			}
			if labels["path"] == "query" {
				pgRequests += m.GetCounter().GetValue()
			}
		}
		// The standard ReverseProxy lane records HTTP traffic at the frontend,
		// whereas native pgwire requests use proxy counters.
		for _, m := range families["trickster_frontend_requests_total"].GetMetric() {
			labels := make(map[string]string)
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["provider"] == "greptimedb" && labels["backend_name"] == envOr("GREPTIMEDB_PROXY_BACKEND", "greptimedb1") && labels["http_status"] == "2xx" {
				httpRequests += m.GetCounter().GetValue()
			}
		}
		if httpRequests == 0 || pgRequests == 0 {
			return fmt.Errorf("missing HTTP or pgwire provider metrics: HTTP=%v pgwire=%v", httpRequests, pgRequests)
		}
		r.Facts["provider_requests"] = map[string]float64{"http": httpRequests, "pgwire": pgRequests}
		return nil
	})
}

const (
	rewriteFailuresKey    = "rewrite failures"
	sharedCounterAttempts = 3
)

var (
	errOtherRequests = errors.New("other requests were counted alongside this one")
	inclusiveMacros  = []string{"$__timeFilter(", "$__unixEpochFilter("}
)

func hasInclusiveMacro(target map[string]any) bool {
	sql, _ := target["rawSql"].(string)
	return slices.ContainsFunc(inclusiveMacros, func(macro string) bool { return strings.Contains(sql, macro) })
}

func withoutRowsAt(doc queryResponse, at time.Time) queryResponse {
	stamp := json.Number(strconv.FormatInt(at.UnixMilli(), 10))
	out := queryResponse{Results: make(map[string]queryResult, len(doc.Results))}
	for ref, result := range doc.Results {
		frames := slices.Clone(result.Frames)
		for i, f := range frames {
			col := slices.IndexFunc(f.Schema.Fields, func(fd field) bool { return fd.Type == "time" })
			if col < 0 || col >= len(f.Data.Values) {
				continue
			}
			drop := make(map[int]bool)
			for row, v := range f.Data.Values[col] {
				if v == stamp {
					drop[row] = true
				}
			}
			if len(drop) == 0 {
				continue
			}
			keep := func(columns [][]any) [][]any {
				if columns == nil {
					return nil
				}
				kept := make([][]any, len(columns))
				for c, values := range columns {
					if values == nil {
						continue
					}
					kept[c] = make([]any, 0, len(values))
					for row, v := range values {
						if !drop[row] {
							kept[c] = append(kept[c], v)
						}
					}
				}
				return kept
			}
			frames[i].Data.Values, frames[i].Data.Nanos = keep(f.Data.Values), keep(f.Data.Nanos)
		}
		result.Frames = frames
		out.Results[ref] = result
	}
	return out
}

func sqlCacheCounts(client *http.Client, backend, mode string) (map[string]float64, error) {
	resp, err := client.Get(envOr("GREPTIMEDB_PROXY_METRICS_URL", "http://127.0.0.1:8481/metrics"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics HTTP %d", resp.StatusCode)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	counts := map[string]float64{}
	for _, metric := range families["trickster_sql_query_cache_total"].GetMetric() {
		labels := map[string]string{}
		for _, label := range metric.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}
		if labels["backend_name"] == backend && labels["cache_mode"] == mode {
			counts[labels["cache_status"]] += metric.GetCounter().GetValue()
		}
	}
	// a long-lived developer Trickster may have counted failures before this run, so only new ones matter
	for _, metric := range families["trickster_sql_query_rewrite_failures_total"].GetMetric() {
		for _, label := range metric.GetLabel() {
			if label.GetName() == "backend_name" && label.GetValue() == backend {
				counts[rewriteFailuresKey] += metric.GetCounter().GetValue()
			}
		}
	}
	return counts, nil
}

func cacheTransition(before, after map[string]float64, mode string, scenario int) (string, error) {
	if after[rewriteFailuresKey] > before[rewriteFailuresKey] {
		return "", fmt.Errorf("the query's SQL rewrite failed: before=%v after=%v", before, after)
	}
	status := ""
	for name, value := range after {
		delta := value - before[name]
		if delta == 0 || name == rewriteFailuresKey {
			continue
		}
		if delta != 1 || status != "" {
			return "", fmt.Errorf("%w: before=%v after=%v", errOtherRequests, before, after)
		}
		status = name
	}
	if status != "kmiss" && status != "rmiss" && status != "hit" && status != "phit" {
		return status, fmt.Errorf("query did not take the %s cache path: %s", mode, status)
	}
	if scenario == 1 && status != "hit" {
		return status, fmt.Errorf("repeat was %s, not a hit", status)
	}
	if scenario == 2 && mode == "delta" && status != "phit" && status != "hit" {
		return status, fmt.Errorf("overlapping range was %s, not a partial hit or a warmed hit", status)
	}
	if scenario == 2 && mode == "object" && status != "kmiss" && status != "hit" {
		return status, fmt.Errorf("object query unexpectedly reported %s", status)
	}
	return status, nil
}

func TestCacheTransition(t *testing.T) {
	for _, tc := range []struct {
		mode, status string
		phase        int
		pass         bool
	}{
		{"delta", "kmiss", 0, true},
		{"delta", "hit", 1, true},
		{"delta", "phit", 2, true},
		{"delta", "kmiss", 1, false},
		{"delta", "kmiss", 2, false},
		{"delta", "", 0, false},
		{"object", "kmiss", 2, true},
		{"object", "phit", 2, false},
	} {
		_, err := cacheTransition(nil, map[string]float64{tc.status: 1}, tc.mode, tc.phase)
		if (err == nil) != tc.pass {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	if _, err := cacheTransition(map[string]float64{rewriteFailuresKey: 3},
		map[string]float64{rewriteFailuresKey: 3, "hit": 1}, "delta", 1); err != nil {
		t.Fatalf("an earlier rewrite failure failed a later query: %v", err)
	}
	if _, err := cacheTransition(nil, map[string]float64{rewriteFailuresKey: 1, "hit": 1}, "delta", 1); err == nil {
		t.Fatal("accepted a query whose rewrite failed")
	}
	for _, counts := range []map[string]float64{{"hit": 2}, {"hit": 1, "kmiss": 1}, {"hit": -1}} {
		if _, err := cacheTransition(nil, counts, "delta", 0); !errors.Is(err, errOtherRequests) {
			t.Fatalf("%v: %v", counts, err)
		}
	}
	if _, err := cacheTransition(nil, nil, "delta", 0); err == nil || errors.Is(err, errOtherRequests) {
		t.Fatalf("a query no cache counted must fail without a retry: %v", err)
	}
}

func TestWithoutRowsAt(t *testing.T) {
	end := time.Unix(1_790_380_800, 0)
	var f frame
	f.Schema.Fields = []field{{Name: "Time", Type: "time"}, {Name: "trips", Type: "number"}}
	f.Data.Values = [][]any{
		{json.Number("1790380500000"), json.Number("1790380800000")},
		{json.Number("51"), json.Number("1")},
	}
	doc := queryResponse{Results: map[string]queryResult{"A": {Frames: []frame{f}}}}
	got := withoutRowsAt(doc, end).Results["A"].Frames[0]
	if want := [][]any{{json.Number("1790380500000")}, {json.Number("51")}}; !reflect.DeepEqual(got.Data.Values, want) {
		t.Fatalf("values = %v, want %v", got.Data.Values, want)
	}
	if got.Data.Nanos != nil || len(doc.Results["A"].Frames[0].Data.Values[0]) != 2 {
		t.Fatal("the source frame changed, or absent nanoseconds appeared")
	}
	if kept := withoutRowsAt(doc, end.Add(time.Hour)); !reflect.DeepEqual(kept, doc) {
		t.Fatal("a response without rows at the instant changed")
	}
}

func comparePGProxy(address, user, password string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	connect := func(addr, pass string) (*pgconn.PgConn, error) {
		u := url.URL{Scheme: "postgres", Host: addr, Path: "/" + envOr("GREPTIMEDB_DATABASE", "public"), User: url.UserPassword(user, pass), RawQuery: "sslmode=disable&connect_timeout=10"}
		return pgconn.Connect(ctx, u.String())
	}
	direct, err := connect(envOr("GREPTIMEDB_PG_ADDR", "127.0.0.1:4003"), password)
	if err != nil {
		return err
	}
	defer direct.Close(ctx)
	proxy, err := connect(address, password)
	if err != nil {
		return err
	}
	defer proxy.Close(ctx)
	bad, err := connect(address, password+"-wrong")
	if err == nil {
		_ = bad.Close(ctx)
		return fmt.Errorf("proxy accepted a wrong password")
	}
	for _, sql := range []string{"", "-- ping", pgTypeQuery, "SELECT 1 AS a; SELECT 2 AS b", "SELECT COUNT(*) FROM trips"} {
		want, err := direct.Exec(ctx, sql).ReadAll()
		if err != nil {
			return err
		}
		got, err := proxy.Exec(ctx, sql).ReadAll()
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(want, got) {
			return fmt.Errorf("pgwire results differ for %q", sql)
		}
	}
	want := direct.ExecParams(ctx, "SELECT $1::BIGINT AS answer", [][]byte{[]byte("42")}, []uint32{20}, nil, nil).Read()
	got := proxy.ExecParams(ctx, "SELECT $1::BIGINT AS answer", [][]byte{[]byte("42")}, []uint32{20}, nil, nil).Read()
	if want.Err != nil || got.Err != nil || !reflect.DeepEqual(want, got) {
		return fmt.Errorf("extended-protocol responses differ: direct=%v proxy=%v", want.Err, got.Err)
	}
	_, wantErr := direct.Exec(ctx, "SELECT __missing_proxy_column FROM trips").ReadAll()
	_, gotErr := proxy.Exec(ctx, "SELECT __missing_proxy_column FROM trips").ReadAll()
	var wantPG, gotPG *pgconn.PgError
	if !errors.As(wantErr, &wantPG) || !errors.As(gotErr, &gotPG) || wantPG.Code != gotPG.Code {
		return fmt.Errorf("error SQLSTATE differs")
	}
	return proxy.Ping(ctx)
}

func proxyHTTPPayload(endpoint string, form url.Values, user, password string, sql bool) (any, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(user, password)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, req.URL.Path)
	}
	d := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	d.UseNumber()
	var body map[string]any
	if err := d.Decode(&body); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("trailing HTTP response data")
	}
	if sql {
		output, ok := body["output"].([]any)
		if !ok || len(output) == 0 || body["error"] != nil {
			return nil, fmt.Errorf("unsuccessful SQL output")
		}
		for _, item := range output {
			result, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid SQL result")
			}
			records, ok := result["records"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("missing SQL records")
			}
			rows, ok := records["rows"].([]any)
			if !ok || len(rows) == 0 {
				return nil, fmt.Errorf("empty SQL records")
			}
		}
		// Execution duration is not query data and varies between requests.
		return output, nil
	}
	data, ok := body["data"].(map[string]any)
	if body["status"] != "success" || !ok {
		return nil, fmt.Errorf("unsuccessful PromQL output")
	}
	result, ok := data["result"].([]any)
	if !ok || len(result) == 0 {
		return nil, fmt.Errorf("empty PromQL output")
	}
	return data, nil
}

func TestProxyHTTPPayloadValidation(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		sql, valid bool
	}{
		{"SQL data", `{"output":[{"records":{"rows":[[9007199254740993]]}}],"execution_time_ms":3}`, true, true},
		{"SQL error", `{"error":"bad query","code":1000}`, true, false},
		{"empty SQL", `{"output":[]}`, true, false},
		{"empty records", `{"output":[{"records":{"rows":[]}}]}`, true, false},
		{"invalid records", `{"output":[{}]}`, true, false},
		{"PromQL data", `{"status":"success","data":{"resultType":"matrix","result":[{"values":[[1,"1"]]}]}}`, false, true},
		{"empty PromQL", `{"status":"success","data":{"result":[]}}`, false, false},
		{"PromQL error", `{"status":"error","error":"bad query"}`, false, false},
		{"trailing JSON", `{"output":[]} {}`, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Error("expected POST")
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			_, err := proxyHTTPPayload(server.URL, url.Values{"sql": {"SELECT 1"}}, "user", "password", tt.sql)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%t, err=%v", tt.valid, err)
			}
		})
	}
}
