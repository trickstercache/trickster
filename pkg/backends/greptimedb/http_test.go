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
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const httpSQL = "SELECT date_bin('15m', ts) AS time, host, SUM(value) AS value FROM metrics WHERE ts >= '2024-01-01T00:00:00Z' AND ts < '2024-01-01T01:00:00Z' GROUP BY 1,2 ORDER BY time,host"

func TestHTTPSQLProvider(t *testing.T) {
	o := bo.New()
	o.Provider, o.OriginURL = "greptimedb", "http://localhost:4000"
	if err := o.Initialize("greptime"); err != nil {
		t.Fatal(err)
	}
	b, err := NewClient(o.Name, o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := b.(backends.TimeseriesBackend)
	if !ok {
		t.Fatal("GreptimeDB HTTP SQL does not implement the timeseries cache contract")
	}
	r := httptest.NewRequest("GET", "/v1/sql?"+url.Values{"sql": {httpSQL}, "db": {"public"}}.Encode(), nil)
	trq, ro, canOPC, err := c.ParseTimeRangeQuery(r)
	if err != nil || trq == nil || ro == nil || !canOPC || trq.Step != 15*time.Minute {
		t.Fatalf("HTTP SQL was not delta analyzed: query=%+v options=%+v object=%v err=%v", trq, ro, canOPC, err)
	}
	if c.Modeler() == nil || !ro.FastForwardDisable || o.FastForwardDisable {
		t.Fatal("missing modeler or disabled SQL fast-forward guard")
	}
}

func TestHTTPSQLRequestModes(t *testing.T) {
	for _, test := range []struct {
		name, statement, params string
		object, delta           bool
	}{
		{"delta", httpSQL, "", true, true},
		{"partial_lower", strings.Replace(httpSQL, "00:00:00Z", "00:00:01Z", 1), "", true, false},
		{"partial_upper", strings.Replace(httpSQL, "01:00:00Z", "00:59:59Z", 1), "", true, false},
		{"inclusive_upper", strings.Replace(httpSQL, "ts <", "ts <=", 1), "", true, false},
		{"complete_inclusive_upper", strings.Replace(strings.Replace(httpSQL, "ts <", "ts <=", 1), "01:00:00Z", "00:59:59.999999999Z", 1), "", true, true},
		{"count", "SELECT COUNT(*) FROM metrics", "", true, false},
		{"limit", httpSQL, "&limit=1", true, false},
		{"csv", httpSQL, "&format=csv", true, false},
		{"unknown_format", httpSQL, "&format=future", true, false},
		{"unknown_parameter", httpSQL, "&future=1", true, false},
		{"format_case", httpSQL, "&format=GREPTIMEDB_V1", true, true},
		{"delete", "DELETE FROM metrics", "", false, false},
		{"insert", "INSERT INTO metrics VALUES (1)", "", false, false},
		{"multiple", httpSQL + "; SELECT 1", "", false, false},
		{"volatile", "SELECT random()", "", false, false},
		{"clock", "SELECT now()", "", false, false},
		{"duplicate", httpSQL, "&sql=SELECT+1", false, false},
		{"bad_escape", httpSQL, "&db=%XY", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/v1/sql?sql="+url.QueryEscape(test.statement)+test.params, nil)
			trq, _, object, err := (&Client{}).ParseTimeRangeQuery(r)
			if object != test.object || (err == nil) != test.delta {
				t.Fatalf("object=%v delta=%v query=%+v err=%v", object, err == nil, trq, err)
			}
		})
	}
}

