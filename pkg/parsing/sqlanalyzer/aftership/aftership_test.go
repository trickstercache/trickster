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

package aftership

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	chast "github.com/AfterShip/clickhouse-sql-parser/parser"
)

const tq00 = `/* this tests a multi-line comment at the front, where the query continues after` +
	`, and on the same line as, the comment closing delimiter
  also, here we test: trickster-volatile-window:30 */ WITH  'igor * 31 + \' dks( k )'  as  igor, 3600 as x ` +
	` SELECT (  intDiv(toUInt32(datetime), x) * x) * 1000 as t, apple,` +
	` count() as cnt FROM test_db.test_table PREWHERE some_column = 'myvalue' WHERE datetime >= 1589904000 AND datetime < 1589997600` +
	` GROUP BY t, apple ORDER BY  t FORMAT TabSeparatedWithNamesAndTypes // test comment
	// test 2 comment`

const tq01 = `SELECT toStartOfFiveMinute(datetime) AS t, count() AS cnt ` +
	`FROM test_db.test_table WHERE datetime >= 1589904000 ` +
	`GROUP BY t ORDER BY t FORMAT TabSeparatedWithNamesAndTypes`

const tq02 = `SELECT toStartOfInterval(datetime, INTERVAL 60 second) AS t, x, count() AS cnt ` +
	`FROM test_db.test_table WHERE datetime >= 1589904000 AND datetime < 1589997600 ` +
	`GROUP BY t, x ORDER BY t FORMAT TabSeparatedWithNamesAndTypes`

const tq03 = `SELECT (intDiv(toUInt32(time_column), 60) * 60) * 1000 AS t, countMerge(some_count) AS cnt, field1, field2 ` +
	`FROM testdb.test_table WHERE time_column >= toDateTime(1516665600) AND time_column < toDateTime(1516687200) ` +
	`AND date_column >= toDate(1516665600) AND date_column <= toDate(1516687200) ` +
	`AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t FORMAT JSON`

const tq04 = `SELECT toStartOfFiveMinute(datetime) AS t, count() AS cnt, testfield1, testfield2 ` +
	`FROM (SELECT * FROM test_db.test_table WHERE x = 1) WHERE datetime >= 1589904000 ` +
	`GROUP BY t, testfield1, testfield2 ORDER BY t DESC FORMAT TabSeparatedWithNamesAndTypes`

const tq05 = `SELECT toStartOfFiveMinute(datetime) AS t, count() AS cnt FROM test_db.test_table ` +
	`WHERE t > '2020-01-01 00:00:00' AND t < now() - 300 ` +
	`GROUP BY t ORDER BY t FORMAT JSON`

const tq07 = `SELECT (intDiv(toUInt32(datetime), 300) * 300) * 1000 AS t, count() AS cnt ` +
	`FROM test_db.test_table WHERE datetime >= 1589904000 AND datetime < 1589997600 ` +
	`GROUP BY t ORDER BY t FORMAT JSON`

const tq08 = `SELECT (intDiv(toUInt32(datetime), 300) * 300) * 1000 AS t, count() AS cnt ` +
	`FROM test_db.test_table WHERE datetime >= 1699999200 ` +
	`GROUP BY t ORDER BY t FORMAT JSON`

