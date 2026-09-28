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
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const testRange = " FROM trips WHERE pickup_datetime >= '2026-01-01T00:00:00Z' AND pickup_datetime < '2026-02-01T00:00:00Z' GROUP BY 1 ORDER BY 1"

func TestCacheAnalyzer(t *testing.T) {
	a := Engine().Analyzer()
	if a == nil {
		t.Fatal("GreptimeDB must analyze SQL before enabling its native cache")
	}
	if sessions, ok := a.(pgwire.SessionAnalyzer); ok {
		a = sessions.ForSession(pgwire.SessionView{UTC: true})
	}
	for _, bucket := range []string{
		"date_bin(INTERVAL '5 minutes', pickup_datetime)",
		"date_bin('5m', pickup_datetime)",
		"date_trunc('week', pickup_datetime)",
		"floor(extract(epoch FROM pickup_datetime)/300)*300",
		"floor(date_part('epoch', pickup_datetime)/300)*300",
	} {
		t.Run(bucket, func(t *testing.T) {
			analysis := a.Analyze("SELECT "+bucket+" AS time, count(*)"+testRange, time.Now())
			if analysis.Mode != sqlanalyzer.CacheModeDelta {
				t.Fatalf("got %v/%v: %v", analysis.Mode, analysis.Reason, analysis.Err)
			}
		})
	}
}

