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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// clickHouseCompatibilityCorpus is the maintained parser-compatibility
// contract for the AfterShip parser version pinned in go.mod. Add production
// and Grafana query shapes here with their expected classification before
// expanding the analyzer's accepted SQL surface or upgrading the parser.
var clickHouseCompatibilityCorpus = []struct {
	name   string
	query  string
	mode   sqlanalyzer.CacheMode
	reason sqlanalyzer.AnalysisReason
	step   time.Duration
	phase  time.Duration
}{
	{
		name: "grafana intDiv macro",
		query: "SELECT intDiv(toUInt32(ts), 300) * 300 * 1000 AS t, service, count() AS cnt " +
			"FROM events WHERE ts >= 1699999200 AND ts < 1700001000 GROUP BY t, service FORMAT JSON",
		mode: sqlanalyzer.CacheModeDelta, reason: sqlanalyzer.ReasonDeltaCacheable, step: 5 * time.Minute,
	},
	{
		name: "anonymized production partition query",
		query: "SELECT intDiv(toUInt32(events.ts), 60) * 60 * 1000 AS bucket, " +
			"events.service AS service, countMerge(request_count) AS cnt FROM metrics.events " +
			"WHERE events.ts >= toDateTime(1516665600) AND events.ts < toDateTime(1516687200) " +
			"AND partition_date >= toDate(1516665600) AND partition_date <= toDate(1516687200) " +
			"AND environment = 'prod' GROUP BY bucket, events.service ORDER BY bucket FORMAT JSON",
		mode: sqlanalyzer.CacheModeDelta, reason: sqlanalyzer.ReasonDeltaCacheable, step: time.Minute,
	},
	{
		name: "real common table expression",
		query: "WITH filtered AS (SELECT ts, service FROM events WHERE environment = 'prod') " +
			"SELECT toStartOfMinute(ts) AS t, service, count() AS cnt FROM filtered " +
			"WHERE ts >= 120 AND ts < 240 GROUP BY t, service FORMAT JSON",
		mode: sqlanalyzer.CacheModeDelta, reason: sqlanalyzer.ReasonDeltaCacheable, step: time.Minute,
	},
	{
		name: "unsafe raw BETWEEN",
		query: "SELECT toStartOfMinute(ts) AS t, count() FROM events " +
			"WHERE ts BETWEEN 120 AND 240 GROUP BY t",
		mode: sqlanalyzer.CacheModeObject, reason: sqlanalyzer.ReasonUnsafePredicate,
	},
	{
		name: "timezone bucket",
		query: "SELECT toStartOfHour(ts, 'America/Denver') AS t, count() FROM events " +
			"WHERE ts >= 0 AND ts < 7200 GROUP BY t",
		mode: sqlanalyzer.CacheModeObject, reason: sqlanalyzer.ReasonUnsupportedBucket,
	},
	{
		name:   "non-select statement",
		query:  "INSERT INTO events VALUES (1)",
		mode:   sqlanalyzer.CacheModeNone,
		reason: sqlanalyzer.ReasonUnsupportedStatement,
	},
}

func TestClickHouseCompatibilityCorpus(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, test := range clickHouseCompatibilityCorpus {
		t.Run(test.name, func(t *testing.T) {
			analysis := NewAnalyzer().Analyze(test.query, now)
			if analysis.Mode != test.mode || analysis.Reason != test.reason {
				t.Fatalf("analysis = (%s, %s, %v), want (%s, %s)",
					analysis.Mode.String(), analysis.Reason, analysis.Err,
					test.mode.String(), test.reason)
			}
			if test.mode != sqlanalyzer.CacheModeDelta {
				if analysis.Plan != nil {
					t.Fatal("non-delta analysis produced a query plan")
				}
				return
			}
			if analysis.Err != nil || analysis.Plan == nil {
				t.Fatalf("delta analysis = %+v", analysis)
			}
			if analysis.Plan.Step != test.step || analysis.Plan.Phase != test.phase {
				t.Errorf("cadence = (%s, %s), want (%s, %s)",
					analysis.Plan.Step, analysis.Plan.Phase, test.step, test.phase)
			}
		})
	}
}