func TestHTTPSQLFormRewrite(t *testing.T) {
	for _, inURL := range []bool{false, true} {
		t.Run(map[bool]string{false: "form", true: "url"}[inURL], func(t *testing.T) {
			form := url.Values{"sql": {httpSQL}, "db": {"form_db"}, "format": {"greptimedb_v1"}, "epoch": {"ns"}}
			query := url.Values{"db": {"url_db"}}
			if inURL {
				query.Set("sql", httpSQL)
				form.Set("sql", "SELECT 5")
			}
			body := form.Encode()
			r := httptest.NewRequest("POST", "/v1/sql?"+query.Encode(), strings.NewReader(body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
			c := &Client{}
			trq, _, _, err := c.ParseTimeRangeQuery(r)
			if err != nil {
				t.Fatal(err)
			}
			identity := maps.Clone(trq.CacheKeyElements)
			if string(trq.OriginalBody) != body || !strings.Contains(identity["greptime.http"], "db=url_db") {
				t.Fatal("body or effective database identity was lost")
			}
			extent := trq.Extent
			extent.Start = extent.Start.Add(15 * time.Minute)
			wantSQL, err := trq.ParsedQuery.(*sqlanalyzer.QueryPlan).RenderExtent(extent)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.SetExtent(r, trq, &extent); err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(r.Body)
			gotForm, err := url.ParseQuery(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if inURL {
				query.Set("sql", wantSQL)
			} else {
				form.Set("sql", wantSQL)
			}
			if !reflect.DeepEqual(query, r.URL.Query()) || !reflect.DeepEqual(form, gotForm) || !maps.Equal(identity, trq.CacheKeyElements) {
				t.Fatalf("extent rewrite changed unrelated fields: %v %v", r.URL.Query(), gotForm)
			}
		})
	}
}

func TestHTTPSQLRejectedRequestsPreserveBody(t *testing.T) {
	for _, test := range []struct{ name, method, query, body, contentType string }{
		{"empty_url", "POST", "sql=", "sql=SELECT+1", "application/x-www-form-urlencoded"},
		{"duplicate_form", "POST", "sql=SELECT+1", "sql=SELECT+2&sql=SELECT+3", "application/x-www-form-urlencoded"},
		{"malformed_form", "POST", "sql=SELECT+1", "db=%XX", "application/x-www-form-urlencoded"},
		{"json", "POST", "sql=SELECT+1", `{"sql":"SELECT 2"}`, "application/json"},
		{"missing_type", "POST", "sql=SELECT+1", "", ""},
		{"method", "PUT", "sql=SELECT+1", "sql=SELECT+2", "application/x-www-form-urlencoded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, "/v1/sql?"+test.query, strings.NewReader(test.body))
			r.Header.Set("Content-Type", test.contentType)
			_, _, can, err := (&Client{}).ParseTimeRangeQuery(r)
			if err == nil || can {
				t.Fatal("invalid request became cacheable")
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != test.body {
				t.Fatal("fallback request body changed")
			}
		})
	}
	if _, _, can, err := (&Client{}).ParseTimeRangeQuery(nil); err == nil || can {
		t.Fatal("nil request accepted")
	}
	if err := (&Client{}).SetExtent(nil, nil, nil); err == nil {
		t.Fatal("nil rewrite accepted")
	}
}

func TestHTTPSQLTimezoneAndOverrides(t *testing.T) {
	statement := strings.ReplaceAll(httpSQL, "date_bin('15m', ts)", "date_trunc('hour', ts)")
	for _, test := range []struct {
		name, timezone, override string
		delta                    bool
	}{
		{"default", "", "", true},
		{"utc", "UTC", "", true},
		{"offset", "+08:00", "", false},
		{"configured_offset", "UTC", "+08:00", false},
		{"configured_utc", "+08:00", "UTC", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/v1/sql?sql="+url.QueryEscape(statement), nil)
			r.Header.Set("X-Greptime-Timezone", test.timezone)
			pc := po.New()
			if test.override != "" {
				pc.RequestHeaders["X-Greptime-Timezone"] = test.override
			}
			r = request.SetResources(r, request.NewResources(bo.New(), pc, nil, nil, nil, nil))
			_, _, _, err := (&Client{}).ParseTimeRangeQuery(r)
			if (err == nil) != test.delta {
				t.Fatalf("delta=%v error=%v", test.delta, err)
			}
			if r.Header.Get("X-Greptime-Timezone") != test.timezone {
				t.Fatal("input headers mutated")
			}
			pc.RequestParams = map[string]string{"sql": "DELETE FROM metrics"}
			if _, _, can, err := (&Client{}).ParseTimeRangeQuery(r); err == nil || can {
				t.Fatal("late query override was cached")
			}
		})
	}
}

func TestHTTPSQLOpenEndedBackfill(t *testing.T) {
	statement := strings.Replace(httpSQL, " AND ts < '2024-01-01T01:00:00Z'", "", 1)
	r := httptest.NewRequest(http.MethodGet, "/v1/sql?sql="+url.QueryEscape(statement), nil)
	trq, _, _, err := (&Client{}).ParseTimeRangeQuery(r)
	if err != nil || trq.BackfillTolerance != 15*time.Minute {
		t.Fatalf("query=%+v err=%v", trq, err)
	}
}

func BenchmarkHTTPSQLParse(b *testing.B) {
	r := httptest.NewRequest("GET", "/v1/sql?sql="+url.QueryEscape(httpSQL), nil)
	for b.Loop() {
		if _, _, _, err := (&Client{}).ParseTimeRangeQuery(r); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHTTPSQLRender(b *testing.B) {
	r := httptest.NewRequest("GET", "/v1/sql?sql="+url.QueryEscape(httpSQL), nil)
	trq, _, _, err := (&Client{}).ParseTimeRangeQuery(r)
	if err != nil {
		b.Fatal(err)
	}
	extent := timeseries.Extent{Start: trq.Extent.Start, End: trq.Extent.Start}
	for b.Loop() {
		if err := (&Client{}).SetExtent(r, trq, &extent); err != nil {
			b.Fatal(err)
		}
	}
}
