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

package mysql

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
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
	a := mustNewAnalyzer()
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
	a := mustNewAnalyzer()
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

func TestParseCacheModes(t *testing.T) {
	if query, cacheable, err := Parse("DELETE FROM trips", time.Time{}); query != nil || cacheable || err == nil {
		t.Errorf("non-cacheable Parse = %+v/%t/%v", query, cacheable, err)
	}
	query, cacheable, err := Parse("SELECT COUNT(*) FROM trips", time.Time{})
	if query == nil || !cacheable || err == nil || query.Statement != "SELECT COUNT(*) FROM trips" {
		t.Errorf("object-cache Parse = %+v/%t/%v", query, cacheable, err)
	}
}

func TestParserDefensiveBranches(t *testing.T) {
	parse := func(source string) sqlparser.Expr {
		t.Helper()
		expr, err := defaultAnalyzer.parser.ParseExpr(source)
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
	analysis := mustNewAnalyzer().Analyze(query, time.Time{})
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
			analysis := mustNewAnalyzer().Analyze(tc.query, time.Time{})
			if analysis.Mode == sqlanalyzer.CacheModeDelta || analysis.Reason != tc.reason {
				t.Fatalf("Analyze() = %s/%s (%v), want non-delta/%s", analysis.Mode,
					analysis.Reason, analysis.Err, tc.reason)
			}
		})
	}
}

func TestAnalyzerResolvesDimensionAliasesForMerge(t *testing.T) {
	query := `SELECT UNIX_TIMESTAMP(ts) DIV 300 * 300 AS time,
cab_type AS metric, COUNT(*) AS trips
FROM trips WHERE ts >= FROM_UNIXTIME(1785542400)
AND ts < FROM_UNIXTIME(1785628800)
GROUP BY time, cab_type ORDER BY time, metric`
	analysis := mustNewAnalyzer().Analyze(query, time.Time{})
	if analysis.Plan == nil || !slices.Equal(analysis.Plan.GroupColumns, []string{"metric"}) {
		t.Fatalf("dimension alias plan = %+v (%v)", analysis.Plan, analysis.Err)
	}
	result := &sqltypes.Result{
		Fields: []*querypb.Field{
			{Name: "time", Type: querypb.Type_INT64},
			{Name: "metric", Type: querypb.Type_VARCHAR},
			{Name: "trips", Type: querypb.Type_INT64},
		},
		Rows: [][]sqltypes.Value{{
			sqltypes.NewInt64(1785542400), sqltypes.NewVarChar("yellow"), sqltypes.NewInt64(4),
		}},
	}
	if _, err := dpcTestHandler.mergeResults([]*sqltypes.Result{result}, analysis.Plan); err != nil {
		t.Fatalf("aliased dimension could not be modeled: %v", err)
	}
	result.Fields[2] = &querypb.Field{Name: "trips", Type: querypb.Type_VARCHAR}
	result.Rows[0][2] = sqltypes.NewVarChar("not numeric")
	if _, err := dpcTestHandler.mergeResults([]*sqltypes.Result{result}, analysis.Plan); err == nil {
		t.Fatal("non-numeric value field was accepted for DPC merging")
	}
}

func TestParseUsesCanonicalCacheKey(t *testing.T) {
	query, cacheable, err := Parse(safeDateTimeQuery, time.Time{})
	if err != nil || !cacheable {
		t.Fatalf("parse() = cacheable %t, err %v", cacheable, err)
	}
	if query.ParsedQuery == nil || query.Statement != query.CacheKeyElements["query"] ||
		!strings.Contains(query.Statement, "__trickster_mysql_lower") {
		t.Fatalf("parse() did not install canonical query: %#v", query)
	}
	if query.Extent.Start.Unix() != 1785542400 || query.Extent.End.Unix() != 1785628500 {
		t.Fatalf("inclusive extent = %v", query.Extent)
	}
}

func TestParseNormalizesUnalignedOutputExtent(t *testing.T) {
	statement := `SELECT UNIX_TIMESTAMP(ts) DIV 60 * 60 AS time, count(*)
FROM events WHERE ts >= FROM_UNIXTIME(1785542407)
AND ts < FROM_UNIXTIME(1785542527) GROUP BY time ORDER BY time`
	query, cacheable, err := Parse(statement, time.Time{})
	if err != nil || !cacheable {
		t.Fatalf("Parse() = cacheable %t, err %v", cacheable, err)
	}
	if !query.Extent.Start.Equal(time.Unix(1785542460, 0)) ||
		!query.Extent.End.Equal(time.Unix(1785542460, 0)) {
		t.Fatalf("normalized extent = %v", query.Extent)
	}
}

func TestAnalyzerCanonicalizesRangeAndRendersExtent(t *testing.T) {
	a := mustNewAnalyzer()
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
	a := mustNewAnalyzer()
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
	a := mustNewAnalyzer()
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
	plan := mustNewAnalyzer().Analyze(safeDateTimeQuery, time.Time{}).Plan
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

func BenchmarkAnalyzerGrafanaQuery(b *testing.B) {
	a := mustNewAnalyzer()
	for b.Loop() {
		_ = a.Analyze(grafanaDateTimeQuery, time.Time{})
	}
}

func BenchmarkRenderGrafanaExtent(b *testing.B) {
	a := mustNewAnalyzer()
	plan := a.Analyze(safeDateTimeQuery, time.Time{}).Plan
	extent := timeseries.Extent{Start: time.Unix(1785542400, 0), End: time.Unix(1785546000, 0)}
	for b.Loop() {
		if _, err := plan.RenderExtent(extent); err != nil {
			b.Fatal(err)
		}
	}
}
