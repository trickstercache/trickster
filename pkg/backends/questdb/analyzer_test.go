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

package questdb

import (
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

var questDBTestNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

const questDBTestRange = " FROM trips WHERE pickup_datetime >= '2026-09-18T08:00:03.123456Z'" +
	" AND pickup_datetime < '2026-09-18T11:00:07.456789Z'"

func TestAnalyzerSampleBy(t *testing.T) {
	for name, test := range map[string]struct {
		sql    string
		step   time.Duration
		groups []string
	}{
		"fixed sample": {
			sql:  "SELECT pickup_datetime AS time, count() AS trips" + questDBTestRange + " SAMPLE BY 5m",
			step: 5 * time.Minute,
		},
		"sample with series": {
			sql:  "SELECT pickup_datetime AS time, cab_type, count() AS trips" + questDBTestRange + " SAMPLE BY 15m",
			step: 15 * time.Minute, groups: []string{"cab_type"},
		},
		"multiline proxy query": {
			sql: `SELECT pickup_datetime AS time, cab_type, count() AS trips
FROM trips
WHERE pickup_datetime >= '2026-09-18T08:00:03.123456Z'
  AND pickup_datetime < '2026-09-18T11:00:07.456789Z'
SAMPLE BY 5m`,
			step: 5 * time.Minute, groups: []string{"cab_type"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			analysis := analyzer.Analyze(test.sql, questDBTestNow)
			if analysis.Mode != sqlanalyzer.CacheModeDelta {
				t.Fatalf("got %v / %v / %v", analysis.Mode, analysis.Reason, analysis.Err)
			}
			plan := analysis.Plan
			if plan.Step != test.step || plan.TimeColumn != "pickup_datetime" || plan.OutputColumn != "time" {
				t.Fatalf("unexpected plan: %+v", plan)
			}
			if len(plan.GroupColumns) != len(test.groups) {
				t.Fatalf("groups = %v, want %v", plan.GroupColumns, test.groups)
			}
			for i := range test.groups {
				if plan.GroupColumns[i] != test.groups[i] {
					t.Fatalf("groups = %v, want %v", plan.GroupColumns, test.groups)
				}
			}
			if !strings.Contains(plan.CanonicalSQL, "SAMPLE BY") {
				t.Fatalf("canonical SQL lost the lifted clause: %q", plan.CanonicalSQL)
			}
			rendered, err := plan.RenderExtent(timeRangeExtent())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(rendered, "SAMPLE BY") || strings.Contains(rendered, "<$") {
				t.Fatalf("bad extent SQL: %q", rendered)
			}
		})
	}
}

func TestAnalyzerTimestampFloor(t *testing.T) {
	analysis := analyzer.Analyze(
		"SELECT timestamp_floor('5m', pickup_datetime) AS time, count() AS trips"+
			questDBTestRange+" GROUP BY 1 ORDER BY 1", questDBTestNow)
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan.Step != 5*time.Minute {
		t.Fatalf("got %v / %v / %v", analysis.Mode, analysis.Reason, analysis.Err)
	}
}

func TestAnalyzerFailsClosed(t *testing.T) {
	for name, sql := range map[string]string{
		"month sample":           "SELECT pickup_datetime AS time, count()" + questDBTestRange + " SAMPLE BY 1M",
		"previous fill":          "SELECT pickup_datetime AS time, count()" + questDBTestRange + " SAMPLE BY 5m FILL(PREV)",
		"null fill":              "SELECT pickup_datetime AS time, count()" + questDBTestRange + " SAMPLE BY 5m FILL(NULL)",
		"clause owned bounds":    "SELECT pickup_datetime AS time, count()" + questDBTestRange + " SAMPLE BY 5m FROM '2026-09-18T08:00:00Z' TO '2026-09-18T11:00:00Z'",
		"duplicate fill":         "SELECT pickup_datetime AS time, count()" + questDBTestRange + " SAMPLE BY 5m FILL(NULL) FILL(NULL)",
		"explicit group":         "SELECT pickup_datetime AS time, cab_type, count()" + questDBTestRange + " GROUP BY cab_type SAMPLE BY 5m",
		"timestamp is not first": "SELECT cab_type, pickup_datetime AS time, count()" + questDBTestRange + " SAMPLE BY 5m",
		"sampling text":          "SELECT 'SAMPLE BY 5m' AS text" + questDBTestRange,
		"interval overflow":      "SELECT pickup_datetime AS time, count()" + questDBTestRange + " SAMPLE BY 9223372036854775808s",
	} {
		t.Run(name, func(t *testing.T) {
			analysis := analyzer.Analyze(sql, questDBTestNow)
			if analysis.Mode != sqlanalyzer.CacheModeObject {
				t.Fatalf("got %v / %v / %v", analysis.Mode, analysis.Reason, analysis.Err)
			}
		})
	}
}

func TestAnalyzerQuestDBVolatileFunctions(t *testing.T) {
	for _, name := range []string{
		"sysdate", "systimestamp", "systimestamp_ns",
		"rnd_double", "rnd_int", "rnd_timestamp", "rnd_varchar",
	} {
		t.Run(name, func(t *testing.T) {
			analysis := analyzer.Analyze("SELECT "+name+"() FROM trips", questDBTestNow)
			if analysis.Mode != sqlanalyzer.CacheModeNone || analysis.Reason != sqlanalyzer.ReasonNondeterministic {
				t.Fatalf("got %v / %v / %v", analysis.Mode, analysis.Reason, analysis.Err)
			}
		})
	}

	analysis := analyzer.Analyze(
		"SELECT pickup_datetime AS time, rnd_double() AS value"+questDBTestRange+
			" SAMPLE BY 5m",
		questDBTestNow,
	)
	if analysis.Mode != sqlanalyzer.CacheModeNone || analysis.Reason != sqlanalyzer.ReasonNondeterministic {
		t.Fatalf("delta query got %v / %v / %v", analysis.Mode, analysis.Reason, analysis.Err)
	}
}

func TestAnalyzerVolatileFallbacks(t *testing.T) {
	for name, sql := range map[string]string{
		"standalone":             "SELECT systimestamp()",
		"limit":                  "SELECT systimestamp() FROM trips LIMIT 1",
		"distinct":               "SELECT DISTINCT sysdate() FROM trips",
		"union":                  "SELECT systimestamp() UNION ALL SELECT systimestamp()",
		"cte":                    "WITH clock AS (SELECT systimestamp()) SELECT * FROM clock",
		"subquery":               "SELECT (SELECT systimestamp()) FROM trips",
		"join":                   "SELECT systimestamp() FROM trips a JOIN trips b ON a.cab_type = b.cab_type",
		"null fill":              "SELECT pickup_datetime, avg(rnd_double())" + questDBTestRange + " SAMPLE BY 5m FILL(NULL)",
		"previous fill":          "SELECT pickup_datetime, avg(rnd_double())" + questDBTestRange + " SAMPLE BY 5m FILL(PREV)",
		"dialect syntax":         "SELECT systimestamp() FROM trips LATEST ON pickup_datetime PARTITION BY cab_type",
		"predicate":              "SELECT count() FROM trips WHERE rnd_double() > 0.5",
		"qualified":              "SELECT qdb.systimestamp()",
		"quoted":                 `SELECT "SYSTIMESTAMP"()`,
		"comment between tokens": "SELECT SyStImEsTaMp /* clock */ ()",
		"quoted delta":           `SELECT pickup_datetime, "SYSTIMESTAMP"()` + questDBTestRange + " SAMPLE BY 5m",
		"qualified delta":        "SELECT pickup_datetime, qdb.systimestamp()" + questDBTestRange + " SAMPLE BY 5m",

		"default clock": "SELECT now()",
	} {
		t.Run(name, func(t *testing.T) {
			got := analyzer.Analyze(sql, questDBTestNow)
			if got.Mode != sqlanalyzer.CacheModeNone || got.Reason != sqlanalyzer.ReasonNondeterministic {
				t.Fatalf("got %v / %v / %v", got.Mode, got.Reason, got.Err)
			}
		})
	}
}

func TestAnalyzerVolatilePolicy(t *testing.T) {
	for _, expression := range []string{
		"now_ns()", "today()", "tomorrow()", "yesterday()",
		"timestamp_shuffle('2026-09-18T08:00:00Z', '2026-09-18T11:00:00Z')",
		"rnd_geohash(8)", "rnd_interval()", "rnd_log(1.0, 2.0)",
		"rnd_double_array(2, 2, 0)", "rnd_uuid4()", "rnd_symbol_weighted()",
	} {
		for _, sql := range []string{
			"SELECT " + expression,
			"SELECT " + expression + " FROM trips",
			"SELECT pickup_datetime AS time, " + expression + " AS value" + questDBTestRange + " SAMPLE BY 5m",
		} {
			t.Run(sql, func(t *testing.T) {
				got := analyzer.Analyze(sql, questDBTestNow)
				if got.Mode != sqlanalyzer.CacheModeNone || got.Reason != sqlanalyzer.ReasonNondeterministic {
					t.Fatalf("got %v / %v / %v", got.Mode, got.Reason, got.Err)
				}
			})
		}
	}
}

func TestAnalyzerVolatilityIgnoresNonCalls(t *testing.T) {
	for _, sql := range []string{
		"SELECT 'systimestamp()' FROM trips LIMIT 1",
		"SELECT $$rnd_double()$$ FROM trips",
		"SELECT systimestamp FROM trips",
		`SELECT "systimestamp" FROM trips`,
		"SELECT count() FROM trips /* systimestamp() */",
		"SELECT count() FROM trips -- rnd_double()",
		"SELECT count() FROM trips WHERE pickup_datetime > '2026-09-18T08:00:00Z'",
	} {
		t.Run(sql, func(t *testing.T) {
			got := analyzer.Analyze(sql, questDBTestNow)
			if got.Mode != sqlanalyzer.CacheModeObject {
				t.Fatalf("got %v / %v / %v", got.Mode, got.Reason, got.Err)
			}
		})
	}
}

func timeRangeExtent() timeseries.Extent {
	return timeseries.Extent{
		Start: questDBTestNow.Add(-2 * time.Hour),
		End:   questDBTestNow.Add(-time.Hour),
	}
}
