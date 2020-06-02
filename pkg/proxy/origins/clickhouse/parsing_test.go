/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"github.com/tricksterproxy/trickster/pkg/timeseries"
	"testing"
	"time"
)

func testNow() int {
	t, _ := time.Parse(chLayout, "2020-06-01 12:00:00")
	return int(t.Unix())
}

func testDate(ts string) time.Time {
	t, _ := time.Parse(chLayout, "2020-06-01 "+ts)
	return t
}

/*func chDateDisplay(time.Time) string {
	return time.
}*/

func TestFindParts(t *testing.T) {
	/*query := "WITH  3600  as  x  SELECT (  intDiv(toUInt32(datetime), x) * x) * 1000 AS t," +
	" count() as cnt FROM comcast_ott_maple.atsec_chi WHERE datetime BETWEEN toDateTime(1589904000) AND toDateTime(1589997600)" +
	" GROUP BY t ORDER BY  t DESC FORMAT JSON"*/
	query := `WITH  'igor * 31 + \' dks( k )'  as  igor, 3600 as x  SELECT (  intDiv(toUInt32(datetime), x) * x) * 1000 AS t,` +
		` count() as cnt FROM comcast_ott_maple.atsec_chi WHERE datetime >= 1589904000 AND datetime < 1589997600)` +
		` GROUP BY t ORDER BY  t DESC FORMAT JSON`
	parts := findParts(query)
	if len(parts) != 27 {
		t.Errorf("Find parts returned %d, expected %d incorrect number of parts", len(parts), 30)
	}

}

func TestGoodQueries(t *testing.T) {
	query := `SELECT (  intDiv(toUInt32(datetime), 300) * 300) * 1000 AS t,` +
		` count() as cnt FROM test_db.test_table WHERE datetime between 1589904000 AND 1589997600` +
		` GROUP BY t ORDER BY  t DESC FORMAT JSON`
	trq := &timeseries.TimeRangeQuery{}
	err := parseRawQuery(query, trq)
	if err != nil {
		t.Error(err)
	}
	if trq.Step != 300*time.Second {
		t.Errorf("Step of %d did not match 300 seconds", trq.Step)
	}

	if trq.Extent.Start != time.Unix(int64(1589904000), 0) {
		t.Errorf("Expected start time of 1589904000, got %d", trq.Extent.Start.Unix())
	}
	trq = &timeseries.TimeRangeQuery{}
	query = `SELECT toStartOfFiveMinute(datetime) AS t, count() as cnt FROM test_db.test_table WHERE t > ` +
		`'2020-05-30 11:00:00' AND t < now() - 300 FORMAT JSON`
	err = parseRawQuery(query, trq)
	if err != nil {
		t.Error(err)
	}
	if trq.Step != 300*time.Second {
		t.Errorf("Step of %d did not match 300 seconds", trq.Step)
	}

}

func TestBackfillTolerance(t *testing.T) {
	parsingNowProvider = testNow
	query := `select intDiv(toInt32(datetime), 20) * 20 as t, sum(cnt) FROM testTable WHERE datetime >= '2020-06-01 11:00:00' ` +
		` and datetime < '2020-06-01 12:00:00' FORMAT JSON`
	trq := &timeseries.TimeRangeQuery{BackfillTolerance: 180 * time.Second}
	_ = parseRawQuery(query, trq)
	if trq.BackfillTolerance != time.Second*180 {
		t.Errorf("Expected bft of 180, got %d", trq.BackfillTolerance)
	}

}

/*func TestGetQueryPartsFailure(t *testing.T) {
	query := "this should fail to parse"
	_, _, _, err := getQueryParts(query, "")
	if err == nil {
		t.Errorf("should have produced error")
	}

}

func TestParseQueryExtents(t *testing.T) {

	_, _, err := parseQueryExtents("", map[string]string{})
	if err == nil {
		t.Errorf("expected error: %s", `failed to parse query: could not find operator`)
	}

	_, _, err = parseQueryExtents("", map[string]string{"operator": "", "ts1": "a"})
	if err == nil {
		t.Errorf("expected error: %s", `failed to parse query: could not find start time`)
	}

	_, _, err = parseQueryExtents("", map[string]string{"operator": "between", "ts1": "1", "ts2": "a"})
	if err == nil {
		t.Errorf("expected error: %s", `failed to parse query: could not determine end time`)
	}

	_, _, err = parseQueryExtents("", map[string]string{"operator": "between", "ts1": "1"})
	if err == nil {
		t.Errorf("expected error: %s", `failed to parse query: could not find end time`)
	}

	_, _, err = parseQueryExtents("", map[string]string{"operator": "x", "ts1": "1"})
	if err != nil {
		t.Error(err)
	}

} */
