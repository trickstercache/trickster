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

package cockroach

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

/*
Example Usage:

package main

import (
	"fmt"
	"log"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser"
)

func main() {
	sql := `SELECT TIME_FLOOR(__time, 'PT1H') AS bucket, SUM(value)
	        FROM metrics GROUP BY 1`

	stmt, err := parser.ParseOne(sql)
	if err != nil {
		log.Fatal(err)
	}
	stdout.Debug(fmt.Sprintf("Formatted SQL: %s\n", stmt.AST))
}
*/

func newDataFusionAnalyzer() *Analyzer {
	return NewAnalyzer(Options{BucketMatchers: DataFusionBucketMatchers()})
}

const hourlyEpochQuery = `SELECT date_bin(INTERVAL '1 hour', time) AS time, ` +
	`avg(temperature) AS temperature FROM weather ` +
	`WHERE time >= 1704067200 AND time < 1704153600 GROUP BY 1`

func TestAnalyzeDeltaCacheableQueries(t *testing.T) {
	a := newDataFusionAnalyzer()
	tests := []struct {
		name  string
		query string
		step  time.Duration
		unit  timeseries.FieldDataType
	}{
		{"date_bin epoch seconds", hourlyEpochQuery, time.Hour, timeseries.DateTimeUnixSecs},
		{"date_bin five minutes",
			`SELECT date_bin(INTERVAL '5 minutes', time) AS time, avg(cpu) AS cpu FROM metrics WHERE time >= 1704067200 AND time < 1704070800 GROUP BY 1`,
			5 * time.Minute, timeseries.DateTimeUnixSecs},
		{"date_bin SQL datetime bounds",
			`SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(temp) FROM weather WHERE time >= '2024-01-01 00:00:00' AND time < '2024-01-02 00:00:00' GROUP BY 1`,
			time.Hour, timeseries.DateTimeSQL},
		{"date_bin RFC3339 bounds",
			`SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(temp) FROM weather WHERE time >= '2024-01-01T00:00:00Z' AND time < '2024-01-02T00:00:00Z' GROUP BY 1`,
			time.Hour, timeseries.DateTimeRFC3339},
		{"date_bin TIMESTAMP literal bounds",
			`SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(temp) FROM weather WHERE time >= TIMESTAMP '2024-01-01 00:00:00' AND time < TIMESTAMP '2024-01-02 00:00:00' GROUP BY 1`,
			time.Hour, timeseries.DateTimeSQL},
		{"date_bin epoch nanoseconds",
			`SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(temp) FROM weather WHERE time >= 1704067200000000000 AND time < 1704153600000000000 GROUP BY 1`,
			time.Hour, timeseries.DateTimeUnixNano},
		{"date_trunc hour",
			`SELECT date_trunc('hour', time) AS time, avg(temperature) AS temperature FROM weather WHERE time >= 1704067200 AND time < 1704153600 GROUP BY 1`,
			time.Hour, timeseries.DateTimeUnixSecs},
		{"date_trunc day",
			`SELECT date_trunc('day', time) AS time, avg(val) FROM stats WHERE time >= 1704067200 AND time < 1704153600 GROUP BY 1`,
			24 * time.Hour, timeseries.DateTimeUnixSecs},
		{"group by alias",
			`SELECT date_bin(INTERVAL '1 hour', time) AS bucket, sum(v) FROM t WHERE time >= 1704067200 AND time < 1704153600 GROUP BY bucket`,
			time.Hour, timeseries.DateTimeUnixSecs},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := a.Analyze(tc.query, time.Time{})
			if got.Mode != sqlanalyzer.CacheModeDelta || got.Plan == nil {
				t.Fatalf("Analyze() = %s/%s (%v)", got.Mode, got.Reason, got.Err)
			}
			if got.Plan.Step != tc.step {
				t.Fatalf("step = %s, want %s", got.Plan.Step, tc.step)
			}
			if got.Plan.InputUnit != tc.unit {
				t.Fatalf("input unit = %v, want %v", got.Plan.InputUnit, tc.unit)
			}
			if got.Plan.LowerBound == nil || got.Plan.UpperBound == nil {
				t.Fatal("plan is missing bounds")
			}
		})
	}
}

