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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/vt/sqlparser"
)

func TestAnalyzerPredicateSafetyAdversarial(t *testing.T) {
	a := mustNewAnalyzer()
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
	a := mustNewAnalyzer()
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

// TestProxiedParserSkewMakesSessionCacheUnsafe covers the session-level half of
// the same rule: a read the analyzer could not parse but the origin executed
// must not leave later statements cacheable.
func TestProxiedParserSkewMakesSessionCacheUnsafe(t *testing.T) {
	h := &protocolHandler{config: ProtocolConfig{Cache: newTestCache()}}
	session := &upstreamSession{}
	if !h.cacheEligible(session) {
		t.Fatal("a fresh session was not cache-eligible")
	}
	h.updateSessionStateParsed(session, parseQuery("SELECT count(*) FROM trips"))
	if !h.cacheEligible(session) {
		t.Fatal("an ordinary read made the session cache-ineligible")
	}
	h.updateSessionStateParsed(session, parseQuery("SELECT FROM trips"))
	if h.cacheEligible(session) {
		t.Fatal("a proxied parser-skew read left the session cache-eligible")
	}
}

// TestParserSkewDoesNotPoisonLaterCacheEntries proves the session rule end to
// end: once a read the analyzer could not parse has been proxied successfully,
// later reads on that session are served from the origin rather than the cache.
func TestParserSkewDoesNotPoisonLaterCacheEntries(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-parser-skew", time.Second,
		func(config *ProtocolConfig) {
			config.ProxyOnly = false
			config.Cache = newTestCache()
			config.CacheTTL = time.Hour
		})

	const cacheable = "select 42"
	for range 2 {
		if _, err := client.ExecuteFetch(cacheable, vtmysql.FETCH_ALL_ROWS, true); err != nil {
			t.Fatal(err)
		}
	}
	if got := origin.statementCount(cacheable); got != 1 {
		t.Fatalf("origin saw %q %d times before the parser-skew read, want 1", cacheable, got)
	}

	// The origin accepts syntax Vitess cannot parse, so the analyzer never saw
	// what this statement did.
	if _, err := client.ExecuteFetch("SELECT FROM metrics", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatalf("the parser-skew read was not proxied: %v", err)
	}
	if _, err := client.ExecuteFetch(cacheable, vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	if got := origin.statementCount(cacheable); got != 2 {
		t.Fatalf("origin saw %q %d times after the parser-skew read, want 2: the cache was still trusted",
			cacheable, got)
	}
}

func TestAnalyzerRejectsComplexStatementShapes(t *testing.T) {
	a := mustNewAnalyzer()
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
	a := mustNewAnalyzer()
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

func TestAnalyzerRangeEdges(t *testing.T) {
	a := mustNewAnalyzer()
	query := func(lower, upper string) string {
		return fmt.Sprintf(`SELECT epoch DIV 60 * 60 AS time, COUNT(*) AS value FROM events WHERE epoch >= %s AND epoch < %s GROUP BY time ORDER BY time`, lower, upper)
	}
	tests := []struct {
		name, lower, upper string
		delta              bool
		empty              bool
	}{
		{"equal", "1785542400", "1785542400", true, true},
		{"reversed", "1785542460", "1785542400", false, false},
		{"sub-cadence", "1785542401", "1785542459", true, true},
		{"unaligned", "1785542401", "1785542521", true, false},
		{"aligned", "1785542400", "1785542520", true, false},
		{"negative epoch", "-3600", "-3480", true, false},
		{"far future", "7258118400", "7258118520", true, false},
		{"seconds overflow", "9223372037", "9223372097", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := a.Analyze(query(tc.lower, tc.upper), time.Time{})
			if (got.Mode == sqlanalyzer.CacheModeDelta) != tc.delta {
				t.Fatalf("Analyze() = %s/%s (%v), want delta=%t", got.Mode,
					got.Reason, got.Err, tc.delta)
			}
			if !tc.delta {
				return
			}
			window, err := buildDeltaRequestWindow(got.Plan)
			if err != nil {
				t.Fatal(err)
			}
			if window.empty != tc.empty || window.lower.Before(got.Plan.LowerBound.Value) ||
				(!window.empty && window.upper.After(got.Plan.UpperBound.Value)) {
				t.Fatalf("unsafe normalized window: %+v for %+v", window, got.Plan)
			}
		})
	}
	for _, query := range []string{
		`SELECT epoch DIV 60 * 60 AS time, COUNT(*) AS value FROM events WHERE epoch >= 1785542400 GROUP BY time`,
		`SELECT epoch DIV 60 * 60 AS time, COUNT(*) AS value FROM events WHERE epoch < 1785628800 GROUP BY time`,
	} {
		if got := a.Analyze(query, time.Time{}); got.Mode == sqlanalyzer.CacheModeDelta {
			t.Fatalf("open range was DPC: %+v", got.Plan)
		}
	}
}

func TestAnalyzeResultShapeRejectsEmptyGroupByWithoutPanicking(t *testing.T) {
	statement, err := defaultAnalyzer.parser.Parse(safeDateTimeQuery)
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

func TestMySQLDirectiveIdentityAndBackfill(t *testing.T) {
	a := mustNewAnalyzer()
	minuteQuery := strings.ReplaceAll(safeDateTimeQuery, "300", "60")
	query30 := "/* trickster-backfill-tolerance:30 */ " + minuteQuery
	query60 := "/* trickster-backfill-tolerance:60 */ " + minuteQuery
	plan30 := a.Analyze(query30, time.Time{}).Plan
	plan60 := a.Analyze(query60, time.Time{}).Plan
	plan30Alternate := a.Analyze("-- trickster-backfill-tolerance:30\n"+minuteQuery,
		time.Time{}).Plan
	if plan30 == nil || plan60 == nil {
		t.Fatal("directive queries did not produce delta plans")
	}
	if plan30.BackfillTolerance != 30*time.Second ||
		plan30.IdentitySuffix != "backfill_tolerance=30" {
		t.Fatalf("30-second directive = %+v", plan30)
	}
	h := &protocolHandler{config: ProtocolConfig{BackendName: "mysql1"}}
	c := &vtmysql.Conn{User: "alice"}
	session := &upstreamSession{database: "analytics", timeZone: "+00:00"}
	key30 := h.queryCacheKey(c, session, "dpc", plan30.CanonicalSQL, plan30.IdentitySuffix)
	key60 := h.queryCacheKey(c, session, "dpc", plan60.CanonicalSQL, plan60.IdentitySuffix)
	if key30 == key60 {
		t.Fatal("result-affecting directives share a DPC identity")
	}
	if plan30Alternate == nil || key30 != h.queryCacheKey(c, session, "dpc",
		plan30Alternate.CanonicalSQL, plan30Alternate.IdentitySuffix) {
		t.Fatal("equivalent directive comments do not share normalized identity")
	}
	literalOnly := a.Analyze(strings.Replace(minuteQuery,
		"WHERE ", "WHERE note = 'trickster-backfill-tolerance:99' AND ", 1), time.Time{}).Plan
	if literalOnly == nil || literalOnly.BackfillTolerance != 0 {
		t.Fatalf("directive-like SQL literal was interpreted as a directive: %+v", literalOnly)
	}
	literalQuery := strings.Replace(minuteQuery,
		"WHERE ", "WHERE note = 'trickster-backfill-tolerance:99' AND ", 1)
	literalParsed, _, literalErr := Parse(literalQuery, time.Time{})
	if literalErr != nil || literalParsed.BackfillTolerance != 0 {
		t.Fatalf("Parse() interpreted directive-like SQL literal: %+v, %v",
			literalParsed, literalErr)
	}
	parsed, _, err := Parse(query30, time.Time{})
	if err != nil || parsed.CacheKeyElements["mysql_directives"] != "backfill_tolerance=30" {
		t.Fatalf("Parse() directive identity = %+v, %v", parsed, err)
	}
	extent := timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(600, 0)}}
	stable := (&protocolHandler{}).stableExtents(extent, plan30, time.Unix(600, 0))
	if len(stable) != 1 || !stable[0].End.Equal(time.Unix(480, 0)) {
		t.Fatalf("directive backfill stable extent = %v", stable)
	}
}
