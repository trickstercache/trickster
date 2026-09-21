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

package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	// Grafana's live ranges are never aligned to the bucket cadence
	testRange     = " FROM trips WHERE ts >= '2026-09-18T08:00:03.123Z' AND ts < '2026-09-18T11:00:07.456Z'"
	testBetween   = " FROM trips WHERE ts BETWEEN '2026-09-18T08:00:03.123Z' AND '2026-09-18T11:00:07.456Z'"
	testLongRange = " FROM trips WHERE ts >= '2026-08-01T00:00:00Z' AND ts < '2026-09-18T00:00:00Z'"
	testZoneless  = " FROM trips WHERE ts >= '2026-09-18 08:00:00' AND ts < '2026-09-18 11:00:00'"
	testOpenRange = " FROM trips WHERE ts >= '2026-09-18T08:00:00Z'"
	testGrouped   = " GROUP BY 1 ORDER BY 1"
	testBucket    = "SELECT time_bucket('300.000s', ts) AS time, "
	testSelect5m  = testBucket + "count(*)"
)

var (
	testNow    = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	testExtent = timeseries.Extent{Start: testNow.Add(-2 * time.Hour), End: testNow.Add(-time.Hour)}
)

func testAnalyze(utc bool, sql string) sqlanalyzer.Analysis {
	return Engine().Analyzer().(pgwire.SessionAnalyzer).ForSession(pgwire.SessionView{UTC: utc}).Analyze(sql, testNow)
}

func TestAnalyzerBuckets(t *testing.T) {
	for name, test := range map[string]struct {
		sql    string
		step   time.Duration
		phase  time.Duration
		output string
		unit   timeseries.FieldDataType
	}{
		"grafana time_bucket": {testSelect5m + testRange + testGrouped, 5 * time.Minute, 0, "time", timeseries.DateTimeRFC3339Nano},
		"unaliased time_bucket is named for the function": {
			"SELECT time_bucket('5m', ts), count(*)" + testRange + " GROUP BY 1", 5 * time.Minute, 0, fnTimeBucket,
			timeseries.DateTimeRFC3339Nano,
		},
		"interval keyword": {
			"SELECT time_bucket(INTERVAL '1 hour', ts) AS time, count(*)" + testRange + testGrouped, time.Hour, 0, "time",
			timeseries.DateTimeRFC3339Nano,
		},
		// TimescaleDB's grid starts on Monday 2000-01-03, which is not a multiple of these widths from the epoch
		"seven days": {
			"SELECT time_bucket('7 days'::interval, ts) AS time, count(*)" + testLongRange + testGrouped, 7 * day, 4 * day, "time",
			timeseries.DateTimeRFC3339Nano,
		},
		"seven hours": {
			"SELECT time_bucket('7h', ts) AS time, count(*)" + testLongRange + testGrouped, 7 * time.Hour, 5 * time.Hour, "time",
			timeseries.DateTimeRFC3339Nano,
		},
		"typed origin": {
			"SELECT time_bucket('1h', ts, TIMESTAMPTZ '2000-01-01 00:20+00') AS time, count(*)" + testLongRange + testGrouped,
			time.Hour, 20 * time.Minute, "time", timeseries.DateTimeRFC3339Nano,
		},
		"origin before the epoch": {
			"SELECT time_bucket('1h', ts, TIMESTAMPTZ '1969-12-31T23:50:00Z') AS time, count(*)" + testLongRange + testGrouped,
			time.Hour, 50 * time.Minute, "time", timeseries.DateTimeRFC3339Nano,
		},
		"typed offset": {
			"SELECT time_bucket('1h', ts, '15m'::interval) AS time, count(*)" + testLongRange + testGrouped,
			time.Hour, 15 * time.Minute, "time", timeseries.DateTimeRFC3339Nano,
		},
		"date_bin with a zoned origin": {
			"SELECT date_bin('15 minutes', ts, '2001-01-01T00:05:00Z') AS time, count(*)" + testRange + testGrouped,
			15 * time.Minute, 5 * time.Minute, "time", timeseries.DateTimeRFC3339Nano,
		},
		"epoch floor over a timestamp": {
			`SELECT floor(extract(epoch from ts)/300)*300 AS "time", count(*)` + testBetween + testGrouped,
			5 * time.Minute, 0, "time", timeseries.DateTimeUnixSecs,
		},
		"unaliased epoch floor with the multiplier first": {
			"SELECT 600 * floor(date_part('epoch', (ts)) / 600), count(*)" + testRange + " GROUP BY 1",
			10 * time.Minute, 0, unnamedColumn, timeseries.DateTimeUnixSecs,
		},
		"epoch floor over an epoch column": {
			`SELECT floor((c)/600)*600 AS "time", avg(v) FROM m WHERE c >= 1788998400 AND c <= 1789002000` + testGrouped,
			10 * time.Minute, 0, "time", timeseries.DateTimeUnixSecs,
		},
		"single-series gapfill": {
			"SELECT time_bucket_gapfill('5m', ts) AS time, avg(v)" + testRange + testGrouped, 5 * time.Minute, 0, "time",
			timeseries.DateTimeRFC3339Nano,
		},
	} {
		// none of these depends on the session zone
		for _, utc := range []bool{true, false} {
			got := testAnalyze(utc, test.sql)
			if got.Mode != sqlanalyzer.CacheModeDelta {
				t.Fatalf("%s (utc=%t): got %v / %v / %v", name, utc, got.Mode, got.Reason, got.Err)
			}
			plan := got.Plan
			if plan.Step != test.step || plan.Phase != test.phase || plan.OutputColumn != test.output ||
				plan.OutputUnit != test.unit {
				t.Fatalf("%s: got step %v phase %v output %q unit %v", name, plan.Step, plan.Phase,
					plan.OutputColumn, plan.OutputUnit)
			}
		}
	}
}