func TestAnalyzePlanShape(t *testing.T) {
	a := newDataFusionAnalyzer()
	query := `SELECT date_bin(INTERVAL '5 minutes', ts) AS bucket, host, region, ` +
		`avg(cpu) AS cpu FROM metrics ` +
		`WHERE ts >= 1704067200 AND ts < 1704070800 GROUP BY 1, host, region`
	got := a.Analyze(query, time.Time{})
	if got.Mode != sqlanalyzer.CacheModeDelta || got.Plan == nil {
		t.Fatalf("Analyze() = %s/%s (%v)", got.Mode, got.Reason, got.Err)
	}
	plan := got.Plan
	if plan.TimeColumn != "ts" || plan.OutputColumn != "bucket" ||
		!slices.Equal(plan.GroupColumns, []string{"host", "region"}) {
		t.Fatalf("unexpected plan shape: %+v", plan)
	}
	if !plan.LowerBound.Value.Equal(time.Unix(1704067200, 0)) || !plan.LowerBound.Inclusive {
		t.Fatalf("lower bound = %+v", plan.LowerBound)
	}
	if !plan.UpperBound.Value.Equal(time.Unix(1704070800, 0)) || plan.UpperBound.Inclusive {
		t.Fatalf("upper bound = %+v", plan.UpperBound)
	}
	if !strings.Contains(plan.CanonicalSQL, "<$TS1$>") ||
		!strings.Contains(plan.CanonicalSQL, "<$TS2$>") {
		t.Fatalf("canonical SQL lacks range tokens: %s", plan.CanonicalSQL)
	}
}

func TestAnalyzeDateBinOriginPhase(t *testing.T) {
	a := newDataFusionAnalyzer()
	query := `SELECT date_bin(INTERVAL '1 hour', time, TIMESTAMP '2024-01-01 00:30:00') AS time, ` +
		`sum(v) FROM t WHERE time >= 1704069000 AND time < 1704155400 GROUP BY 1`
	got := a.Analyze(query, time.Time{})
	if got.Mode != sqlanalyzer.CacheModeDelta || got.Plan == nil {
		t.Fatalf("Analyze() = %s/%s (%v)", got.Mode, got.Reason, got.Err)
	}
	if got.Plan.Phase != 30*time.Minute {
		t.Fatalf("phase = %s, want 30m", got.Plan.Phase)
	}
	// The same bounds are not phase-aligned without the origin shift.
	unshifted := a.Analyze(strings.Replace(query,
		", TIMESTAMP '2024-01-01 00:30:00'", "", 1), time.Time{})
	if unshifted.Mode == sqlanalyzer.CacheModeDelta {
		t.Fatalf("unaligned bounds were delta-cacheable: %+v", unshifted.Plan)
	}
}

func TestAnalyzeCanonicalizesRangeAndRendersExtent(t *testing.T) {
	a := newDataFusionAnalyzer()
	first := a.Analyze(hourlyEpochQuery, time.Time{})
	second := a.Analyze(strings.Replace(hourlyEpochQuery, "1704067200", "1703980800", 1), time.Time{})
	if first.Plan == nil || second.Plan == nil {
		t.Fatal("expected delta plans")
	}
	if first.Plan.CanonicalSQL != second.Plan.CanonicalSQL {
		t.Fatalf("range-independent keys differ:\n%s\n%s",
			first.Plan.CanonicalSQL, second.Plan.CanonicalSQL)
	}

	start := time.Unix(1704070800, 0).UTC()
	end := start.Add(2 * time.Hour)
	rendered, err := first.Plan.RenderExtent(timeseries.Extent{Start: start, End: end})
	if err != nil {
		t.Fatal(err)
	}
	// Extents are inclusive bucket starts; the exclusive upper comparator is
	// re-rendered one step past the final included bucket.
	if strings.Contains(rendered, "TRICKSTER") ||
		!strings.Contains(rendered, ">= 1704070800") ||
		!strings.Contains(rendered, "< 1704081600") {
		t.Fatalf("unexpected rendered SQL: %s", rendered)
	}
}

func TestRenderExtentPreservesBoundStyles(t *testing.T) {
	a := newDataFusionAnalyzer()
	extent := timeseries.Extent{
		Start: time.Unix(1704070800, 0).UTC(),
		End:   time.Unix(1704074400, 0).UTC(),
	}
	tests := []struct {
		name, query, wantLower, wantUpper string
	}{
		{"sql datetime",
			`SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(v) FROM m WHERE time >= '2024-01-01 00:00:00' AND time < '2024-01-02 00:00:00' GROUP BY 1`,
			`'2024-01-01 01:00:00'`, `'2024-01-01 03:00:00'`},
		{"rfc3339",
			`SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(v) FROM m WHERE time >= '2024-01-01T00:00:00Z' AND time < '2024-01-02T00:00:00Z' GROUP BY 1`,
			`'2024-01-01T01:00:00Z'`, `'2024-01-01T03:00:00Z'`},
		{"timestamp literal",
			`SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(v) FROM m WHERE time >= TIMESTAMP '2024-01-01 00:00:00' AND time < TIMESTAMP '2024-01-02 00:00:00' GROUP BY 1`,
			`TIMESTAMP '2024-01-01 01:00:00'`, `TIMESTAMP '2024-01-01 03:00:00'`},
		{"epoch nanoseconds",
			`SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(v) FROM m WHERE time >= 1704067200000000000 AND time < 1704153600000000000 GROUP BY 1`,
			`1704070800000000000`, `1704078000000000000`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := a.Analyze(tc.query, time.Time{}).Plan
			if plan == nil {
				t.Fatal("expected a delta plan")
			}
			rendered, err := plan.RenderExtent(extent)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(rendered, tc.wantLower) ||
				!strings.Contains(rendered, tc.wantUpper) {
				t.Fatalf("bounds did not round-trip in style:\nwant %s / %s\ngot %s",
					tc.wantLower, tc.wantUpper, rendered)
			}
		})
	}
}