func TestAnalyzeSupportedCorpus(t *testing.T) {
	tests := []struct {
		name  string
		query string
		step  time.Duration
		phase time.Duration
	}{
		{"with constant", tq00, time.Hour, 0},
		{"fixed bucket", tq01, 5 * time.Minute, 0},
		{"interval bucket", tq02, time.Minute, 0},
		{"intDiv and secondary range", tq03, time.Minute, 0},
		{"relative upper", tq05, 5 * time.Minute, 0},
		{"intDiv multiplier", tq07, 5 * time.Minute, 0},
		{"relative lower and open upper", tq08, 5 * time.Minute, 0},
		{
			"bucket output alias in range",
			`SELECT toStartOfFiveMinute(datetime) AS t, count() AS cnt FROM test_db.test_table ` +
				`WHERE t > '2020-05-30 11:00:00' AND t < now() - 300 GROUP BY t FORMAT JSON`,
			5 * time.Minute,
			0,
		},
		{
			"nonconstant WITH expression",
			`WITH dictGetString('test_cache', server, xxHash64(server)) AS server_name ` +
				`SELECT toStartOfFiveMinute(datetime) AS t, count() AS cnt FROM test_db.test_table ` +
				`WHERE t > '2020-05-30 11:00:00' AND t < now() - 300 GROUP BY t FORMAT JSON`,
			5 * time.Minute,
			0,
		},
		{
			"cast around bucket",
			`SELECT toInt32(toStartOfFiveMinute(datetime)) AS t, count() AS cnt FROM test_db.test_table ` +
				`WHERE t > '2020-05-30 11:00:00' AND t < now() - 300 GROUP BY t FORMAT JSON`,
			5 * time.Minute,
			0,
		},
		{
			"monday phase",
			`SELECT toMonday(datetime) AS t, count() FROM events ` +
				`WHERE datetime >= 1589760000 AND datetime < 1590364800 GROUP BY t`,
			week,
			4 * day,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := NewAnalyzer(Options{}).Analyze(test.query, time.Unix(1_700_000_000, 0))
			if analysis.Err != nil {
				t.Fatalf("Analyze() error = %v", analysis.Err)
			}
			if analysis.Mode != sqlanalyzer.CacheModeDelta {
				t.Fatalf("mode = %v, want delta", analysis.Mode)
			}
			if analysis.Plan.Step != test.step {
				t.Errorf("step = %s, want %s", analysis.Plan.Step, test.step)
			}
			if analysis.Plan.Phase != test.phase {
				t.Errorf("phase = %s, want %s", analysis.Plan.Phase, test.phase)
			}
			if !strings.Contains(analysis.Plan.CanonicalSQL, "<$TS1$>") ||
				!strings.Contains(analysis.Plan.CanonicalSQL, "<$TS2$>") {
				t.Errorf("canonical SQL does not contain both placeholders: %s", analysis.Plan.CanonicalSQL)
			}
		})
	}
}

func TestSupportedCorpusFormatIsStable(t *testing.T) {
	for i, query := range []string{tq00, tq01, tq02, tq03, tq04, tq05, tq07, tq08} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			first, _, err := parseSelect(query)
			if err != nil {
				t.Fatal(err)
			}
			formatted := chast.Format(first)
			second, _, err := parseSelect(formatted)
			if err != nil {
				t.Fatalf("formatted SQL did not parse: %v\n%s", err, formatted)
			}
			if reformatted := chast.Format(second); reformatted != formatted {
				t.Errorf("format was not stable:\n%s\n%s", formatted, reformatted)
			}
		})
	}
}

func TestBucketOutputUnits(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  timeseries.FieldDataType
	}{
		{"fixed DateTime bucket", tq01, timeseries.DateTimeSQL},
		{"interval DateTime bucket", tq02, timeseries.DateTimeSQL},
		{
			"Date bucket",
			`SELECT toMonday(datetime) AS t, count() FROM events ` +
				`WHERE datetime >= 1589760000 AND datetime < 1590364800 GROUP BY t`,
			timeseries.DateSQL,
		},
		{
			"integer cast",
			`SELECT toInt32(toStartOfFiveMinute(datetime)) AS t, count() FROM events ` +
				`WHERE datetime >= 1589904000 AND datetime < 1589997600 GROUP BY t`,
			timeseries.DateTimeUnixSecs,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := NewAnalyzer(Options{}).Analyze(test.query, time.Unix(1_700_000_000, 0))
			if analysis.Err != nil {
				t.Fatal(analysis.Err)
			}
			if analysis.Plan.OutputUnit != test.want {
				t.Errorf("output unit = %v, want %v", analysis.Plan.OutputUnit, test.want)
			}
		})
	}
}

