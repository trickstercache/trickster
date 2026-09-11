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

package vitess

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"vitess.io/vitess/go/vt/sqlparser"
)

const grafanaDateTimeQuery = `SELECT
  UNIX_TIMESTAMP(pickup_datetime) DIV 300 * 300 AS time,
  count(*) AS trips
FROM trips
WHERE pickup_datetime BETWEEN FROM_UNIXTIME(1785542400) AND FROM_UNIXTIME(1785628800)
GROUP BY time
ORDER BY time`

const safeDateTimeQuery = `SELECT
  cast(cast(UNIX_TIMESTAMP(pickup_datetime)/(300) as signed)*300 as signed) AS time,
  count(*) AS trips
FROM trips
WHERE pickup_datetime >= FROM_UNIXTIME(1785542400) AND pickup_datetime < FROM_UNIXTIME(1785628800)
GROUP BY time
ORDER BY time`

func TestAnalyzerGrafanaMacroExpansions(t *testing.T) {
	a := MustNewAnalyzer()
	tests := []struct {
		name  string
		query string
		step  time.Duration
		unit  timeseries.FieldDataType
		mode  sqlanalyzer.CacheMode
	}{
		{"timeGroupAlias and timeFilter", grafanaDateTimeQuery, 0, 0, sqlanalyzer.CacheModeObject},
		{"timeGroup fill null", strings.Replace(grafanaDateTimeQuery, "300", "900", 2), 0, 0, sqlanalyzer.CacheModeObject},
		{"unixEpochGroup and unixEpochFilter", `SELECT cast(cast(pickup_epoch/(300) as signed)*300 as signed) AS time, count(*) FROM trips WHERE pickup_epoch > 1785542400 AND pickup_epoch < 1785628800 GROUP BY time ORDER BY time`, 0, 0, sqlanalyzer.CacheModeObject},
		{"unixEpochNanoGroup and unixEpochNanoFilter", `SELECT cast(cast(pickup_ns/(300000000000) as signed)*300000000000 as signed) AS time, count(*) FROM trips WHERE pickup_ns > 1785542400000000000 AND pickup_ns < 1785628800000000000 GROUP BY time ORDER BY time`, 0, 0, sqlanalyzer.CacheModeObject},
		{"timeFrom and timeTo", `SELECT cast(cast(UNIX_TIMESTAMP(ts)/(60) as signed)*60 as signed) AS time_sec, count(*) FROM events WHERE ts >= FROM_UNIXTIME(1785542400) AND ts < FROM_UNIXTIME(1785628800) GROUP BY time_sec ORDER BY time_sec`, time.Minute, timeseries.DateTimeUnixSecs, sqlanalyzer.CacheModeDelta},
		{"safe Grafana DIV expansion", `SELECT UNIX_TIMESTAMP(ts) DIV 60 * 60 AS time_sec, count(*) FROM events WHERE ts >= FROM_UNIXTIME(1785542400) AND ts < FROM_UNIXTIME(1785628800) GROUP BY time_sec ORDER BY time_sec`, time.Minute, timeseries.DateTimeUnixSecs, sqlanalyzer.CacheModeDelta},
		{"live unaligned Grafana range", `SELECT UNIX_TIMESTAMP(ts) DIV 60 * 60 AS time_sec, count(*) FROM events WHERE ts >= FROM_UNIXTIME(1785542407) AND ts < FROM_UNIXTIME(1785628807) GROUP BY time_sec ORDER BY time_sec`, time.Minute, timeseries.DateTimeUnixSecs, sqlanalyzer.CacheModeDelta},
		{"safe unix epoch", `SELECT cast(cast(pickup_epoch/(300) as signed)*300 as signed) AS time, count(*) FROM trips WHERE pickup_epoch >= 1785542400 AND pickup_epoch < 1785628800 GROUP BY time ORDER BY time`, 5 * time.Minute, timeseries.DateTimeUnixSecs, sqlanalyzer.CacheModeDelta},
		{"safe unix epoch nanos", `SELECT cast(cast(pickup_ns/(300000000000) as signed)*300000000000 as signed) AS time, count(*) FROM trips WHERE pickup_ns >= 1785542400000000000 AND pickup_ns < 1785628800000000000 GROUP BY time ORDER BY time`, 5 * time.Minute, timeseries.DateTimeUnixNano, sqlanalyzer.CacheModeDelta},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := a.Analyze(test.query, time.Time{})
			if got.Mode != test.mode {
				t.Fatalf("Analyze() = mode %s, reason %s, err %v", got.Mode, got.Reason, got.Err)
			}
			if test.mode != sqlanalyzer.CacheModeDelta {
				return
			}
			if got.Plan == nil {
				t.Fatal("delta analysis has no plan")
			}
			if got.Plan.Step != test.step || got.Plan.OutputUnit != test.unit {
				t.Fatalf("plan step/unit = %s/%v, want %s/%v", got.Plan.Step,
					got.Plan.OutputUnit, test.step, test.unit)
			}
		})
	}
}