func TestAnalyzeOpenUpperBoundAddsSyntheticPredicate(t *testing.T) {
	a := newDataFusionAnalyzer()
	now := time.Unix(1704153600, 0).UTC()
	query := `SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(v) FROM m ` +
		`WHERE time >= 1704067200 GROUP BY 1`
	got := a.Analyze(query, now)
	if got.Mode != sqlanalyzer.CacheModeDelta || got.Plan == nil {
		t.Fatalf("Analyze() = %s/%s (%v)", got.Mode, got.Reason, got.Err)
	}
	if got.Plan.UpperBound != nil {
		t.Fatalf("open range has an upper bound: %+v", got.Plan.UpperBound)
	}
	rendered, err := got.Plan.RenderExtent(timeseries.Extent{
		Start: time.Unix(1704070800, 0).UTC(), End: time.Unix(1704074400, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, ">= 1704070800") ||
		!strings.Contains(rendered, "< 1704078000") {
		t.Fatalf("synthetic upper bound missing: %s", rendered)
	}
}

func TestAnalyzeNowRelativeBounds(t *testing.T) {
	a := newDataFusionAnalyzer()
	now := time.Unix(1704153600, 0).UTC()
	query := `SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(v) FROM m ` +
		`WHERE time >= now() - INTERVAL '24 hours' GROUP BY 1`
	got := a.Analyze(query, now)
	if got.Mode != sqlanalyzer.CacheModeDelta || got.Plan == nil {
		t.Fatalf("Analyze() = %s/%s (%v)", got.Mode, got.Reason, got.Err)
	}
	if !got.Plan.LowerBound.Value.Equal(now.Add(-24 * time.Hour)) {
		t.Fatalf("lower bound = %v, want %v", got.Plan.LowerBound.Value, now.Add(-24*time.Hour))
	}
}

func TestAnalyzeClassifiesUnsupportedQueries(t *testing.T) {
	a := newDataFusionAnalyzer()
	tests := []struct {
		name   string
		query  string
		mode   sqlanalyzer.CacheMode
		reason sqlanalyzer.AnalysisReason
	}{
		{"empty", " ", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonInvalidSQL},
		{"mutation", "DELETE FROM trips", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonUnsupportedStatement},
		{"create", "CREATE TABLE test (id INT)", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonUnsupportedStatement},
		{"invalid select", "SELECT , FROM trips", sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonInvalidSQL},
		{"invalid non-select", "GRANT nothing", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonInvalidSQL},
		{"no bucket", "SELECT count(*) FROM trips WHERE time >= 1704067200 AND time < 1704153600 GROUP BY 1", sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket},
		{"variable-length unit", strings.Replace(hourlyEpochQuery, "'1 hour'", "'1 month'", 1), sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket},
		{"limit", hourlyEpochQuery + " LIMIT 10", sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedLimit},
		{"cte", "WITH t AS (SELECT 1) " + hourlyEpochQuery, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat},
		{"subquery", strings.Replace(hourlyEpochQuery, "FROM weather", "FROM (SELECT * FROM weather)", 1), sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat},
		{"distinct", strings.Replace(hourlyEpochQuery, "SELECT ", "SELECT DISTINCT ", 1), sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat},
		{"volatile select", strings.Replace(hourlyEpochQuery, "avg(temperature)", "now()", 1), sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"missing group by", strings.Replace(hourlyEpochQuery, " GROUP BY 1", "", 1), sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedGrouping},
		{"ungrouped tag", `SELECT date_bin(INTERVAL '1 hour', time) AS time, host, avg(v) FROM m WHERE time >= 1704067200 AND time < 1704153600 GROUP BY 1`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedGrouping},
		{"no time range", strings.Replace(hourlyEpochQuery, "WHERE time >= 1704067200 AND time < 1704153600", "WHERE city = 'sf'", 1), sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonNotTimeRange},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := a.Analyze(tc.query, time.Time{})
			if got.Mode != tc.mode || got.Reason != tc.reason {
				t.Fatalf("Analyze() = %s/%s (%v), want %s/%s", got.Mode,
					got.Reason, got.Err, tc.mode, tc.reason)
			}
		})
	}
}