func TestOutputAliasRangeUsesBucketUnit(t *testing.T) {
	query := `SELECT (intDiv(toUInt32(ts), 300) * 300) * 1000 AS t, count() FROM events ` +
		`WHERE t >= 1516665600000 AND t < 1516687200000 GROUP BY t FORMAT JSON`
	analysis := NewAnalyzer(Options{}).Analyze(query, time.Unix(1_700_000_000, 0))
	if analysis.Err != nil {
		t.Fatal(analysis.Err)
	}
	if got := analysis.Plan.LowerBound.Value.Unix(); got != 1516665600 {
		t.Errorf("lower bound = %d, want 1516665600", got)
	}
	if analysis.Plan.InputUnit != timeseries.DateTimeUnixMilli {
		t.Errorf("input unit = %v, want milliseconds", analysis.Plan.InputUnit)
	}
	rendered, err := analysis.Plan.RenderExtent(timeseries.Extent{
		Start: time.Unix(1516669200, 0), End: time.Unix(1516672800, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "t >= 1516669200000") ||
		!strings.Contains(rendered, "t < 1516673100000") {
		t.Errorf("millisecond bounds were not preserved: %s", rendered)
	}
}

func TestRenderExtentPreservesPredicateTopology(t *testing.T) {
	query := `SELECT toStartOfMinute(ts) AS t, count() FROM events ` +
		`WHERE tenant_start = 100 AND ts >= 120 AND ts < 240 GROUP BY t FORMAT JSON`
	analysis := NewAnalyzer(Options{}).Analyze(query, time.Unix(500, 0))
	if analysis.Err != nil {
		t.Fatal(analysis.Err)
	}
	rendered, err := analysis.Plan.RenderExtent(timeseries.Extent{
		Start: time.Unix(120, 0), End: time.Unix(180, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "tenant_start = 100") {
		t.Errorf("unrelated literal was modified: %s", rendered)
	}
	if !strings.Contains(rendered, "ts >= 120") || !strings.Contains(rendered, "ts < 240") {
		t.Errorf("comparators were not preserved: %s", rendered)
	}
	if strings.Contains(strings.ToUpper(rendered), " BETWEEN ") {
		t.Errorf("separate comparisons were synthesized into BETWEEN: %s", rendered)
	}
	if !strings.Contains(rendered, "FORMAT TSVWithNamesAndTypes") {
		t.Errorf("origin format was not forced: %s", rendered)
	}
}

func TestOpenEndedRangeAddsSafeUpperConjunct(t *testing.T) {
	analysis := NewAnalyzer(Options{}).Analyze(tq01, time.Unix(1_700_000_000, 0))
	if analysis.Err != nil {
		t.Fatal(analysis.Err)
	}
	rendered, err := analysis.Plan.RenderExtent(timeseries.Extent{
		Start: time.Unix(1589904300, 0), End: time.Unix(1589904600, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "datetime >= 1589904300") ||
		!strings.Contains(rendered, "datetime < 1589904900") {
		t.Errorf("open range was not safely bounded: %s", rendered)
	}
}

func TestStatementClassification(t *testing.T) {
	nonSelect := NewAnalyzer(Options{}).Analyze(`INSERT INTO events VALUES (1)`, time.Now())
	if nonSelect.Mode != sqlanalyzer.CacheModeNone || nonSelect.Reason != sqlanalyzer.ReasonUnsupportedStatement {
		t.Errorf("unexpected non-select classification: %+v", nonSelect)
	}
	insertSelect := NewAnalyzer(Options{}).Analyze(`INSERT INTO archive SELECT * FROM events`, time.Now())
	if insertSelect.Mode != sqlanalyzer.CacheModeNone || insertSelect.Reason != sqlanalyzer.ReasonUnsupportedStatement {
		t.Errorf("unexpected INSERT SELECT classification: %+v", insertSelect)
	}
	invalid := NewAnalyzer(Options{}).Analyze(`SELECT !!!`, time.Now())
	if invalid.Mode != sqlanalyzer.CacheModeObject || invalid.Reason != sqlanalyzer.ReasonInvalidSQL ||
		!errors.Is(invalid.Err, ErrInvalidSQL) {
		t.Errorf("unexpected invalid SELECT classification: %+v", invalid)
	}
	union := NewAnalyzer(Options{}).Analyze(`SELECT 1 UNION ALL SELECT 2`, time.Now())
	if union.Mode != sqlanalyzer.CacheModeObject || union.Reason != sqlanalyzer.ReasonUnsupportedStatement {
		t.Errorf("unexpected UNION classification: %+v", union)
	}
	multiple := NewAnalyzer(Options{}).Analyze(`SELECT 1; SELECT 2`, time.Now())
	if multiple.Mode != sqlanalyzer.CacheModeObject || multiple.Reason != sqlanalyzer.ReasonInvalidSQL {
		t.Errorf("unexpected multi-statement classification: %+v", multiple)
	}
}

const (
	shapeSelect = "SELECT toStartOfMinute(ts) AS t, host, count() AS c FROM events "
	shapeFilter = "WHERE ts >= 120 AND ts < 240 "
	shapeGroup  = "GROUP BY t, host"
	shapeQuery  = shapeSelect + shapeFilter + shapeGroup
)

func TestAnalyzeRefusesCrossBucketShapes(t *testing.T) {
	selectWith := func(expr string) string {
		return "SELECT toStartOfMinute(ts) AS t, host, " + expr + " AS c FROM events " + shapeFilter + shapeGroup
	}
	fromSource := func(source string) string {
		return "SELECT toStartOfMinute(ts) AS t, host, count() AS c FROM " + source + " " + shapeFilter + shapeGroup
	}
	tests := []struct {
		name   string
		query  string
		reason sqlanalyzer.AnalysisReason
	}{
		{"TOP", "SELECT TOP 5 toStartOfMinute(ts) AS t, host, count() AS c FROM events " +
			shapeFilter + shapeGroup, sqlanalyzer.ReasonUnsupportedLimit},
		{"TOP WITH TIES", "SELECT TOP 5 WITH TIES toStartOfMinute(ts) AS t, host, count() AS c FROM events " +
			shapeFilter + shapeGroup + " ORDER BY t", sqlanalyzer.ReasonUnsupportedLimit},
		{
			"inline window", selectWith("sum(count()) OVER (PARTITION BY host ORDER BY t)"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{
			"named window", selectWith("sum(count()) OVER w") + " WINDOW w AS (ORDER BY t)",
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{"unused WINDOW clause", shapeQuery + " WINDOW w AS (ORDER BY t)", sqlanalyzer.ReasonUnsupportedFormat},
		{
			"window nested in expression",
			selectWith("round(avg(count()) OVER (ORDER BY t ROWS BETWEEN 2 PRECEDING AND CURRENT ROW), 2)"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{"runningDifference", selectWith("runningDifference(count())"), sqlanalyzer.ReasonUnsupportedFormat},
		{
			"nested lowercase runningdifference", selectWith("abs(runningdifference(count()))"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{
			"runningDifferenceStartingWithFirstValue", selectWith("runningDifferenceStartingWithFirstValue(count())"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{
			"uppercase runningAccumulate", selectWith("RUNNINGACCUMULATE(countState())"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{
			"other running function", selectWith("runningConcurrency(min(ts), max(ts))"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{"neighbor", selectWith("neighbor(count(), -1)"), sqlanalyzer.ReasonUnsupportedFormat},
		{"lagInFrame", selectWith("lagInFrame(count(), 1)"), sqlanalyzer.ReasonUnsupportedFormat},
		{"leadInFrame", selectWith("toFloat64(LEADINFRAME(count(), 1))"), sqlanalyzer.ReasonUnsupportedFormat},
		{"rowNumberInAllBlocks", selectWith("rowNumberInAllBlocks()"), sqlanalyzer.ReasonUnsupportedFormat},
		{
			"cross-row function in WHERE", shapeSelect + shapeFilter + "AND neighbor(v, 1) > 0 " + shapeGroup,
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{
			"cross-row function in HAVING", shapeQuery + " HAVING runningDifference(count()) > 0",
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{"WITH TOTALS", shapeQuery + " WITH TOTALS", sqlanalyzer.ReasonUnsupportedFormat},
		{"WITH TOTALS without GROUP BY", shapeSelect + shapeFilter + "WITH TOTALS", sqlanalyzer.ReasonUnsupportedFormat},
		{"WITH FILL", shapeQuery + " ORDER BY t WITH FILL STEP 60", sqlanalyzer.ReasonUnsupportedFormat},
		{
			"WITH FILL INTERPOLATE", shapeQuery + " ORDER BY t WITH FILL INTERPOLATE (c AS c)",
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{"SETTINGS", shapeQuery + " SETTINGS max_threads = 1", sqlanalyzer.ReasonUnsupportedFormat},
		{
			"SETTINGS before FORMAT", shapeQuery + " SETTINGS max_threads = 1 FORMAT JSON",
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{"FROM subquery", tq04, sqlanalyzer.ReasonUnsupportedFormat},
		{
			"IN subquery", shapeSelect + shapeFilter + "AND host IN (SELECT host FROM hosts) " + shapeGroup,
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{"GLOBAL IN subquery", shapeSelect + shapeFilter + "AND host GLOBAL IN (SELECT host FROM hosts) " +
			shapeGroup, sqlanalyzer.ReasonUnsupportedFormat},
		{
			"scalar subquery in select list", selectWith("count() / (SELECT max(v) FROM limits)"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{
			"CTE subquery", "WITH f AS (SELECT ts, host FROM events WHERE ts >= 180) " + fromSource("f"),
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{
			"scalar WITH subquery", "WITH (SELECT max(ts) FROM events) AS m " + shapeQuery,
			sqlanalyzer.ReasonUnsupportedFormat,
		},
		{"JOIN ON", fromSource("events JOIN hosts ON events.host = hosts.host"), sqlanalyzer.ReasonUnsupportedFormat},
		{"LEFT JOIN USING", fromSource("events LEFT JOIN hosts USING (host)"), sqlanalyzer.ReasonUnsupportedFormat},
		{"comma join", fromSource("events, hosts"), sqlanalyzer.ReasonUnsupportedFormat},
		{"ORDER BY DESC", shapeQuery + " ORDER BY t DESC", sqlanalyzer.ReasonUnsupportedOrdering},
		{
			"ORDER BY bucket expression DESC", shapeQuery + " ORDER BY toStartOfMinute(ts) DESC",
			sqlanalyzer.ReasonUnsupportedOrdering,
		},
		{"ORDER BY ordinal DESC", shapeQuery + " ORDER BY 1 DESC", sqlanalyzer.ReasonUnsupportedOrdering},
		{"ORDER BY multiple terms", shapeQuery + " ORDER BY t, host", sqlanalyzer.ReasonUnsupportedOrdering},
		{"ORDER BY tag", shapeQuery + " ORDER BY host", sqlanalyzer.ReasonUnsupportedOrdering},
		{"ORDER BY aggregate", shapeQuery + " ORDER BY c", sqlanalyzer.ReasonUnsupportedOrdering},
		{"ORDER BY raw time column", shapeQuery + " ORDER BY ts", sqlanalyzer.ReasonUnsupportedOrdering},
		{"ORDER BY ordinal of tag", shapeQuery + " ORDER BY 2", sqlanalyzer.ReasonUnsupportedOrdering},
		{"ORDER BY ordinal out of range", shapeQuery + " ORDER BY 9", sqlanalyzer.ReasonUnsupportedOrdering},
		{"ORDER BY aliased term", shapeQuery + " ORDER BY t AS x", sqlanalyzer.ReasonUnsupportedOrdering},
		{
			"ORDER BY ordinal after column expansion",
			"SELECT any(COLUMNS('^h')), toStartOfMinute(ts) AS t, count() AS c FROM events " +
				shapeFilter + "GROUP BY t ORDER BY 2", sqlanalyzer.ReasonUnsupportedOrdering,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := NewAnalyzer(Options{}).Analyze(test.query, time.Unix(1_700_000_000, 0))
			if analysis.Mode != sqlanalyzer.CacheModeObject || analysis.Reason != test.reason {
				t.Fatalf("analysis = (%s, %s, %v), want (object, %s)",
					analysis.Mode.String(), analysis.Reason, analysis.Err, test.reason)
			}
			if analysis.Err == nil || analysis.Plan != nil {
				t.Fatalf("refusal must carry an error and no plan: %+v", analysis)
			}
		})
	}
}

func TestAnalyzeOrderingStaysDelta(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"no ORDER BY", shapeQuery},
		{"ORDER BY bucket alias", shapeQuery + " ORDER BY t"},
		{"ORDER BY bucket alias ASC", shapeQuery + " ORDER BY t ASC"},
		{"ORDER BY parenthesized alias", shapeQuery + " ORDER BY (t)"},
		{"ORDER BY first ordinal", shapeQuery + " ORDER BY 1"},
		{"ORDER BY bucket expression", shapeQuery + " ORDER BY toStartOfMinute(ts)"},
		{
			"ORDER BY unaliased bucket expression",
			"SELECT toStartOfMinute(ts), host, count() FROM events " + shapeFilter +
				"GROUP BY toStartOfMinute(ts), host ORDER BY toStartOfMinute(ts)",
		},
		{
			"ORDER BY later ordinal",
			"SELECT host, toStartOfMinute(ts) AS t, count() AS c FROM events " + shapeFilter + shapeGroup + " ORDER BY 2",
		},
		{
			"ORDER BY Grafana time alias",
			"SELECT toStartOfInterval(time, INTERVAL 60 second) AS time, count() AS c FROM events " +
				"WHERE time >= toDateTime(120) AND time < toDateTime(240) GROUP BY time ORDER BY time",
		},
		{"ARRAY JOIN", "SELECT toStartOfMinute(ts) AS t, tag, count() AS c FROM events ARRAY JOIN tags AS tag " +
			shapeFilter + "GROUP BY t, tag ORDER BY t"},
		{
			"columns named like cross-row functions",
			"SELECT toStartOfMinute(ts) AS t, host, max(running_total) AS c, any(neighbor) AS n FROM events " +
				shapeFilter + shapeGroup,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := NewAnalyzer(Options{}).Analyze(test.query, time.Unix(1_700_000_000, 0))
			if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil || analysis.Err != nil {
				t.Fatalf("analysis = (%s, %s, %v), want delta",
					analysis.Mode.String(), analysis.Reason, analysis.Err)
			}
		})
	}
}

func TestNativeQueryCompatibility(t *testing.T) {
	now := time.Unix(1800, 0)
	for _, query := range []string{
		"SELECT date_trunc('minute', ts) AS t, count() FROM events WHERE ts >= 120 AND ts < 240 GROUP BY t FORMAT Native",
		"SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= toDateTime64(120, 3) AND ts < toDateTime64(240, 3) GROUP BY t",
		"SELECT timeSlot(ts) AS t, count() FROM events WHERE ts >= 0 AND ts < now64() GROUP BY t",
		"SELECT toStartOfMillisecond(ts) AS t, count() FROM events WHERE ts >= 120 AND ts < 121 GROUP BY t",
	} {
		analysis := NewAnalyzer(Options{}).Analyze(query, now)
		if analysis.Err != nil {
			t.Fatalf("%s: %v", query, analysis.Err)
		}
		extent := timeseries.Extent{Start: time.Unix(120, 123000000), End: time.Unix(120, 456000000)}
		rendered, err := analysis.Plan.RenderExtent(extent)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(query, "Millisecond") && !strings.Contains(rendered, ".123") {
			t.Fatalf("lost subsecond bound: %s", rendered)
		}
	}
	for _, query := range []string{
		"SELECT toStartOfMonth(ts) AS t, count() FROM events WHERE ts >= 0 AND ts < 2678400 GROUP BY t",
		"SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= toDateTime64(120, 10) AND ts < 240 GROUP BY t",
		"SELECT toStartOfInterval(ts, INTERVAL 9223372036854775807 SECOND) AS t, count() FROM events WHERE ts >= 0 AND ts < 240 GROUP BY t",
	} {
		if got := NewAnalyzer(Options{}).Analyze(query, now); got.Mode == sqlanalyzer.CacheModeDelta {
			t.Fatalf("unsafe query accepted: %s", query)
		}
	}
}

func TestIsSelectQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  bool
	}{
		{"space", "SELECT col FROM t", true},
		{"tab", "SELECT\tcol FROM t", true},
		{"newline", "SELECT\ncol FROM t", true},
		{"crlf", "SELECT\r\ncol FROM t", true},
		{"lowercase", "select col from t", true},
		{"with clause", "WITH x AS (SELECT 1) SELECT col FROM t", true},
		{"comment prefix", "/* comment */ SELECT col FROM t", true},
		{"insert", "INSERT INTO t VALUES (1)", false},
		{"empty", "", false},
		{"no query", "enable_http_compression=1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSelectQuery(tt.query); got != tt.want {
				t.Errorf("isSelectQuery(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func BenchmarkClickHouseAnalyze(b *testing.B) {
	analyzer := NewAnalyzer(Options{})
	now := time.Unix(1_700_000_000, 0)
	b.ReportAllocs()
	for b.Loop() {
		analysis := analyzer.Analyze(tq03, now)
		if analysis.Err != nil {
			b.Fatal(analysis.Err)
		}
	}
}

// BenchmarkClickHouseRenderExtent measures immutable cache-miss template
// rendering independently from initial parsing and analysis.
