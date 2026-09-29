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
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	vtmysql "vitess.io/vitess/go/mysql"
)

// TestProxiedParserSkewMakesSessionCacheUnsafe covers the session-level half of
// the analyzer's fail-closed rule: a read the analyzer could not parse but the
// origin executed must not leave later statements cacheable.
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
		// buckets that have not ended are never complete
		{"far future", "7258118400", "7258118520", true, true},
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

func TestMySQLDirectivesLeaveIdentityAlone(t *testing.T) {
	a := mustNewAnalyzer()
	minuteQuery := strings.ReplaceAll(safeDateTimeQuery, "300", "60")
	query30 := "/* trickster-volatile-window:30 */ " + minuteQuery
	plan30 := a.Analyze(query30, time.Time{}).Plan
	plan60 := a.Analyze("/* trickster-volatile-window:60 */ "+minuteQuery, time.Time{}).Plan
	plan30Alternate := a.Analyze("-- trickster-volatile-window:30\n"+minuteQuery, time.Time{}).Plan
	plain := a.Analyze(minuteQuery, time.Time{}).Plan
	if plan30 == nil || plan60 == nil || plan30Alternate == nil || plain == nil {
		t.Fatal("directive queries did not produce delta plans")
	}
	if plan30.Directives.VolatileWindow != 30*time.Second ||
		plan30Alternate.Directives.VolatileWindow != 30*time.Second {
		t.Fatalf("30-second directive = %+v, %+v", plan30.Directives, plan30Alternate.Directives)
	}
	h := &protocolHandler{config: ProtocolConfig{BackendName: "mysql1"}}
	c := &vtmysql.Conn{User: "alice"}
	session := &upstreamSession{database: "analytics", timeZone: "+00:00"}
	// a directive changes how a plan is served, never what its buckets hold, so every form shares a key
	key := h.planCacheKey(c, session, cacheModeDPC, plain)
	for _, plan := range []*sqlanalyzer.QueryPlan{plan30, plan60, plan30Alternate} {
		if got := h.planCacheKey(c, session, cacheModeDPC, plan); got != key {
			t.Fatalf("a directive changed the DPC key: %+v", plan.Directives)
		}
	}
	literalOnly := a.Analyze(strings.Replace(minuteQuery,
		"WHERE ", "WHERE note = 'trickster-volatile-window:99' AND ", 1), time.Time{}).Plan
	if literalOnly == nil || !literalOnly.Directives.IsZero() {
		t.Fatalf("directive-like SQL literal was interpreted as a directive: %+v", literalOnly)
	}
	parsed, _, err := Parse(query30, time.Time{})
	if err != nil || parsed.Directives.VolatileWindow != 30*time.Second {
		t.Fatalf("Parse() directives = %+v, %v", parsed, err)
	}
	extent := timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(600, 0)}}
	window := nativedelta.VolatileWindow(0, 0, plan30.Step, plan30.Directives.VolatileWindow)
	stable := nativedelta.StableExtents(extent, plan30.Step, plan30.Phase, window, time.Unix(600, 0))
	if len(stable) != 1 || !stable[0].End.Equal(time.Unix(480, 0)) {
		t.Fatalf("directive volatile window stable extent = %v", stable)
	}
}
