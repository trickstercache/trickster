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

package clickhouse

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const tq02 = `SELECT toStartOfInterval(datetime, INTERVAL 60 second) AS t, x, count() AS cnt ` +
	`FROM test_db.test_table WHERE datetime >= 1589904000 AND datetime < 1589997600 ` +
	`GROUP BY t, x ORDER BY t DESC FORMAT TabSeparatedWithNamesAndTypes`

const tq03 = `SELECT (intDiv(toUInt32(time_column), 60) * 60) * 1000 AS t, countMerge(some_count) AS cnt, field1, field2 ` +
	`FROM testdb.test_table WHERE time_column >= toDateTime(1516665600) AND time_column < toDateTime(1516687200) ` +
	`AND date_column >= toDate(1516665600) AND date_column <= toDate(1516687200) ` +
	`AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t, field1 FORMAT JSON`

func TestParseBuildsTricksterArtifacts(t *testing.T) {
	trq, options, canObjectCache, err := parse(tq03, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !canObjectCache {
		t.Fatal("expected object-cache eligibility")
	}
	if trq.Step != time.Minute || trq.StepNS != time.Minute.Nanoseconds() {
		t.Errorf("unexpected step: %s / %d", trq.Step, trq.StepNS)
	}
	if trq.TimestampDefinition.Name != "t" || trq.TimestampDefinition.DataType != timeseries.DateTimeUnixMilli {
		t.Errorf("unexpected timestamp definition: %+v", trq.TimestampDefinition)
	}
	if got := timeseries.FieldDataType(trq.TimestampDefinition.ProviderData1); got != timeseries.DateTimeUnixSecs {
		t.Errorf("timestamp input type = %v, want UnixSecs", got)
	}
	if options.BaseTimestampFieldName != "time_column" {
		t.Errorf("base timestamp = %q", options.BaseTimestampFieldName)
	}
	if got := trq.CacheKeyElements["query"]; got != trq.Statement {
		t.Errorf("cache key differs from canonical statement\nkey: %s\nstmt: %s", got, trq.Statement)
	}
	if strings.Contains(trq.Statement, "1516665600") || strings.Contains(trq.Statement, "1516687200") {
		t.Errorf("cache identity retains extent: %s", trq.Statement)
	}
	if len(trq.TagFieldDefintions) != 2 {
		t.Errorf("group fields = %d, want 2", len(trq.TagFieldDefintions))
	}
}

func TestCanonicalCacheIdentityIgnoresExtent(t *testing.T) {
	query := func(lower, upper int) string {
		return `SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= ` +
			strconv.Itoa(lower) + ` AND ts < ` + strconv.Itoa(upper) + ` GROUP BY t FORMAT JSON`
	}
	first, _, _, err := parse(query(120, 240), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := parse(query(300, 420), nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Statement != second.Statement {
		t.Errorf("different ranges produced different identities:\n%s\n%s", first.Statement, second.Statement)
	}
	if first.CacheKeyElements["query"] != second.CacheKeyElements["query"] ||
		first.CacheKeyElements["query"] != first.Statement {
		t.Errorf("different ranges produced different cache-key query elements:\n%s\n%s",
			first.CacheKeyElements["query"], second.CacheKeyElements["query"])
	}
	third, _, _, err := parse(strings.Replace(query(120, 240), "WHERE ts", "WHERE tenant = 'other' AND ts", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheKeyElements["query"] == third.CacheKeyElements["query"] {
		t.Error("a non-time predicate did not change the canonical cache-key element")
	}
}

func TestExclusiveUpperBoundUsesInclusiveInternalExtent(t *testing.T) {
	query := `SELECT toStartOfMinute(ts) AS t, count() FROM events ` +
		`WHERE ts >= 120 AND ts < 240 GROUP BY t`
	trq, _, _, err := parse(query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := trq.Extent.End.Unix(); got != 180 {
		t.Errorf("internal upper extent = %d, want final included bucket 180", got)
	}
}

func TestUnsafeAndUnsupportedQueriesUseObjectCache(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		reason sqlanalyzer.AnalysisReason
	}{
		{"or predicate", `SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= 100 OR ts < 200 GROUP BY t`, sqlanalyzer.ReasonUnsafePredicate},
		{"not predicate", `SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE NOT (ts >= 100) GROUP BY t`, sqlanalyzer.ReasonUnsafePredicate},
		{"not time series", `SELECT count() FROM events WHERE ts >= 100 GROUP BY service`, sqlanalyzer.ReasonUnsupportedBucket},
		{"limit", `SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= 100 GROUP BY t LIMIT 10`, sqlanalyzer.ReasonUnsupportedLimit},
		{"unsupported interval", `SELECT toStartOfInterval(ts, INTERVAL 1 year) AS t, count() FROM events WHERE ts >= 100 AND ts < 200 GROUP BY t FORMAT JSON`, sqlanalyzer.ReasonUnsupportedBucket},
		{"unsupported week mode", `SELECT toStartOfWeek(ts, 1) AS t, count() FROM events WHERE ts >= 100 AND ts < 200 GROUP BY t`, sqlanalyzer.ReasonUnsupportedBucket},
		{"missing group by", `SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= 100 AND ts < 200 FORMAT JSON`, sqlanalyzer.ReasonUnsupportedGrouping},
		{"unsupported format", `SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= 120 AND ts < 240 GROUP BY t FORMAT Parquet`, sqlanalyzer.ReasonUnsupportedFormat},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := dialectAnalyzer.Analyze(test.query, time.Unix(500, 0))
			if analysis.Mode != sqlanalyzer.CacheModeObject || analysis.Reason != test.reason {
				t.Errorf("unexpected analysis: %+v", analysis)
			}
			_, _, canObjectCache, err := parse(test.query, nil)
			if !canObjectCache || err == nil {
				t.Errorf("parse classification = (OPC %t, err %v)", canObjectCache, err)
			}
		})
	}
}

func TestDirectivesAreExtractedBeforeCanonicalization(t *testing.T) {
	query := `/* trickster-backfill-tolerance:30 trickster-fast-forward:off */ ` + tq02
	trq, options, _, err := parse(query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if trq.BackfillTolerance != 30*time.Second || !options.FastForwardDisable {
		t.Errorf("directives not extracted: tolerance=%s fast-forward=%t",
			trq.BackfillTolerance, options.FastForwardDisable)
	}
}