func TestAnalyzerClassifiesUnsupportedQueries(t *testing.T) {
	a := MustNewAnalyzer()
	tests := []struct {
		name   string
		query  string
		mode   sqlanalyzer.CacheMode
		reason sqlanalyzer.AnalysisReason
	}{
		{"empty", " ", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonInvalidSQL},
		{"mutation", "DELETE FROM trips", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonUnsupportedStatement},
		{"invalid select", "SELECT FROM trips", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonInvalidSQL},
		{"table result", "SELECT count(*) FROM trips", sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket},
		{"time without cadence", "SELECT UNIX_TIMESTAMP(ts) AS time_sec FROM events WHERE ts BETWEEN FROM_UNIXTIME(1785542400) AND FROM_UNIXTIME(1785628800)", sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket},
		{"limit", grafanaDateTimeQuery + " LIMIT 10", sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedLimit},
		{"unaligned Grafana range", strings.Replace(grafanaDateTimeQuery, "1785542400", "1785542401", 1), sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"missing range", strings.Replace(grafanaDateTimeQuery, "WHERE pickup_datetime BETWEEN FROM_UNIXTIME(1785542400) AND FROM_UNIXTIME(1785628800)", "WHERE cab_type = 'yellow'", 1), sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonNotTimeRange},
		{"union select", "SELECT 1 UNION SELECT 2", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonUnsupportedStatement},
		{"random select", "SELECT RAND() FROM trips", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"sleep", "SELECT SLEEP(1)", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"random bytes", "SELECT RANDOM_BYTES(8)", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"benchmark", "SELECT BENCHMARK(10, 1 + 1)", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"load file", "SELECT LOAD_FILE('/etc/hostname')", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"schema", "SELECT SCHEMA()", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"current user", "SELECT CURRENT_USER()", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"session user", "SELECT SESSION_USER()", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"system user", "SELECT SYSTEM_USER()", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"source position wait", "SELECT SOURCE_POS_WAIT('log', 1)", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"master position wait", "SELECT MASTER_POS_WAIT('log', 1)", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"unknown function", "SELECT plugin_state()", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"SQL no cache", "SELECT SQL_NO_CACHE count(*) FROM trips", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"SQL cache", "SELECT SQL_CACHE count(*) FROM trips", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"leading executable comment", "/*!80000 SELECT 1 */", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"embedded executable comment", "SELECT /*!80000 SQL_NO_CACHE */ count(*) FROM trips", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"locking select", "SELECT * FROM trips FOR UPDATE", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"session found rows", "SELECT SQL_CALC_FOUND_ROWS * FROM trips LIMIT 10", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"current unix timestamp", "SELECT UNIX_TIMESTAMP()", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"select into", "SELECT count(*) INTO @n FROM trips", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"advisory lock", "SELECT GET_LOCK('reporting', 1)", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"advisory unlock", "SELECT RELEASE_LOCK('reporting')", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		{"descending delta order", strings.Replace(safeDateTimeQuery, "ORDER BY time", "ORDER BY time DESC", 1), sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedGrouping},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := a.Analyze(test.query, time.Time{})
			if got.Mode != test.mode || got.Reason != test.reason {
				t.Fatalf("Analyze() = %s/%s (%v), want %s/%s", got.Mode,
					got.Reason, got.Err, test.mode, test.reason)
			}
		})
	}
}