func TestAnalyzePredicateSafety(t *testing.T) {
	a := newDataFusionAnalyzer()
	base := `SELECT date_bin(INTERVAL '1 hour', ts) AS bucket, count(*) AS value FROM events WHERE %s GROUP BY 1`
	tests := []struct {
		name, predicate string
		mode            sqlanalyzer.CacheMode
		reason          sqlanalyzer.AnalysisReason
	}{
		{"or", `(ts >= 1704067200 OR tenant = 1) AND ts < 1704153600`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"not", `NOT (ts >= 1704067200) AND ts < 1704153600`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"nested or", `ts >= 1704067200 AND (ts < 1704153600 OR tenant = 1)`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"exclusive lower", `ts > 1704067200 AND ts < 1704153600`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"inclusive upper", `ts >= 1704067200 AND ts <= 1704153600`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"between raw column", `ts BETWEEN 1704067200 AND 1704153600`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"unaligned lower", `ts >= 1704067201 AND ts < 1704153600`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"unaligned upper", `ts >= 1704067200 AND ts < 1704153601`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"duplicate lower", `ts >= 1704067200 AND ts >= 1704070800 AND ts < 1704153600`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonAmbiguousTimeAxis},
		{"reversed comparison", `1704067200 <= ts AND ts < 1704153600`, sqlanalyzer.CacheModeDelta, sqlanalyzer.ReasonDeltaCacheable},
		{"safe extra predicate", `ts >= 1704067200 AND ts < 1704153600 AND tenant = 1`, sqlanalyzer.CacheModeDelta, sqlanalyzer.ReasonDeltaCacheable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := a.Analyze(strings.Replace(base, "%s", tc.predicate, 1), time.Time{})
			if got.Mode != tc.mode || got.Reason != tc.reason {
				t.Fatalf("Analyze() = %s/%s (%v), want %s/%s", got.Mode,
					got.Reason, got.Err, tc.mode, tc.reason)
			}
		})
	}
}

func TestRenderExtentIsConcurrent(t *testing.T) {
	plan := newDataFusionAnalyzer().Analyze(hourlyEpochQuery, time.Time{}).Plan
	if plan == nil {
		t.Fatal("expected a delta plan")
	}
	const renders = 32
	outputs := make([]string, renders)
	errs := make([]error, renders)
	base := time.Unix(1704067200, 0).UTC()
	var wg sync.WaitGroup
	for i := range renders {
		wg.Go(func() {
			start := base.Add(time.Duration(i) * plan.Step)
			outputs[i], errs[i] = plan.RenderExtent(timeseries.Extent{
				Start: start, End: start.Add(plan.Step),
			})
		})
	}
	wg.Wait()
	for i, output := range outputs {
		if errs[i] != nil {
			t.Fatalf("render %d: %v", i, errs[i])
		}
		start := base.Add(time.Duration(i) * plan.Step)
		if !strings.Contains(output, ">= "+itoa(start.Unix())) ||
			!strings.Contains(output, "< "+itoa(start.Add(2*plan.Step).Unix())) {
			t.Fatalf("render %d used another extent: %s", i, output)
		}
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func TestParseIntervalDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
		ok    bool
	}{
		{"1 hour", time.Hour, true},
		{"5 minutes", 5 * time.Minute, true},
		{"90 seconds", 90 * time.Second, true},
		{"2 days", 48 * time.Hour, true},
		{"1 hour 30 minutes", 90 * time.Minute, true},
		{"100 milliseconds", 100 * time.Millisecond, true},
		{"1 month", 0, false},
		{"0 hours", 0, false},
		{"-1 hour", 0, false},
		{"hour", 0, false},
		{"", 0, false},
	}
	for _, tc := range tests {
		got, ok := ParseIntervalDuration(tc.input)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseIntervalDuration(%q) = %s/%t, want %s/%t",
				tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

// TestDialectAnalyzersAreInterchangeable proves the shared contract: any
// backend can hold either adapter behind sqlanalyzer.DialectAnalyzer.
func TestDialectAnalyzersAreInterchangeable(t *testing.T) {
	var analyzer sqlanalyzer.DialectAnalyzer = newDataFusionAnalyzer()
	got := analyzer.Analyze(hourlyEpochQuery, time.Time{})
	if got.Mode != sqlanalyzer.CacheModeDelta {
		t.Fatalf("Analyze() via interface = %s/%s (%v)", got.Mode, got.Reason, got.Err)
	}
	if _, err := got.Plan.RenderExtent(timeseries.Extent{
		Start: time.Unix(1704067200, 0), End: time.Unix(1704070800, 0),
	}); err != nil {
		t.Fatal(err)
	}
}
