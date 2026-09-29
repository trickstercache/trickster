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

package vitess

import (
	"fmt"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/sqlanalyzertest"
)

func TestRenderParityWithThePlanner(t *testing.T) {
	// every mode's interior and partial buckets render through the statement's own comparators and read
	// back as the range planned
	a := MustNewAnalyzer()
	const base = int64(1_699_999_200)
	now := time.Unix(base, 0).Add(30 * 24 * time.Hour)
	var compared, ranges int
	for _, step := range []int64{60, 300, 3600} {
		buckets := []string{
			fmt.Sprintf("UNIX_TIMESTAMP(ts) DIV %d * %d AS time_sec", step, step),
			fmt.Sprintf("cast(cast(UNIX_TIMESTAMP(ts)/(%d) as signed)*%d as signed) AS time_sec", step, step),
		}
		wheres := []string{
			"ts >= FROM_UNIXTIME(%d) AND ts < FROM_UNIXTIME(%d)",
			"ts >= FROM_UNIXTIME(%d) AND ts <= FROM_UNIXTIME(%d)",
			"ts BETWEEN FROM_UNIXTIME(%d) AND FROM_UNIXTIME(%d)",
		}
		offsets := []int64{0, 1, step / 2, step - 1}
		for _, bucket := range buckets {
			for _, lo := range offsets {
				for _, k := range []int64{2, 3} {
					for _, uo := range offsets {
						for _, where := range wheres {
							query := fmt.Sprintf("SELECT %s, count(*) AS value FROM events WHERE "+where+
								" GROUP BY time_sec ORDER BY time_sec", bucket, base+lo, base+k*step+uo)
							got := a.Analyze(query, now)
							if got.Mode != sqlanalyzer.CacheModeDelta {
								t.Fatalf("%s: %s (%v)", query, got.Reason, got.Err)
							}
							n, err := sqlanalyzertest.RenderParity(a, got.Plan, now)
							if err != nil {
								t.Fatalf("%s: %v", query, err)
							}
							compared, ranges = compared+1, ranges+n
						}
					}
				}
			}
		}
	}
	t.Logf("compared %d delta plans and %d rendered ranges", compared, ranges)
	if compared < 500 {
		t.Fatalf("only %d delta plans were compared", compared)
	}
}
