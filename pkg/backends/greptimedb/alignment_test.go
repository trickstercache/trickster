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

package greptimedb

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/mysql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestSQLCompleteBucketAlignment(t *testing.T) {
	pg := analyzer.ForSession(pgwire.SessionView{UTC: true})
	for _, tc := range []struct {
		name, lower, upper, operator string
		start, end                   int
	}{
		{"lower", "00:00:01", "01:00:00", "<", 15, 45},
		{"upper", "00:00:00", "00:59:59", "<", 0, 30},
		{"both", "00:00:01", "00:59:59", "<", 15, 30},
		{"inclusive", "00:00:00", "01:00:00", "<=", 0, 45},
	} {
		statement := strings.NewReplacer("00:00:00Z", tc.lower+"Z", "01:00:00Z", tc.upper+"Z", "ts <", "ts "+tc.operator).Replace(httpSQL)
		for _, protocol := range []string{"GET", "POST", "PGWire"} {
			t.Run(tc.name+"/"+protocol, func(t *testing.T) {
				var trq *timeseries.TimeRangeQuery
				if protocol == "PGWire" {
					a := pg.Analyze(statement, time.Time{})
					if a.Mode != sqlanalyzer.CacheModeDelta {
						t.Fatalf("not delta: %+v", a)
					}
					trq = sqlanalyzer.NewTimeRangeQuery(statement)
					a.Plan.ApplyToQuery(trq)
					trq.Extent = a.Plan.RequestExtent(time.Time{})
				} else {
					values := url.Values{"sql": {statement}}.Encode()
					r := httptest.NewRequest(protocol, "/v1/sql?"+values, nil)
					if protocol == "POST" {
						r = httptest.NewRequest(protocol, "/v1/sql", strings.NewReader(values))
						r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					}
					var err error
					trq, _, _, err = (&Client{}).ParseTimeRangeQuery(r)
					if err != nil {
						t.Fatal(err)
					}
				}
				base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
				want := timeseries.Extent{Start: base.Add(time.Duration(tc.start) * time.Minute), End: base.Add(time.Duration(tc.end) * time.Minute)}
				if !trq.Extent.Start.Equal(want.Start) || !trq.Extent.End.Equal(want.End) {
					t.Fatalf("extent=%+v want=%+v", trq.Extent, want)
				}
				plan := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
				rendered, err := plan.RenderExtent(want)
				if err != nil {
					t.Fatal(err)
				}
				a := pg.Analyze(rendered, time.Time{})
				if a.Mode != sqlanalyzer.CacheModeDelta || a.Plan.CanonicalSQL != plan.CanonicalSQL || a.Plan.DropsPartialBuckets {
					t.Fatalf("rendered range is not complete buckets: %s %+v", rendered, a)
				}
			})
		}
	}
}

func TestMySQLCompleteBucketAlignment(t *testing.T) {
	a := MySQLEngine().Analyzer(mysql.SessionView{TimeZone: "UTC"})
	for _, tc := range []struct{ lower, upper, start, end int64 }{
		{1767225601, 1767225720, 1767225660, 1767225660},
		{1767225600, 1767225719, 1767225600, 1767225600},
		{1767225601, 1767225839, 1767225660, 1767225720},
		{-179, -1, -120, -120},
	} {
		t.Run(fmt.Sprint(tc.lower), func(t *testing.T) {
			query := strings.NewReplacer("1767225600", fmt.Sprint(tc.lower), "1767225720", fmt.Sprint(tc.upper)).Replace(mysqlBucketQuery)
			analysis := a.Analyze(query, time.Time{})
			if analysis.Mode != sqlanalyzer.CacheModeDelta {
				t.Fatalf("not delta cacheable: %+v", analysis)
			}
			extent := analysis.Plan.RequestExtent(time.Time{})
			if extent.Start.Unix() != tc.start || extent.End.Unix() != tc.end {
				t.Fatalf("extent=%+v want=[%d,%d]", extent, tc.start, tc.end)
			}
			rendered, err := analysis.Plan.RenderExtent(extent)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip := a.Analyze(rendered, time.Time{})
			if roundTrip.Mode != sqlanalyzer.CacheModeDelta || roundTrip.Plan.DropsPartialBuckets || roundTrip.Plan.CanonicalSQL != analysis.Plan.CanonicalSQL {
				t.Fatalf("invalid normalized query: %s %+v", rendered, roundTrip)
			}
		})
	}
}
