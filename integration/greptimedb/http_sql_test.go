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
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

type httpSQLResponse struct {
	Status  int             `json:"status"`
	Headers http.Header     `json:"headers"`
	Body    json.RawMessage `json:"body"`
	Text    string          `json:"text,omitempty"`
}

func fetchHTTPSQL(endpoint, method string, values url.Values, extra http.Header) (httpSQLResponse, error) {
	var out httpSQLResponse
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(values.Encode())
	} else {
		endpoint += "?" + values.Encode()
	}
	r, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return out, err
	}
	r.Header = extra.Clone()
	if r.Header == nil {
		r.Header = make(http.Header)
	}
	r.SetBasicAuth(envOr("GREPTIMEDB_USER", "grafana_ro"), envOr("GREPTIMEDB_PASSWORD", "trickster-dev-grafana"))
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(r)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return out, err
	}
	out.Status, out.Headers = resp.StatusCode, resp.Header.Clone()
	if json.Valid(raw) {
		out.Body = raw
	} else {
		out.Text = string(raw)
	}
	return out, nil
}

func normalizeHTTPNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		if r, ok := new(big.Rat).SetString(string(v)); ok {
			return struct{ Number string }{r.RatString()}
		}
	case []any:
		for i, x := range v {
			v[i] = normalizeHTTPNumbers(x)
		}
	case map[string]any:
		for k, x := range v {
			v[k] = normalizeHTTPNumbers(x)
		}
	}
	return value
}

func compareHTTPSQL(left, right httpSQLResponse) error {
	if left.Status != right.Status {
		return fmt.Errorf("origin status %d, proxy status %d", left.Status, right.Status)
	}
	if left.Body == nil || right.Body == nil {
		if !bytes.Equal(left.Body, right.Body) || left.Text != right.Text {
			return fmt.Errorf("HTTP text response differs")
		}
		return nil
	}
	var l, r map[string]any
	for _, entry := range []struct {
		body []byte
		out  *map[string]any
	}{{left.Body, &l}, {right.Body, &r}} {
		d := json.NewDecoder(bytes.NewReader(entry.body))
		d.UseNumber()
		if err := d.Decode(entry.out); err != nil {
			return err
		}
		delete(*entry.out, "execution_time_ms")
	}
	if !reflect.DeepEqual(normalizeHTTPNumbers(l), normalizeHTTPNumbers(r)) {
		return fmt.Errorf("typed HTTP SQL result differs: origin=%s proxy=%s", left.Body, right.Body)
	}
	return nil
}

