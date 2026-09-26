/*
 * Copyright 2026 The Trickster Authors
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package greptimedb

import (
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/mysql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
)

const mysqlBucketQuery = "SELECT DATE_BIN('1m', ts, FROM_UNIXTIME(0)) AS time, label, COUNT(*) AS value " +
	"FROM readings WHERE ts >= FROM_UNIXTIME(1767225600) AND ts < FROM_UNIXTIME(1767225720) " +
	"GROUP BY time, label ORDER BY time, label"

func TestMySQLBucketAnalysis(t *testing.T) {
	a := MySQLEngine().Analyzer(mysql.SessionView{TimeZone: "UTC"})
	for _, bucket := range []string{"DATE_BIN('1m', ts, FROM_UNIXTIME(0))", "DATE_TRUNC('minute', ts)"} {
		t.Run(bucket, func(t *testing.T) {
			query := strings.Replace(mysqlBucketQuery, "DATE_BIN('1m', ts, FROM_UNIXTIME(0))", bucket, 1)
			analysis := a.Analyze(query, time.Time{})
			if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
				t.Fatalf("not delta cacheable: %+v", analysis)
			}
			if analysis.Plan.OutputUnit != timeseries.DateTimeSQL || analysis.Plan.Step != time.Minute {
				t.Fatal("wrong time axis")
			}
			rendered, err := analysis.Plan.RenderExtent(timeseries.Extent{Start: time.Unix(1767225660, 0), End: time.Unix(1767225720, 0)})
			if err != nil || !strings.Contains(strings.ToLower(rendered), "from_unixtime(1767225780)") {
				t.Fatalf("extent = %s, %v", rendered, err)
			}
		})
	}
	for _, query := range []string{
		strings.Replace(mysqlBucketQuery, "'1m'", "'1M'", 1),
		strings.Replace(mysqlBucketQuery, "'1m'", "'0m'", 1),
		strings.Replace(mysqlBucketQuery, "'1m'", "'+1m'", 1),
		strings.Replace(mysqlBucketQuery, "'1m'", "'9223372036854775807m'", 1),
		strings.Replace(mysqlBucketQuery, "'1m'", "'500ms'", 1),
		strings.Replace(mysqlBucketQuery, "'1m'", "'1500ms'", 1),
		strings.Replace(mysqlBucketQuery, "FROM_UNIXTIME(0)", "FROM_UNIXTIME(1)", 1),
		strings.Replace(mysqlBucketQuery, "1767225600", "1767225601", 1),
		strings.Replace(mysqlBucketQuery, "ts <", "ts <=", 1),
	} {
		if got := a.Analyze(query, time.Time{}); got.Mode == sqlanalyzer.CacheModeDelta {
			t.Fatalf("unsafe query admitted: %s", query)
		}
	}
	for _, zone := range []string{"", "+08:00", "America/New_York"} {
		if got := MySQLEngine().Analyzer(mysql.SessionView{TimeZone: zone}).Analyze(mysqlBucketQuery, time.Time{}); got.Mode == sqlanalyzer.CacheModeDelta {
			t.Fatalf("unverified zone admitted: %s", zone)
		}
	}
}

func TestMySQLTimestampPrecision(t *testing.T) {
	for _, raw := range []string{"2026-01-01 00:00:00", "2026-01-01 00:00:00.123456789", "1969-12-31 23:59:59.999999999"} {
		value := sqltypes.MakeTrusted(querypb.Type_TIMESTAMP, []byte(raw))
		got, err := mysqlTimestampEpoch(value)
		want, parseErr := time.Parse("2006-01-02 15:04:05.999999999", raw)
		if err != nil || parseErr != nil || got != want.UnixNano() {
			t.Fatalf("%s: %d %v", raw, got, err)
		}
	}
	for _, raw := range []string{"0000-00-00 00:00:00", "9999-12-31 23:59:59", "not a timestamp"} {
		if _, err := mysqlTimestampEpoch(sqltypes.MakeTrusted(querypb.Type_TIMESTAMP, []byte(raw))); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if _, err := (mysqlEngine{}).InitSession(nil); err == nil {
		t.Fatal("accepted nil session")
	}
	if _, _, err := (mysqlEngine{}).StreamState(nil); err == nil {
		t.Fatal("accepted nil stream")
	}
}