func TestSQLCommentText(t *testing.T) {
	tests := []struct {
		name, statement, want string
	}{
		{"block", "SELECT '/* ignored */', \"# ignored\", `-- ignored` /* block */", " block  "},
		{"escaped quote", `SELECT 'can\'t # comment' /* after */`, " after  "},
		{"doubled quote", "SELECT 'it''s -- data' # tail", " tail"},
		{"unterminated block", "SELECT 1 /* partial", ""},
		{"hash line", "SELECT 1 # first\n# second", " first  second"},
		{"dash line", "SELECT 1 -- first\n-- second", " first  second"},
		{"operators", "SELECT 6/2, 3-1, 2--1", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sqlCommentText(tc.statement); got != tc.want {
				t.Errorf("sqlCommentText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParserDefensiveBranches(t *testing.T) {
	parse := func(source string) sqlparser.Expr {
		t.Helper()
		expr, err := MustNewAnalyzer().Parser().ParseExpr(source)
		if err != nil {
			t.Fatalf("ParseExpr(%q): %v", source, err)
		}
		return expr
	}

	if _, err := analyzeBucket(&sqlparser.Select{}); err == nil {
		t.Error("analyzeBucket accepted an empty select list")
	}
	for _, source := range []string{"1", "1 * 60", "ts / 60 * 60", "unknown() DIV 60 * 60"} {
		if _, _, _, _, ok := matchBucketExpr(parse(source)); ok {
			t.Errorf("matchBucketExpr(%q) succeeded", source)
		}
	}
	if _, ok := unwrapSignedCasts(parse("CAST(1 AS CHAR)")); ok {
		t.Error("unwrapSignedCasts accepted a non-SIGNED cast")
	}
	for _, source := range []string{
		"FROM_UNIXTIME(epoch)",
		"FROM_UNIXTIME(9223372037)",
		"'not an epoch'",
	} {
		if _, err := parseBound(parse(source), true); err == nil {
			t.Errorf("parseBound(%q) succeeded", source)
		}
	}
}

func TestAnalyzerFloorBucketAndCompleteResultShape(t *testing.T) {
	query := "SELECT FLOOR(UNIX_TIMESTAMP(`e`.`when`) / 300) * 300 AS `time`, " +
		"`e`.`group` AS `metric`, `e`.`region` AS `zone`, " +
		"COUNT(*) AS `trips`, AVG(`e`.`value`) AS `mean_value` " +
		"FROM `analytics`.`events` AS `e` " +
		"WHERE `e`.`when` >= FROM_UNIXTIME(1785542400) " +
		"AND `e`.`when` < FROM_UNIXTIME(1785628800) " +
		"GROUP BY 1, 2, 3 ORDER BY 1, 2, 3"
	analysis := MustNewAnalyzer().Analyze(query, time.Time{})
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		t.Fatalf("Analyze() = %s/%s (%v)", analysis.Mode, analysis.Reason, analysis.Err)
	}
	plan := analysis.Plan
	if plan.Step != 5*time.Minute || plan.Phase != 0 || plan.TimeColumn != "when" ||
		plan.OutputColumn != "time" ||
		!slices.Equal(plan.GroupColumns, []string{"metric", "zone"}) ||
		!slices.Equal(plan.ValueColumns, []string{"trips", "mean_value"}) {
		t.Fatalf("unexpected FLOOR plan: %+v", plan)
	}
}

func TestAnalyzerRejectsUnsafeBucketAndResultShapes(t *testing.T) {
	base := func(bucket, outputs, group, order string) string {
		return "SELECT " + bucket + " AS time, " + outputs +
			" FROM events WHERE ts >= FROM_UNIXTIME(1785542400)" +
			" AND ts < FROM_UNIXTIME(1785628800) GROUP BY " + group + " ORDER BY " + order
	}
	tests := []struct {
		name, query string
		reason      sqlanalyzer.AnalysisReason
	}{
		{
			"dynamic floor cadence",
			base("FLOOR(UNIX_TIMESTAMP(ts) / cadence) * cadence", "COUNT(*) AS value", "time", "time"),
			sqlanalyzer.ReasonUnsupportedBucket,
		},
		{
			"zero cadence",
			base("FLOOR(UNIX_TIMESTAMP(ts) / 0) * 0", "COUNT(*) AS value", "time", "time"),
			sqlanalyzer.ReasonUnsupportedBucket,
		},
		{
			"mismatched cadence",
			base("FLOOR(UNIX_TIMESTAMP(ts) / 300) * 600", "COUNT(*) AS value", "time", "time"),
			sqlanalyzer.ReasonUnsupportedBucket,
		},
		{
			"overflow cadence",
			base("FLOOR(UNIX_TIMESTAMP(ts) / 9223372037) * 9223372037", "COUNT(*) AS value", "time", "time"),
			sqlanalyzer.ReasonUnsupportedBucket,
		},
		{
			"nonzero phase",
			base("FLOOR((UNIX_TIMESTAMP(ts) - 60) / 300) * 300 + 60", "COUNT(*) AS value", "time", "time"),
			sqlanalyzer.ReasonUnsupportedBucket,
		},
		{
			"ambiguous timestamp outputs",
			"SELECT UNIX_TIMESTAMP(ts) DIV 300 * 300 AS time, " +
				"UNIX_TIMESTAMP(other_ts) DIV 300 * 300 AS other_time, COUNT(*) AS value " +
				"FROM events WHERE ts >= FROM_UNIXTIME(1785542400) AND ts < FROM_UNIXTIME(1785628800) " +
				"GROUP BY time ORDER BY time",
			sqlanalyzer.ReasonUnsupportedBucket,
		},
		{
			"duplicate output alias",
			base("UNIX_TIMESTAMP(ts) DIV 300 * 300", "COUNT(*) AS time", "time", "time"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{
			"ungrouped raw value",
			base("UNIX_TIMESTAMP(ts) DIV 300 * 300", "value", "time", "time"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{
			"window value",
			base("UNIX_TIMESTAMP(ts) DIV 300 * 300", "SUM(value) OVER () AS value", "time", "time"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{
			"rollup",
			base("UNIX_TIMESTAMP(ts) DIV 300 * 300", "COUNT(*) AS value", "time WITH ROLLUP", "time"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{
			"grouping sets",
			base("UNIX_TIMESTAMP(ts) DIV 300 * 300", "region, COUNT(*) AS value",
				"GROUPING SETS ((time, region), (time))", "time"),
			sqlanalyzer.ReasonInvalidSQL,
		},
		{
			"nondeterministic output",
			base("UNIX_TIMESTAMP(ts) DIV 300 * 300", "SUM(value) + RAND() AS value", "time", "time"),
			sqlanalyzer.ReasonNondeterministic,
		},
		{
			"wrong result order",
			base("UNIX_TIMESTAMP(ts) DIV 300 * 300", "region, COUNT(*) AS value", "time, region", "region, time"),
			sqlanalyzer.ReasonUnsupportedGrouping,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			analysis := MustNewAnalyzer().Analyze(tc.query, time.Time{})
			if analysis.Mode == sqlanalyzer.CacheModeDelta || analysis.Reason != tc.reason {
				t.Fatalf("Analyze() = %s/%s (%v), want non-delta/%s", analysis.Mode,
					analysis.Reason, analysis.Err, tc.reason)
			}
		})
	}
}

func TestAnalyzerCanonicalizesRangeAndRendersExtent(t *testing.T) {
	a := MustNewAnalyzer()
	first := a.Analyze(safeDateTimeQuery, time.Time{})
	second := a.Analyze(strings.Replace(safeDateTimeQuery,
		"1785542400", "1785456000", 1), time.Time{})
	if first.Plan == nil || second.Plan == nil {
		t.Fatal("expected delta plans")
	}
	if first.Plan.CanonicalSQL != second.Plan.CanonicalSQL {
		t.Fatalf("range-independent keys differ:\n%s\n%s", first.Plan.CanonicalSQL,
			second.Plan.CanonicalSQL)
	}
	if !strings.Contains(first.Plan.CanonicalSQL, ":__trickster_mysql_lower") ||
		!strings.Contains(first.Plan.CanonicalSQL, ":__trickster_mysql_upper") {
		t.Fatalf("canonical SQL lacks range tokens: %s", first.Plan.CanonicalSQL)
	}

	start := time.Unix(1785553200, 0).UTC()
	end := start.Add(10 * time.Minute)
	rendered, err := first.Plan.RenderExtent(timeseries.Extent{Start: start, End: end})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "__trickster") ||
		!strings.Contains(rendered, "FROM_UNIXTIME(1785553200)") ||
		!strings.Contains(rendered, "FROM_UNIXTIME(1785554100)") {
		t.Fatalf("unexpected rendered SQL: %s", rendered)
	}
}

func TestAnalyzerRendererPreservesExclusiveComparators(t *testing.T) {
	a := MustNewAnalyzer()
	query := `SELECT cast(cast(epoch/(300) as signed)*300 as signed) AS time, count(*) FROM events WHERE epoch >= 1785542400 AND epoch < 1785628800 GROUP BY time`
	analysis := a.Analyze(query, time.Time{})
	if analysis.Plan == nil {
		t.Fatalf("Analyze() failed: %v", analysis.Err)
	}
	start := time.Unix(1785542700, 0).UTC()
	end := time.Unix(1785543300, 0).UTC()
	rendered, err := analysis.Plan.RenderExtent(timeseries.Extent{Start: start, End: end})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "epoch >= 1785542700") ||
		!strings.Contains(rendered, "epoch < 1785543600") {
		t.Fatalf("exclusive bounds did not round-trip: %s", rendered)
	}
}

func TestAnalyzerRendererAvoidsPlaceholderCollisions(t *testing.T) {
	a := MustNewAnalyzer()
	query := strings.Replace(safeDateTimeQuery, "WHERE ",
		"WHERE cab_type = :__trickster_mysql_lower AND ", 1)
	analysis := a.Analyze(query, time.Time{})
	if analysis.Plan == nil {
		t.Fatalf("Analyze() failed: %v", analysis.Err)
	}
	if !strings.Contains(analysis.Plan.CanonicalSQL, ":__trickster_mysql_lower2") {
		t.Fatalf("canonical SQL reused a caller placeholder: %s", analysis.Plan.CanonicalSQL)
	}
	rendered, err := analysis.Plan.RenderExtent(timeseries.Extent{
		Start: time.Unix(1785542400, 0), End: time.Unix(1785542700, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "cab_type = :__trickster_mysql_lower") ||
		strings.Contains(rendered, ":__trickster_mysql_lower2") {
		t.Fatalf("rendering modified the caller placeholder: %s", rendered)
	}
}

func TestAnalyzerRendererIsConcurrent(t *testing.T) {
	plan := MustNewAnalyzer().Analyze(safeDateTimeQuery, time.Time{}).Plan
	if plan == nil {
		t.Fatal("expected a delta plan")
	}
	const renders = 32
	outputs := make([]string, renders)
	errs := make([]error, renders)
	base := time.Unix(1785542400, 0)
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
		if !strings.Contains(output, fmt.Sprintf("FROM_UNIXTIME(%d)", start.Unix())) ||
			!strings.Contains(output, fmt.Sprintf("FROM_UNIXTIME(%d)",
				start.Add(2*plan.Step).Unix())) {
			t.Fatalf("render %d used another extent: %s", i, output)
		}
	}
}

func TestAnalyzerPredicateSafetyAdversarial(t *testing.T) {
	a := MustNewAnalyzer()
	base := `SELECT UNIX_TIMESTAMP(ts) DIV 60 * 60 AS time, COUNT(*) AS value
FROM events WHERE %s GROUP BY time ORDER BY time`
	tests := []struct {
		name, predicate string
		mode            sqlanalyzer.CacheMode
		reason          sqlanalyzer.AnalysisReason
	}{
		{"or", `(ts >= FROM_UNIXTIME(1785542400) OR tenant = 1) AND ts < FROM_UNIXTIME(1785628800)`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"not", `NOT(ts >= FROM_UNIXTIME(1785542400)) AND ts < FROM_UNIXTIME(1785628800)`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"nested boolean", `ts >= FROM_UNIXTIME(1785542400) AND (ts < FROM_UNIXTIME(1785628800) OR tenant = 1)`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"reversed comparison", `FROM_UNIXTIME(1785542400) <= ts AND ts < FROM_UNIXTIME(1785628800)`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"duplicate lower", `ts >= FROM_UNIXTIME(1785542400) AND ts >= FROM_UNIXTIME(1785542460) AND ts < FROM_UNIXTIME(1785628800)`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate},
		{"multiple time axes", `ts >= FROM_UNIXTIME(1785542400) AND ts < FROM_UNIXTIME(1785628800) AND created_at >= FROM_UNIXTIME(1785542400) AND created_at < FROM_UNIXTIME(1785628800)`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonAmbiguousTimeAxis},
		{"alias in predicate", `time >= 1785542400 AND time < 1785628800`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonAmbiguousTimeAxis},
		{"safe nested non-time predicate", `ts >= FROM_UNIXTIME(1785542400) AND ts < FROM_UNIXTIME(1785628800) AND (tenant = 1 OR tenant = 2)`, sqlanalyzer.CacheModeDelta, sqlanalyzer.ReasonDeltaCacheable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := a.Analyze(fmt.Sprintf(base, tc.predicate), time.Time{})
			if got.Mode != tc.mode || got.Reason != tc.reason {
				t.Fatalf("Analyze() = %s/%s (%v), want %s/%s", got.Mode,
					got.Reason, got.Err, tc.mode, tc.reason)
			}
		})
	}
}

// TestAnalyzerFailsClosedForUninspectableReads covers the two shapes the
// analyzer cannot reason about: text it could not parse, and read-only ASTs
// that are not a plain *sqlparser.Select. Both must reach CacheModeNone before
// any of the per-statement safety checks, which only inspect a plain SELECT.
func TestAnalyzerFailsClosedForUninspectableReads(t *testing.T) {
	a := MustNewAnalyzer()
	for name, query := range map[string]string{
		// A union is uncacheable even when every branch is deterministic,
		// because nothing proves that over the whole AST today.
		"deterministic union":     "SELECT 1 UNION SELECT 2",
		"deterministic union all": "SELECT 1 UNION ALL SELECT 2",
		// These are the cases the old leading-token and read-only-AST
		// shortcuts admitted: the nondeterminism sits in a branch the
		// analyzer never reached.
		"nondeterministic union":    "SELECT 1 UNION ALL SELECT RAND()",
		"union with session read":   "SELECT 1 UNION SELECT @@sql_mode",
		"union with user variable":  "SELECT 1 UNION SELECT @report_id",
		"union with advisory lock":  "SELECT 1 UNION ALL SELECT GET_LOCK('reporting', 1)",
		"union of time-range reads": safeDateTimeQuery + " UNION ALL " + safeDateTimeQuery,
		"parenthesized union":       "(SELECT 1) UNION (SELECT RAND())",
		// Parser-rejected text that begins with SELECT proves nothing about
		// what the origin would execute.
		"parser-rejected select":      "SELECT FROM trips",
		"parser-rejected select list": "SELECT , FROM trips",
		"parser-rejected lowercase":   "select count(* from trips",
	} {
		t.Run(name, func(t *testing.T) {
			got := a.Analyze(query, time.Time{})
			if got.Mode != sqlanalyzer.CacheModeNone {
				t.Fatalf("Analyze() = %s/%s, want %s", got.Mode, got.Reason,
					sqlanalyzer.CacheModeNone)
			}
			if got.Reason == "" {
				t.Fatal("uninspectable statement has no stable analysis reason")
			}
		})
	}
}

func TestAnalyzerRejectsComplexStatementShapes(t *testing.T) {
	a := MustNewAnalyzer()
	tests := map[string]string{
		"scalar subquery": strings.Replace(safeDateTimeQuery, "count(*) AS trips",
			"count(*) + (SELECT 1) AS trips", 1),
		"correlated subquery": strings.Replace(safeDateTimeQuery, "FROM trips",
			"FROM trips t WHERE EXISTS (SELECT 1 FROM zones z WHERE z.id = t.zone_id) AND", 1),
		"cte":    `WITH source AS (SELECT * FROM trips) SELECT UNIX_TIMESTAMP(pickup_datetime) DIV 300 * 300 AS time, COUNT(*) AS trips FROM source WHERE pickup_datetime >= FROM_UNIXTIME(1785542400) AND pickup_datetime < FROM_UNIXTIME(1785628800) GROUP BY time`,
		"union":  safeDateTimeQuery + " UNION ALL " + safeDateTimeQuery,
		"having": safeDateTimeQuery + " HAVING COUNT(*) > 0",
	}
	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			got := a.Analyze(query, time.Time{})
			if got.Mode == sqlanalyzer.CacheModeDelta {
				t.Fatalf("unsafe statement was DPC: %+v", got.Plan)
			}
			if got.Reason == "" {
				t.Fatal("unsafe statement has no stable analysis reason")
			}
		})
	}
}

func TestAnalyzerBoundOperatorMatrix(t *testing.T) {
	a := MustNewAnalyzer()
	styles := []struct {
		name, bucket, axis, lower, upper string
	}{
		{"datetime", `UNIX_TIMESTAMP(ts) DIV 60 * 60`, "ts", `FROM_UNIXTIME(1785542400)`, `FROM_UNIXTIME(1785628800)`},
		{"epoch seconds", `epoch DIV 60 * 60`, "epoch", `1785542400`, `1785628800`},
		{"epoch nanoseconds", `epoch_ns DIV 60000000000 * 60000000000`, "epoch_ns", `1785542400000000000`, `1785628800000000000`},
	}
	operators := []string{"<", "<=", ">", ">=", "="}
	for _, style := range styles {
		for _, operator := range operators {
			for _, reversed := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/reversed=%t", style.name, operator, reversed)
				t.Run(name, func(t *testing.T) {
					left, right := style.axis, style.lower
					if reversed {
						left, right = right, left
					}
					predicate := fmt.Sprintf("%s %s %s AND %s < %s",
						left, operator, right, style.axis, style.upper)
					query := fmt.Sprintf(`SELECT %s AS time, COUNT(*) AS value FROM events WHERE %s GROUP BY time ORDER BY time`, style.bucket, predicate)
					got := a.Analyze(query, time.Time{})
					wantDelta := !reversed && operator == ">="
					if (got.Mode == sqlanalyzer.CacheModeDelta) != wantDelta {
						t.Fatalf("Analyze() = %s/%s (%v), want delta=%t", got.Mode,
							got.Reason, got.Err, wantDelta)
					}
				})
			}
		}
		t.Run(style.name+"/between", func(t *testing.T) {
			query := fmt.Sprintf(`SELECT %s AS time, COUNT(*) AS value FROM events WHERE %s BETWEEN %s AND %s GROUP BY time ORDER BY time`, style.bucket, style.axis, style.lower, style.upper)
			if got := a.Analyze(query, time.Time{}); got.Mode == sqlanalyzer.CacheModeDelta {
				t.Fatalf("BETWEEN was DPC: %+v", got.Plan)
			}
		})
		t.Run(style.name+"/reversed-between", func(t *testing.T) {
			query := fmt.Sprintf(`SELECT %s AS time, COUNT(*) AS value FROM events WHERE %s BETWEEN %s AND %s AND %s < %s GROUP BY time ORDER BY time`, style.bucket, style.lower, style.axis, style.upper, style.axis, style.upper)
			if got := a.Analyze(query, time.Time{}); got.Mode == sqlanalyzer.CacheModeDelta {
				t.Fatalf("reversed BETWEEN was DPC: %+v", got.Plan)
			}
		})
	}
	for _, predicate := range []string{
		"time >= 1785542400 AND time < 1785628800",
		"1785542400 <= time AND time < 1785628800",
		"time BETWEEN 1785542400 AND 1785628800",
	} {
		t.Run("bucket output/"+predicate, func(t *testing.T) {
			query := fmt.Sprintf(`SELECT epoch DIV 60 * 60 AS time, COUNT(*) AS value FROM events WHERE %s GROUP BY time ORDER BY time`, predicate)
			if got := a.Analyze(query, time.Time{}); got.Mode == sqlanalyzer.CacheModeDelta {
				t.Fatalf("bucket-output predicate was DPC: %+v", got.Plan)
			}
		})
	}
}

func TestAnalyzeResultShapeRejectsEmptyGroupByWithoutPanicking(t *testing.T) {
	a := MustNewAnalyzer()
	statement, err := a.Parser().Parse(safeDateTimeQuery)
	if err != nil {
		t.Fatal(err)
	}
	selectStatement := statement.(*sqlparser.Select)
	bucket, err := analyzeBucket(selectStatement)
	if err != nil {
		t.Fatal(err)
	}
	selectStatement.GroupBy.Exprs = nil
	if _, _, err = analyzeResultShape(selectStatement, bucket); !errors.Is(err,
		ErrUnsupportedResultShape) {
		t.Fatalf("empty GROUP BY error = %v, want %v", err, ErrUnsupportedResultShape)
	}
}

func TestParserExpressionHelperMatrix(t *testing.T) {
	parser := MustNewAnalyzer().Parser()
	parse := func(source string) sqlparser.Expr {
		t.Helper()
		expr, err := parser.ParseExpr(source)
		if err != nil {
			t.Fatal(err)
		}
		return expr
	}
	for _, tc := range []struct {
		expression         string
		safe, hasAggregate bool
	}{
		{"42", true, false},
		{"3.14", true, false},
		{"COUNT(*)", true, true},
		{"STDDEV(value)", false, false},
		{"COUNT(*) + 1", true, true},
		{"-SUM(value)", true, true},
		{"ROUND(COALESCE(SUM(value), 0), 2)", true, true},
		{"COALESCE(SUM(value), name)", false, false},
		{"ABS(SUM(value))", false, false},
		{"value", false, false},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			safe, aggregate := numericValueExpression(parse(tc.expression))
			if safe != tc.safe || aggregate != tc.hasAggregate {
				t.Fatalf("numericValueExpression() = %t/%t", safe, aggregate)
			}
		})
	}
	for _, tc := range []struct {
		expression string
		want       int64
		ok         bool
	}{
		{"+5", 5, true},
		{"-5", -5, true},
		{"'5'", 0, false},
		{"name", 0, false},
		{"~5", 0, false},
		{"9223372036854775808", 1<<63 - 1, false},
	} {
		got, ok := intLiteral(parse(tc.expression))
		if got != tc.want || ok != tc.ok {
			t.Fatalf("intLiteral(%q) = %d/%t", tc.expression, got, ok)
		}
	}
	outputs := []selectOutput{{alias: "bucket", sourceName: "time", sourceAxis: "events.time"},
		{sourceName: "value", sourceAxis: "events.value"}}
	for _, expression := range []string{"0", "3", "unknown", "value + 1"} {
		if _, ok := resolveOutputReference(parse(expression), outputs); ok {
			t.Fatalf("invalid output reference %q resolved", expression)
		}
	}
}

func TestColumnReferenceUsesStructuralAxis(t *testing.T) {
	parser := MustNewAnalyzer().Parser()
	qualified, err := parser.ParseExpr("Analytics.Events.TS")
	if err != nil {
		t.Fatal(err)
	}
	name, axis, ok := columnReference(qualified)
	if !ok || name != "TS" || axis != "analytics\x00events\x00ts" {
		t.Fatalf("qualified column = %q/%q/%t", name, axis, ok)
	}
	quoted, err := parser.ParseExpr("`Analytics.Events.TS`")
	if err != nil {
		t.Fatal(err)
	}
	_, quotedAxis, ok := columnReference(quoted)
	if !ok || quotedAxis == axis {
		t.Fatalf("structurally distinct column axes collided: %q", quotedAxis)
	}
}

func BenchmarkAnalyzerGrafanaQuery(b *testing.B) {
	a := MustNewAnalyzer()
	for b.Loop() {
		_ = a.Analyze(grafanaDateTimeQuery, time.Time{})
	}
}

func BenchmarkRenderGrafanaExtent(b *testing.B) {
	a := MustNewAnalyzer()
	plan := a.Analyze(safeDateTimeQuery, time.Time{}).Plan
	extent := timeseries.Extent{Start: time.Unix(1785542400, 0), End: time.Unix(1785546000, 0)}
	for b.Loop() {
		if _, err := plan.RenderExtent(extent); err != nil {
			b.Fatal(err)
		}
	}
}