// TestHTTPSQLCacheEnvironment is read-only and requires the seeded developer
// database plus the built Trickster HTTP listener. Every result is retained.
func TestHTTPSQLCacheEnvironment(t *testing.T) {
	if os.Getenv("TRICKSTER_GREPTIMEDB_HTTP_ACCEPTANCE") != "1" {
		t.Skip("set TRICKSTER_GREPTIMEDB_HTTP_ACCEPTANCE=1 with running HTTP listeners")
	}
	out := os.Getenv("GREPTIMEDB_REPORT_DIR")
	if out == "" {
		t.Fatal("GREPTIMEDB_REPORT_DIR is required")
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(out, "http-sql-report.json")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	r := report{StartedAt: time.Now().UTC(), GoVersion: runtime.Version(), BuildNote: os.Getenv("GREPTIMEDB_BUILD_NOTE"), Hashes: map[string]string{}, Facts: map[string]any{}}
	t.Cleanup(func() {
		if err := writeJSON(file, r); err != nil {
			t.Error(err)
		}
	})
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
	origin := strings.TrimRight(envOr("GREPTIMEDB_HTTP_URL", "http://127.0.0.1:4000"), "/") + "/v1/sql"
	proxy := strings.TrimRight(envOr("GREPTIMEDB_PROXY_HTTP_URL", "http://127.0.0.1:8480/greptimedb1"), "/") + "/v1/sql"
	nonce := fmt.Sprint(time.Now().UnixNano())
	run := func(name, method, statement string, extra url.Values, hdr http.Header, engine, status string) {
		t.Run(name, func(t *testing.T) {
			c := check{Name: name, Status: "PASS"}
			defer func() {
				if t.Failed() {
					c.Status = "FAIL"
				}
				r.Checks = append(r.Checks, c)
			}()
			values := url.Values{"sql": {statement}, "db": {"public"}}
			for k, v := range extra {
				values[k] = v
			}
			want, err := fetchHTTPSQL(origin, method, values, hdr)
			if err != nil {
				t.Fatal(err)
			}
			got, err := fetchHTTPSQL(proxy, method, values, hdr)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(filepath.Join(out, name+".json"), map[string]any{"query": statement, "origin": want, "proxy": got}); err != nil {
				t.Fatal(err)
			}
			if want.Status != http.StatusOK {
				t.Fatalf("valid fixture query failed at origin: %d %s %s", want.Status, want.Body, want.Text)
			}
			if err := compareHTTPSQL(want, got); err != nil {
				c.Detail = err.Error()
				t.Error(err)
			}
			actualEngine, actualStatus := headers.ParseResultEngineStatus(got.Headers.Get(headers.NameTricksterResult))
			if engine != "" && (actualEngine != engine || actualStatus != status) {
				t.Errorf("cache path %s/%s, want %s/%s", actualEngine, actualStatus, engine, status)
			}
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, kind := range []string{"date_bin", "interval", "epoch", "empty"} {
			prefix := strings.ToLower(method) + "_" + kind
			statement := func(from, to time.Time) string {
				bucket := "date_bin('15m', pickup_datetime)"
				if kind == "interval" {
					bucket = "date_bin(INTERVAL '15 minutes', pickup_datetime)"
				}
				predicate := fmt.Sprintf("pickup_datetime >= '%s' AND pickup_datetime < '%s'", from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
				if kind == "epoch" {
					bucket = "floor(pickup_epoch / 900) * 900"
					predicate = fmt.Sprintf("pickup_epoch >= %d AND pickup_epoch < %d", from.Unix(), to.Unix())
				}
				if kind == "empty" {
					predicate += " AND cab_type = 'missing_http_fixture'"
				}
				return fmt.Sprintf("SELECT %s AS http_%s_%s, cab_type, count(*) AS trips, count(*) + 9007199254740993 AS exact FROM trips WHERE %s GROUP BY 1,2 ORDER BY 1 DESC,2", bucket, prefix, nonce, predicate)
			}
			first := statement(r.From.Add(time.Hour), r.To.Add(-time.Hour))
			wide := statement(r.From, r.To)
			run(prefix+"_miss", method, first, nil, nil, "DeltaProxyCache", "kmiss")
			run(prefix+"_hit", method, first, nil, nil, "DeltaProxyCache", "hit")
			run(prefix+"_partial", method, wide, nil, nil, "DeltaProxyCache", "phit")
			run(prefix+"_wide_hit", method, wide, nil, nil, "DeltaProxyCache", "hit")
			if kind == "date_bin" {
				for _, tz := range []struct{ name, value string }{{"utc", "UTC"}, {"offset", "+08:00"}} {
					hdr := http.Header{"X-Greptime-Timezone": {tz.value}}
					run(prefix+"_"+tz.name+"_miss", method, wide, nil, hdr, "DeltaProxyCache", "kmiss")
					run(prefix+"_"+tz.name+"_hit", method, wide, nil, hdr, "DeltaProxyCache", "hit")
				}
				for _, shape := range []struct {
					name   string
					params url.Values
				}{{"limit", url.Values{"limit": {"1"}}}, {"csv", url.Values{"format": {"csv"}}}} {
					run(prefix+"_"+shape.name+"_miss", method, wide, shape.params, nil, "ObjectProxyCache", "kmiss")
					status := "hit"
					if method == http.MethodGet {
						// GreptimeDB does not mark authenticated GET responses shareable.
						status = "kmiss"
					}
					run(prefix+"_"+shape.name+"_repeat", method, wide, shape.params, nil, "ObjectProxyCache", status)
				}
				// Exact SQL semantics include both partial edge buckets.
				unaligned := statement(r.From.Add(time.Second), r.To.Add(-time.Second))
				run(prefix+"_unaligned", method, unaligned, nil, nil, "ObjectProxyCache", "kmiss")
				inclusive := strings.Replace(wide, "pickup_datetime <", "pickup_datetime <=", 1)
				run(prefix+"_inclusive_upper", method, inclusive, nil, nil, "ObjectProxyCache", "kmiss")
				floating := strings.Replace(wide, "count(*) AS trips", "avg(total_amount) AS trips", 1)
				run(prefix+"_float_miss", method, floating, nil, nil, "DeltaProxyCache", "kmiss")
				run(prefix+"_float_hit", method, floating, nil, nil, "DeltaProxyCache", "hit")
				nonfinite := strings.Replace(wide, "count(*) AS trips", "max(CAST('NaN' AS DOUBLE)) AS trips", 1)
				nonfinite = strings.Replace(nonfinite, "ORDER BY 1 DESC,2", "ORDER BY 3 DESC NULLS LAST,1,2", 1)
				run(prefix+"_nonfinite_order", method, nonfinite, nil, nil, "HTTPProxy", "proxy-only")
				valueless := strings.Replace(wide, ", count(*) AS trips, count(*) + 9007199254740993 AS exact", "", 1)
				run(prefix+"_valueless", method, valueless, nil, nil, "HTTPProxy", "proxy-only")
			}
		}
	}
	run("multiple", "POST", "SELECT 1; SELECT 2", nil, nil, "HTTPProxy", "proxy-only")
	run("metadata", "POST", "SHOW TABLES", nil, nil, "HTTPProxy", "proxy-only")
}

func TestCompareHTTPSQLNumbers(t *testing.T) {
	makeResponse := func(value string) httpSQLResponse {
		return httpSQLResponse{Status: 200, Body: json.RawMessage(`{"output":[` + value + `],"execution_time_ms":1}`)}
	}
	if err := compareHTTPSQL(makeResponse("1e3"), makeResponse("1000")); err != nil {
		t.Fatal(err)
	}
	if err := compareHTTPSQL(makeResponse("9007199254740993"), makeResponse("9007199254740992")); err == nil {
		t.Fatal("integer precision loss was hidden")
	}
	if err := compareHTTPSQL(makeResponse("7"), makeResponse(`"7"`)); err == nil {
		t.Fatal("numeric and string values were conflated")
	}
}
