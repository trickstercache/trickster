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
	"fmt"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/sqlanalyzertest"
)

func TestDropParityWithThePlanner(t *testing.T) {
	a := NewAnalyzer(Options{RoundUnalignedTimeBounds: true})
	const base = int64(1_699_999_200)
	now := time.Unix(base, 0).Add(30 * 24 * time.Hour)
	var compared int
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
	}
	buckets := []struct {
		expr string
		step int64
	}{
		{"toStartOfInterval(ts, INTERVAL 5 MINUTE) AS t", 300},
		{"intDiv(toUInt32(ts), 300) * 300 AS t", 300},
		{"toStartOfHour(ts) AS t", 3600},
	}
	lowers := []string{"ts >= %d", "ts >= toDateTime(%d)", "t >= toDateTime(%d)", "t > toDateTime(%d)", "ts > %d"}
	uppers := []string{
		"ts < %d", "ts <= %d", "ts < toDateTime(%d)", "ts <= toDateTime(%d)",
		"t < toDateTime(%d)", "t <= toDateTime(%d)",
	}
	for _, b := range buckets {
		offsets := []int64{0, 1, b.step / 2, b.step - 1}
		for _, lo := range offsets {
			for _, k := range []int64{2, 3} {
				for _, uo := range offsets {
					lower, upper := base+lo, base+k*b.step+uo
					for _, lf := range lowers {
						for _, uf := range uppers {
							check(fmt.Sprintf("SELECT %s, count() AS cnt FROM events WHERE %s AND %s GROUP BY t ORDER BY t",
								b.expr, fmt.Sprintf(lf, lower), fmt.Sprintf(uf, upper)))
						}
					}
					check(fmt.Sprintf("SELECT %s, count() AS cnt FROM events WHERE ts BETWEEN %d AND %d GROUP BY t",
						b.expr, lower, upper))
				}
			}
		}
	}
	for _, c := range clickHouseCompatibilityCorpus {
		check(c.query)
	}
	t.Logf("compared %d delta plans", compared)
	if compared < 500 {
		t.Fatalf("only %d delta plans were compared", compared)
	}
}
