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

package cockroach

import (
	"fmt"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/sqlanalyzertest"
)

func TestDropParityWithThePlanner(t *testing.T) {
	const base = int64(1_699_999_200)
	now := time.Unix(base, 0).Add(30 * 24 * time.Hour)
	rfc := func(v int64) string { return time.Unix(v, 0).UTC().Format(time.RFC3339) }
	// each analyzer with the timestamp precision of the columns it fronts: DataFusion's nanoseconds, and
	// PostgreSQL's microseconds
	analyzers := map[string]struct {
		a         *Analyzer
		precision time.Duration
	}{
		"datafusion": {newDataFusionAnalyzer(), time.Nanosecond},
		"microsecond precision": {NewAnalyzer(Options{
			BucketMatchers: DataFusionBucketMatchers(),
			BoundPrecision: time.Microsecond,
		}), time.Microsecond},
	}
	buckets := []struct {
		expr string
		step int64
	}{
		{"date_bin(INTERVAL '5 minutes', time) AS b", 300},
		{"date_bin(INTERVAL '1 hour', time, TIMESTAMP '2000-01-01 00:15:00') AS b", 3600},
		{"date_trunc('hour', time) AS b", 3600},
	}
	lowers := []func(int64) string{
		func(v int64) string { return fmt.Sprintf("time >= %d", v) },
		func(v int64) string { return fmt.Sprintf("time >= '%s'", rfc(v)) },
		func(v int64) string { return fmt.Sprintf("b >= '%s'", rfc(v)) },
		func(v int64) string { return fmt.Sprintf("b > '%s'", rfc(v)) },
	}
	uppers := []func(int64) string{
		func(v int64) string { return fmt.Sprintf("time < %d", v) },
		func(v int64) string { return fmt.Sprintf("time <= %d", v) },
		func(v int64) string { return fmt.Sprintf("time < '%s'", rfc(v)) },
		func(v int64) string { return fmt.Sprintf("time <= '%s'", rfc(v)) },
		func(v int64) string { return fmt.Sprintf("b < '%s'", rfc(v)) },
		func(v int64) string { return fmt.Sprintf("b <= '%s'", rfc(v)) },
	}
	for name, analyzer := range analyzers {
		a := analyzer.a
		t.Run(name, func(t *testing.T) {
			var compared, ranges int
			check := func(query string) {
				t.Helper()
				got := a.Analyze(query, now)
				if got.Mode != sqlanalyzer.CacheModeDelta {
					return
				}
				ok, err := sqlanalyzertest.DropParity(got.Plan, now)
				if err != nil {
					t.Fatalf("%s: %v", query, err)
				}
				if ok {
					compared++
				}
				rendered, err := sqlanalyzertest.RenderParity(a, got.Plan, now, analyzer.precision)
				if err != nil {
					t.Fatalf("%s: %v", query, err)
				}
				ranges += rendered
			}
			for _, b := range buckets {
				offsets := []int64{0, 1, b.step / 2, b.step - 1}
				for _, lo := range offsets {
					for _, k := range []int64{2, 3} {
						for _, uo := range offsets {
							lower, upper := base+lo, base+k*b.step+uo
							for _, lf := range lowers {
								for _, uf := range uppers {
									check(fmt.Sprintf("SELECT %s, avg(v) AS v FROM cpu WHERE %s AND %s GROUP BY 1 ORDER BY 1",
										b.expr, lf(lower), uf(upper)))
								}
							}
							check(fmt.Sprintf("SELECT %s, avg(v) AS v FROM cpu WHERE time BETWEEN %d AND %d GROUP BY 1",
								b.expr, lower, upper))
						}
					}
				}
			}
			check(hourlyEpochQuery)
			t.Logf("compared %d delta plans and %d rendered ranges", compared, ranges)
			if compared < 500 {
				t.Fatalf("only %d delta plans were compared", compared)
			}
		})
	}
}