func TestAnalyzerFailsClosed(t *testing.T) {
	for name, test := range map[string]struct {
		sql    string
		mode   sqlanalyzer.CacheMode
		reason sqlanalyzer.AnalysisReason
	}{
		"scalar":                       {"SELECT 1", sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat},
		"limited":                      {"SELECT * FROM trips LIMIT 10", sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedLimit},
		"insert":                       {"INSERT INTO trips VALUES (1)", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonUnsupportedStatement},
		"ddl":                          {"CREATE TABLE a (b int)", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonUnsupportedStatement},
		"tql":                          {"TQL EVAL (0, 1, '1s') up", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonInvalidSQL},
		"admin":                        {"ADMIN FLUSH_TABLE('trips')", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonInvalidSQL},
		"range select":                 {"SELECT sum(fare_amount) RANGE '5m' FROM trips ALIGN '5m'", sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonInvalidSQL},
		"volatile despite limit":       {"SELECT random() FROM trips LIMIT 10", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		"volatile in cte":              {"WITH x AS (SELECT random()) SELECT * FROM x", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		"volatile in unparsed dialect": {"SELECT random() RANGE '5m' FROM trips ALIGN '5m'", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		"clock output":                 {"SELECT current_timestamp", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		"side effect":                  {"SELECT flush_flow('f')", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		"calendar bucket":              {"SELECT date_trunc('month', pickup_datetime) AS time, count(*)" + testRange, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket},
		"fractional compact width":     {"SELECT date_bin('0.5s', pickup_datetime) AS time, count(*)" + testRange, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket},
		"submicrosecond output":        {"SELECT date_bin('5ns', pickup_datetime) AS time, count(*)" + testRange, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket},
	} {
		t.Run(name, func(t *testing.T) {
			a := analyzer.ForSession(pgwire.SessionView{UTC: true}).Analyze(test.sql, time.Now())
			if a.Mode != test.mode || a.Reason != test.reason {
				t.Fatalf("got %s/%s (%v), want %s/%s", a.Mode, a.Reason, a.Err, test.mode, test.reason)
			}
		})
	}
}

func TestAnalyzerRender(t *testing.T) {
	for _, bucket := range []string{"date_bin(INTERVAL '5 minutes', pickup_datetime)", "date_bin('5m', pickup_datetime)", "floor(extract(epoch FROM pickup_datetime)/300)*300"} {
		sql := "SELECT " + bucket + " AS time, count(*)" + testRange
		a := analyzer.ForSession(pgwire.SessionView{UTC: true}).Analyze(sql, time.Now())
		if a.Mode != sqlanalyzer.CacheModeDelta {
			t.Fatalf("%s: %+v", sql, a)
		}
		extent := timeseries.Extent{Start: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)}
		for range 2 {
			rendered, err := a.Plan.RenderExtent(extent)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(rendered, "extract('epoch',") || !strings.Contains(rendered, "2026-01-02") {
				t.Fatalf("bad rendering: %s", rendered)
			}
			b := analyzer.ForSession(pgwire.SessionView{UTC: true}).Analyze(rendered, time.Now())
			if b.Mode != sqlanalyzer.CacheModeDelta || b.Plan.Step != a.Plan.Step {
				t.Fatalf("cannot analyze rendered SQL %s: %+v", rendered, b)
			}
		}
	}
	sql := "SELECT date_bin('5m', pickup_datetime) AS time, count(*)" + strings.ReplaceAll(testRange, "T00:00:00Z", " 00:00:00")
	if a := analyzer.Analyze(sql, time.Now()); a.Mode == sqlanalyzer.CacheModeDelta {
		t.Fatal("unknown session zone accepted naive bounds")
	}
	if a := analyzer.ForSession(pgwire.SessionView{UTC: true}).Analyze(sql, time.Now()); a.Mode != sqlanalyzer.CacheModeDelta {
		t.Fatalf("known UTC rejected naive bounds: %+v", a)
	}
}

func TestPostRender(t *testing.T) {
	for input, want := range map[string]string{
		"SELECT extract('epoch', ts)":                        "SELECT extract(epoch FROM ts)",
		"SELECT bucket::STRING, 'bucket STRING', col::BYTES": "SELECT \"bucket\"::TEXT, 'bucket STRING', col::BYTEA",
		"SELECT 'extract(''epoch'', ts)'":                    "SELECT 'extract(''epoch'', ts)'",
		"SELECT extract(epoch FROM ts)":                      "SELECT extract(epoch FROM ts)",
		"SELECT extract":                                     "SELECT extract",
	} {
		got, err := postRender(input)
		if err != nil || got != want {
			t.Fatalf("%s: got %s, %v; want %s", input, got, err, want)
		}
	}
	for _, input := range []string{"SELECT extract('', ts)", "SELECT extract('bad-field', ts)", "SELECT extract('a''b', ts)"} {
		if _, err := postRender(input); err == nil {
			t.Fatalf("accepted invalid extract field: %s", input)
		}
	}
}

func TestAnalyzerGuardedBuckets(t *testing.T) {
	for name, tc := range map[string]struct {
		sql    string
		mode   sqlanalyzer.CacheMode
		reason sqlanalyzer.AnalysisReason
	}{
		"clock resolved only in bounds": {"SELECT date_bin('5m', pickup_datetime) AS time, count(*) FROM trips WHERE pickup_datetime >= now() - INTERVAL '1 hour' AND pickup_datetime < now() GROUP BY 1", sqlanalyzer.CacheModeDelta, sqlanalyzer.ReasonDeltaCacheable},
		"unfaithful qualifier":          {"SELECT date_bin('5m', pickup_datetime) AS time, count(*)" + strings.Replace(testRange, "FROM trips", "FROM ONLY trips", 1), sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat},
		"set returning":                 {"SELECT date_bin('5m', pickup_datetime) AS time, max(unnest(items))" + testRange, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat},
		"submicrosecond origin":         {"SELECT date_bin('5m', pickup_datetime, TIMESTAMP '1970-01-01T00:00:00.000000001Z') AS time, count(*)" + testRange, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket},
	} {
		t.Run(name, func(t *testing.T) {
			a := analyzer.ForSession(pgwire.SessionView{UTC: true}).Analyze(tc.sql, time.Now())
			if a.Mode != tc.mode || a.Reason != tc.reason {
				t.Fatalf("got %s/%s (%v), want %s/%s", a.Mode, a.Reason, a.Err, tc.mode, tc.reason)
			}
		})
	}
	sql := "SELECT floor(extract(epoch FROM pickup_datetime)/300)*300 AS time, count(*)" + testRange
	if a := analyzer.ForSession(pgwire.SessionView{}).Analyze(sql, time.Now()); a.Mode != sqlanalyzer.CacheModeDelta {
		t.Fatalf("absolute bounds must remain cacheable without a UTC session: %+v", a)
	}
}
