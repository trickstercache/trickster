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
	"slices"
	"strings"
	"testing"
	"time"

	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
)

// The analyzer itself is exercised in pkg/parsing/sqlanalyzer/vitess; the
// tests here cover this backend's use of its analyses.

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

func TestParseCacheModes(t *testing.T) {
	if query, cacheable, err := Parse("DELETE FROM trips", time.Time{}); query != nil || cacheable || err == nil {
		t.Errorf("non-cacheable Parse = %+v/%t/%v", query, cacheable, err)
	}
	query, cacheable, err := Parse("SELECT COUNT(*) FROM trips", time.Time{})
	if query == nil || !cacheable || err == nil || query.Statement != "SELECT COUNT(*) FROM trips" {
		t.Errorf("object-cache Parse = %+v/%t/%v", query, cacheable, err)
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