func TestAllFixedBucketCadences(t *testing.T) {
	tests := []struct {
		function string
		step     time.Duration
		phase    time.Duration
		start    int64
	}{
		{"toMonday", week, 4 * day, 345600},
		{"toStartOfWeek", week, 3 * day, 259200},
		{"timeSlot", 30 * time.Minute, 0, 0},
		{"toStartOfSecond", time.Second, 0, 0},
		{"toStartOfMillisecond", time.Millisecond, 0, 0},
		{"toStartOfMicrosecond", time.Microsecond, 0, 0},
		{"toStartOfNanosecond", time.Nanosecond, 0, 0},
		{"toStartOfDay", day, 0, 0},
		{"toStartOfHour", time.Hour, 0, 0},
		{"toStartOfMinute", time.Minute, 0, 0},
		{"toStartOfFiveMinute", 5 * time.Minute, 0, 0},
		{"toStartOfTenMinutes", 10 * time.Minute, 0, 0},
		{"toStartOfFifteenMinutes", 15 * time.Minute, 0, 0},
	}
	if len(tests) != len(fixedBucketDurations) {
		t.Fatalf("fixed bucket corpus has %d cases for %d supported functions",
			len(tests), len(fixedBucketDurations))
	}
	for _, test := range tests {
		t.Run(test.function, func(t *testing.T) {
			end := test.start + max(1, int64(2*test.step/time.Second))
			query := "SELECT " + test.function + "(ts) AS t, count() FROM events WHERE ts >= " +
				formatInt(test.start) + " AND ts < " + formatInt(end) + " GROUP BY t"
			assertDeltaCadence(t, query, test.step, test.phase)
		})
	}
}

func TestAllIntervalBucketUnits(t *testing.T) {
	tests := []struct {
		unit  string
		step  time.Duration
		phase time.Duration
		start int64
	}{
		{"second", time.Second, 0, 0},
		{"millisecond", time.Millisecond, 0, 0},
		{"microsecond", time.Microsecond, 0, 0},
		{"nanosecond", time.Nanosecond, 0, 0},
		{"minute", time.Minute, 0, 0},
		{"hour", time.Hour, 0, 0},
		{"day", day, 0, 0},
		{"week", week, 4 * day, 345600},
	}
	if len(tests) != len(intervalDurations) {
		t.Fatalf("interval corpus has %d cases for %d supported units",
			len(tests), len(intervalDurations))
	}
	for _, test := range tests {
		t.Run(test.unit, func(t *testing.T) {
			end := test.start + max(1, int64(2*test.step/time.Second))
			query := "SELECT toStartOfInterval(ts, INTERVAL 1 " + test.unit + ") AS t, count() " +
				"FROM events WHERE ts >= " + formatInt(test.start) + " AND ts < " + formatInt(end) + " GROUP BY t"
			if test.unit == "microsecond" || test.unit == "nanosecond" {
				analysis := NewAnalyzer().Analyze(query, time.Now())
				if analysis.Mode != sqlanalyzer.CacheModeObject || analysis.Reason != sqlanalyzer.ReasonInvalidSQL {
					t.Fatalf("unsupported parser interval did not fail closed: %+v", analysis)
				}
				return
			}
			assertDeltaCadence(t, query, test.step, test.phase)
		})
	}
}

func TestCrossPredicateBetweenStateDoesNotLeak(t *testing.T) {
	query := "SELECT toStartOfMinute(ts) AS t, count() FROM events " +
		"WHERE partition_date BETWEEN toDate(120) AND toDate(240) " +
		"AND ts >= 120 AND ts < 240 GROUP BY t"
	analysis := NewAnalyzer().Analyze(query, time.Unix(500, 0))
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		t.Fatalf("analysis = %+v, want delta", analysis)
	}
	rendered, err := analysis.Plan.RenderExtent(timeseries.Extent{
		Start: time.Unix(300, 0), End: time.Unix(360, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"partition_date BETWEEN toDate(300) AND toDate(360)",
		"ts >= 300", "ts < 420",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered query lacks %q: %s", want, rendered)
		}
	}
}

func assertDeltaCadence(t *testing.T, query string, step, phase time.Duration) {
	t.Helper()
	analysis := NewAnalyzer().Analyze(query, time.Unix(1_700_000_000, 0))
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil || analysis.Err != nil {
		t.Fatalf("analysis = %+v, want delta", analysis)
	}
	if analysis.Plan.Step != step || analysis.Plan.Phase != phase {
		t.Errorf("cadence = (%s, %s), want (%s, %s)", analysis.Plan.Step, analysis.Plan.Phase, step, phase)
	}
}

func formatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