func TestAnalyzerFailsClosed(t *testing.T) {
	for name, test := range map[string]struct {
		sql    string
		mode   sqlanalyzer.CacheMode
		reason sqlanalyzer.AnalysisReason
	}{
		"untyped third argument is the time zone overload": {
			"SELECT time_bucket('1h', ts, 'Europe/Berlin') AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"text third argument": {
			"SELECT time_bucket('1h', ts, 'UTC'::text) AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"computed origin": {
			"SELECT time_bucket('1h', ts, o::timestamptz) AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"unreadable origin": {
			"SELECT time_bucket('1h', ts, TIMESTAMPTZ 'yesterday') AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"computed width": {
			"SELECT time_bucket(w::interval, ts) AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"width of another type": {
			"SELECT time_bucket('5m'::text, ts) AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"date origin": {
			"SELECT time_bucket('1h', ts, DATE '2000-01-01') AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"signed offset": {
			"SELECT time_bucket('1h', ts, '-15m'::interval) AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"month width": {
			"SELECT time_bucket('1 month', ts) AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"integer width": {
			"SELECT time_bucket(300, ts) AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"restricted interval type": {
			"SELECT time_bucket(INTERVAL '5' MINUTE, ts) AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"computed bucket column": {
			"SELECT time_bucket('5m', ts + INTERVAL '1h') AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"date_bin without an origin": {
			"SELECT date_bin('15 minutes', ts) AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"date_bin with a date origin": {
			"SELECT date_bin('15 minutes', ts, DATE '2001-01-01') AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"date_bin with a computed width": {
			"SELECT date_bin(w, ts, '2001-01-01T00:00:00Z') AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"date_bin with a computed column": {
			"SELECT date_bin('15 minutes', lower(ts), '2001-01-01T00:00:00Z') AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"date_bin with a computed origin": {
			"SELECT date_bin('15 minutes', ts, o) AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"floor scaled by another factor": {
			"SELECT floor(extract(epoch from ts)/300)*60 AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"floor of another field": {
			"SELECT floor(extract(minute from ts)/5)*5 AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"floor of a computed epoch": {
			"SELECT floor(extract(epoch from ts + INTERVAL '1h')/300)*300 AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"floor of another function": {
			"SELECT floor(sqrt(c)/300)*300 AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"division without a floor": {
			"SELECT (c/300)*300 AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"floor of a sum": {
			"SELECT floor(c+300)*300 AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"floor over a fractional divisor": {
			"SELECT floor(c/0.5)*0.5 AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"sum rather than product": {
			"SELECT floor(c/300)+300 AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"filtered floor": {
			"SELECT floor(c/300) FILTER (WHERE true) * 300 AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		// an epoch column compared with timestamps, or in milliseconds, is not the range the bucket is over
		"epoch column with timestamp bounds": {
			"SELECT floor((ts)/600)*600 AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate,
		},
		"epoch column with millisecond bounds": {
			"SELECT floor((c)/600)*600 AS time, count(*) FROM m WHERE c >= 1788998400000 AND c < 1789002000000" + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsafePredicate,
		},
		"gapfill with explicit start and finish": {
			"SELECT time_bucket_gapfill('5m', ts, '2026-09-18T08:00:00Z', '2026-09-18T11:00:00Z') AS time, avg(v)" +
				testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"gapfill carrying values forward": {
			"SELECT time_bucket_gapfill('5m', ts) AS time, locf(avg(v))" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"gapfill interpolating": {
			"SELECT time_bucket_gapfill('5m', ts) AS time, INTERPOLATE (avg(v))" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"gapfill over several series": {
			"SELECT time_bucket_gapfill('5m', ts) AS time, host, avg(v)" + testRange + " GROUP BY 1, 2",
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		// the origin rejects this; a synthetic finish would turn its error into data
		"gapfill without an upper bound": {
			"SELECT time_bucket_gapfill('5m', ts) AS time, avg(v)" + testOpenRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
		"FROM ONLY is dropped by the parser": {
			testBucket + "count(*) FROM ONLY trips WHERE ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z'" + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat,
		},
		"json becomes jsonb": {
			testBucket + "max(doc::json ->> 'a')" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat,
		},
		"unicode escape string": {
			testBucket + "count(*)" + testRange + ` AND host = u&'\0041'` + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat,
		},
		"system_user renders like a column": {
			testBucket + "count(*) FILTER (WHERE usr = system_user)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat,
		},
		"extract with a quoted odd field": {
			testBucket + `max(extract('time zone' from ts))` + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat,
		},
		"set-returning function": {
			testBucket + "generate_series(1, 3)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedFormat,
		},
		"named arguments do not parse": {
			"SELECT time_bucket('1 day', ts, origin => '2000-01-01') AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonInvalidSQL,
		},
		"volatile function in a delta statement": {
			testBucket + "count(*)" + testRange + " AND v < Random()" + testGrouped,
			sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic,
		},
		"clock outside a time bound": {
			testBucket + "count(*)" + testRange + " AND dropoff < now()" + testGrouped,
			sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic,
		},
		"bare clock in an object statement": {"SELECT current_date", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		"clock in an object statement":      {"SELECT now()", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		"sleep":                             {"SELECT pg_sleep(30)", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		"volatile in an unparsable select": {
			"SELECT random() FROM trips GROUP BY ROLLUP (1)", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic,
		},
		"quoted volatile call": {`SELECT "random"() FROM t`, sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonNondeterministic},
		"not a read":           {"DELETE FROM trips", sqlanalyzer.CacheModeNone, sqlanalyzer.ReasonUnsupportedStatement},
		// a word is only a call when a parenthesis follows, and never inside quotes
		"guarded words as identifiers and text": {
			`SELECT random, "now", 'now()' AS locf, "only" FROM t`, sqlanalyzer.CacheModeObject, sqlanalyzer.ReasonUnsupportedBucket,
		},
	} {
		for _, utc := range []bool{true, false} {
			if got := testAnalyze(utc, test.sql); got.Mode != test.mode || got.Reason != test.reason {
				t.Fatalf("%s (utc=%t): got %v / %v / %v", name, utc, got.Mode, got.Reason, got.Err)
			}
		}
	}
}

func TestAnalyzerSessionZone(t *testing.T) {
	for name, test := range map[string]struct {
		sql    string
		reason sqlanalyzer.AnalysisReason
	}{
		// PostgreSQL truncates in the session zone and reads zone-less literals in it
		"date_trunc":             {"SELECT date_trunc('hour', ts) AS time, count(*)" + testRange + testGrouped, sqlanalyzer.ReasonUnsupportedBucket},
		"zone-less bounds":       {testSelect5m + testZoneless + testGrouped, sqlanalyzer.ReasonUnsafePredicate},
		"zone-less upper bound":  {testSelect5m + testOpenRange + " AND ts < TIMESTAMP '2026-09-18 11:00:00'" + testGrouped, sqlanalyzer.ReasonUnsafePredicate},
		"zone-less bucket start": {"SELECT time_bucket('1h', ts, TIMESTAMPTZ '2000-01-01 00:20') AS time, count(*)" + testRange + testGrouped, sqlanalyzer.ReasonUnsupportedBucket},
		"zone-less date_bin origin": {
			"SELECT date_bin('15 minutes', ts, TIMESTAMP '2001-01-01') AS time, count(*)" + testRange + testGrouped,
			sqlanalyzer.ReasonUnsupportedBucket,
		},
	} {
		if got := testAnalyze(true, test.sql); got.Mode != sqlanalyzer.CacheModeDelta {
			t.Fatalf("%s under UTC: got %v / %v / %v", name, got.Mode, got.Reason, got.Err)
		}
		zoned := testAnalyze(false, test.sql)
		if zoned.Mode != sqlanalyzer.CacheModeObject || zoned.Reason != test.reason {
			t.Fatalf("%s under a local zone: got %v / %v / %v", name, zoned.Mode, zoned.Reason, zoned.Err)
		}
		// an analyzer asked without a session cannot assume UTC
		if direct := Engine().Analyzer().Analyze(test.sql, testNow); direct.Mode != zoned.Mode {
			t.Fatalf("%s without a session: got %v", name, direct.Mode)
		}
	}
	unqualified := "SELECT date_trunc('hour', ts), count(*)" + testRange + " GROUP BY 1"
	if got := testAnalyze(true, unqualified); got.Plan == nil || got.Plan.OutputColumn != fnDateTrunc {
		t.Fatalf("an unaliased date_trunc is named for the function: %+v", got)
	}
}

func TestAnalyzerRendersPostgreSQL(t *testing.T) {
	const upper = "'2026-09-18T11:04:59.999999Z'"
	for name, test := range map[string]struct {
		sql  string
		want []string
	}{
		// PostgreSQL rounds a nanosecond literal up into the next bucket, so the tick is its own resolution
		"inclusive upper bound stops a microsecond short": {
			testSelect5m + testBetween + testGrouped, []string{"BETWEEN '2026-09-18T10:00:00Z' AND " + upper},
		},
		"less-or-equal upper bound": {
			testSelect5m + testOpenRange + " AND ts <= '2026-09-18T11:00:00Z'" + testGrouped, []string{"ts <= " + upper},
		},
		"exclusive upper bound": {testSelect5m + testRange + testGrouped, []string{"(ts < '2026-09-18T11:05:00Z')"}},
		"clock bound is resolved": {
			testSelect5m + " FROM trips WHERE ts >= now() - INTERVAL '6 hours' AND ts < NOW()" + testGrouped,
			[]string{"(ts >= '2026-09-18T10:00:00Z')", "(ts < '2026-09-18T11:05:00Z')"},
		},
		"epoch bounds stay integers": {
			`SELECT floor((c)/600)*600 AS "time", avg(v) FROM m WHERE c >= 1788998400 AND c <= 1789002000` + testGrouped,
			[]string{"(c >= 1789725600)", "(c <= 1789729799)"},
		},
		"extract keeps its keyword form": {
			testBucket + "max(extract(dow FROM ts)), floor(max(EXTRACT(EPOCH FROM ts)))" + testRange + testGrouped,
			[]string{"max(extract(dow FROM ts))", "max(extract(epoch FROM ts))"},
		},
		"type names": {
			testBucket + "max(host::text), max(CAST(b AS bytea)), max(tags::text[]), max(v::int), max(n::integer[]), max(j::jsonb)" +
				testRange + testGrouped,
			[]string{"host::text", "CAST(b AS bytea)", "tags::text[]", "v::INT4)", "n::INT4[]", "j::JSONB"},
		},
		"bare SQL-value functions": {
			testBucket + "count(*) FILTER (WHERE usr = current_user OR usr = session_user OR usr = user OR d = current_role)" +
				testRange + testGrouped,
			[]string{"usr = current_user)", "usr = session_user)", "d = current_user)"},
		},
		"identifiers PostgreSQL reserves": {
			testBucket + `max("binary"), max("freeze"), max(t."verbose"), max("tablesample"), max("user"), max("Binary")` +
				` FROM trips AS t WHERE ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z'` + testGrouped,
			[]string{`max("binary")`, `max("freeze")`, `t."verbose"`, `max("tablesample")`, `max("user")`, `max("Binary")`},
		},
		"words inside strings are left alone": {
			testBucket + "count(*)" + testRange + " AND note = 'STRING binary extract(x, y) current_user()'" + testGrouped,
			[]string{"'STRING binary extract(x, y) current_user()'"},
		},
	} {
		got := testAnalyze(true, test.sql)
		if got.Mode != sqlanalyzer.CacheModeDelta {
			t.Fatalf("%s: got %v / %v / %v", name, got.Mode, got.Reason, got.Err)
		}
		rendered, err := got.Plan.Renderer.RenderExtent(testExtent)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range test.want {
			if !strings.Contains(rendered, want) {
				t.Fatalf("%s: %q is missing from %s", name, want, rendered)
			}
		}
	}
}

func TestAnalyzerCanonicalSQLIsRespelled(t *testing.T) {
	got := testAnalyze(true, testBucket+"max(extract(dow FROM ts)), max(host::text)"+testRange+testGrouped)
	const want = "SELECT time_bucket('300.000s', ts) AS time, max(extract(dow FROM ts)), max(host::text) FROM trips " +
		"WHERE (ts >= <$TS1$>) AND (ts < <$TS2$>) GROUP BY 1 ORDER BY 1"
	if got.Plan == nil || got.Plan.CanonicalSQL != want {
		t.Fatalf("got %+v", got)
	}
}

func TestParseInterval(t *testing.T) {
	for text, want := range map[string]time.Duration{
		"300.000s": 5 * time.Minute, "5 minutes": 5 * time.Minute, "5m": 5 * time.Minute, "5M": 5 * time.Minute,
		"1h30m": 90 * time.Minute, "1.5h": 90 * time.Minute, "1 hour 30 minutes": 90 * time.Minute, " 90 S ": 90 * time.Second,
		"1 day": day, "2 w": 14 * day, "1 week": 7 * day, "250ms": 250 * time.Millisecond, "0.25s": 250 * time.Millisecond,
		"7 usecs": 7 * time.Microsecond, "1 hr": time.Hour, "2 mins": 2 * time.Minute, "3 secs": 3 * time.Second,
		"4 msec": 4 * time.Millisecond, "1d": day, "0.000001s": time.Microsecond,
	} {
		if got, ok := parseInterval(text); !ok || got != want {
			t.Fatalf("%q: got %v %t", text, got, ok)
		}
	}
	for _, text := range []string{
		"", " ", "300", "5 parsecs", "1 month", "1 mon", "1 year", "-5m", "+5m", "00:05:00", "PT5M", "5m ago", "0s",
		".5h", "1..5h", "0.0000001s", "0.0000005s", "1.5d", "1.5 weeks", "9999999999999 hours", "999999999999 weeks",
		"5 m!", "106751 days 106751 days",
	} {
		if got, ok := parseInterval(text); ok {
			t.Fatalf("%q must not parse, got %v", text, got)
		}
	}
}

func TestPostRender(t *testing.T) {
	const untouched = "SELECT time_bucket('5m', ts) AS time FROM trips WHERE ts >= <$TRICKSTER_TS1_0$>"
	if got, err := postRender(untouched); err != nil || got != untouched {
		t.Fatalf("got %q %v", got, err)
	}
	got, err := postRender("SELECT extract('epoch', ts)::STRING, localtime() FROM verbose WHERE ts < <$TRICKSTER_TS2_1$>")
	if want := `SELECT extract(epoch FROM ts)::text, localtime FROM "verbose" WHERE ts < <$TRICKSTER_TS2_1$>`; err != nil || got != want {
		t.Fatalf("got %q %v", got, err)
	}
	for _, sql := range []string{"SELECT system_user", "SELECT extract(e'epoch', ts)", "SELECT extract('', ts)"} {
		if _, err := postRender(sql); err == nil {
			t.Fatalf("%q must not render", sql)
		}
	}
}

func FuzzLexicalPasses(f *testing.F) {
	for _, seed := range []string{
		"SELECT extract('epoch', ts)::STRING, localtime() FROM verbose WHERE ts < <$TRICKSTER_TS2_1$>",
		`SELECT "now"(), u&'\0041', $$x$$, e'\'' FROM ONLY t -- random(`, "300.000s", "1h30m", "extract(", "current_user(",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if width, ok := parseInterval(text); ok && (width <= 0 || width%time.Microsecond != 0) {
			t.Fatalf("%q parsed to %v", text, width)
		}
		scanFacts(text)
		if rendered, err := postRender(text); err == nil {
			// a second pass finds nothing left to re-spell
			if again, err := postRender(rendered); err != nil || again != rendered {
				t.Fatalf("%q rendered to %q and then %q (%v)", text, rendered, again, err)
			}
		}
		testAnalyze(true, text)
	})
}
