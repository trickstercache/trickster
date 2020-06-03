/*
 * Copyright 2020 Comcast Cable Communications Management, LLC
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
	t, _ := time.Parse(chLayout, "2020-06-01 12:02:00")
	return int(t.Unix())
}

func TestFindParts(t *testing.T) {
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

	trq = &timeseries.TimeRangeQuery{}
	query = `WITH dictGetString('test_cache', server, xxHash64(server)) as server_name ` +
		`SELECT toStartOfFiveMinute(datetime) AS t, count() as cnt FROM test_db.test_table WHERE t > ` +
		`'2020-05-30 11:00:00' AND t < now() - 300 FORMAT JSON`
	err = parseRawQuery(query, trq)
	if err != nil {
		t.Error(err)
	}
	if trq.Step != 300*time.Second {
		t.Errorf("Step of %d did not match 300 seconds", trq.Step)
	}
	if trq.Statement != `WITH dictGetString('test_cache',server,xxHash64(server)) as server_name `+
		`SELECT toStartOfFiveMinute(datetime) AS t,count() as cnt `+
		`FROM test_db.test_table WHERE t >= <$TIMESTAMP1$> AND t < <$TIMESTAMP2$> FORMAT JSON` {
		t.Errorf("Tokenized statement did not match query")
	}

	trq = &timeseries.TimeRangeQuery{}
	query = `SELECT toInt32(toStartOfFiveMinute(datetime)) AS t, count() as cnt FROM test_db.test_table WHERE datetime > ` +
		`'2020-05-30 11:00:00' AND datetime < now() - 300 FORMAT JSON`
	err = parseRawQuery(query, trq)
	if err != nil {
		t.Error(err)
	}
	if trq.Step != 300*time.Second {
		t.Errorf("Step of %d did not match 300 seconds", trq.Step)
	}

}

func TestBadQueries(t *testing.T) {
	test := func(run string, query string, es string) {
		t.Run(run, func(t *testing.T) {
			trq := &timeseries.TimeRangeQuery{}
			err := parseRawQuery(query, trq)
			if err == nil {
				t.Errorf("Expected err parsing time query")
			} else if err.Error() != es {
				t.Errorf("Expected error \"%s\", got \"%s\"", es, err.Error())
			}
		})
	}

	test("Query too short", "SELECT too short", "unrecognized query format")
	test("Query not JSON format", "SELECT toStartOfMinute(datetime), cnt FROM test_table FORMAT TSV",
		"non JSON formats not supported")
	test("Bad time function", "WITH 300 as t SELECT toStartOfTenMinutes(datetime, cnt FROM "+
		"test_table FORMAT JSON", "invalid time function syntax")
	test("Not valid time series", "SELECT a, b FROM test_table FORMAT JSON", "no matching time value column found")
	test("No range on time column", "SELECT toDate(datetime) t, cnt FROM test_table WHERE cnt > 100 FORMAT JSON",
		"no time range found")
	test("Weird between clause", "SELECT toDate(datetime) as t, cnt FROM test_table WHERE t BETWEEN 15002 15003 FORMAT JSON",
		"unrecognized between clause")
	test("Invalid start time", "SELECT toDate(datetime), cnt FROM test_table WHERE datetime BETWEEN November AND December FORMAT JSON",
		`parsing time "November" as "2006-01-02 15:04:05": cannot parse "November" as "2006"`)
	test("Invalid end time", "SELECT toDate(datetime), cnt FROM test_table WHERE datetime BETWEEN now() AND December FORMAT JSON",
		`parsing time "December" as "2006-01-02 15:04:05": cannot parse "December" as "2006"`)
	test("Invalid start time", "SELECT toDate(datetime), cnt FROM test_table WHERE datetime>='November' AND datetime <=now() FORMAT JSON",
		`parsing time "November" as "2006-01-02 15:04:05": cannot parse "November" as "2006"`)
	test("Invalid end time", "SELECT toDate(datetime), cnt FROM test_table WHERE datetime > '2020-10-15 00:22:00' AND datetime <'December' FORMAT JSON",
		`parsing time "December" as "2006-01-02 15:04:05": cannot parse "December" as "2006"`)
	test("Weird now expression", "SELECT toDate(datetime), cnt FROM test_table WHERE datetime > '2020-10-15 00:22:00' AND datetime <now()-2tt FORMAT JSON",
		`strconv.Atoi: parsing "2tt": invalid syntax`)
}

func TestBackfillTolerance(t *testing.T) {
	var query string
	parsingNowProvider = testNow

	test := func(run string, bf int, query string, exp int) {
		t.Run(run, func(t *testing.T) {
			trq := &timeseries.TimeRangeQuery{}
			trq.BackfillTolerance = time.Duration(bf) * time.Second
			err := parseRawQuery(query, trq)
			if err != nil {
				t.Error(err)
			}
			actual := int(trq.BackfillTolerance.Seconds())
			if actual != exp {
				t.Errorf("Expected backfill tolerance of %d, got %d", exp, actual)
			}
		})
	}

	query = `select intDiv(toInt32(datetime), 20) * 20 * 1000 as t, sum(cnt) FROM test_table WHERE datetime >= '2020-06-01 11:00:00'` +
		" FORMAT JSON"
	test("Backfill from now should be at least configured value", 180, query, 180)
	query = `select intDiv(toInt32(datetime), 300) * 300 as t, sum(cnt) FROM testTable WHERE datetime >= '2020-06-01 11:00:00' ` +
		` and datetime <= '2020-06-01 12:02:00' FORMAT JSON`
	test("Backfill from now bucket should be at least to prior bucket value", 60, query, 120)
	query = `select intDiv(toInt32(datetime), 20) * 20 as t, sum(cnt) FROM testTable WHERE datetime >= '2020-06-01 11:00:00' ` +
		` and datetime <= '2020-06-01 12:01:00' FORMAT JSON`
	test("Backfill should be at least now - configured value value", 180, query, 120)
	query = `select intDiv(toInt32(datetime), 20) * 20 as t, sum(cnt) FROM testTable WHERE datetime >= '2020-06-01 11:00:00' ` +
		` and datetime <= '2020-06-01 11:50:00' FORMAT JSON`
	test("Backfill should be negative/ignored if too far back", 180, query, -540)
}
