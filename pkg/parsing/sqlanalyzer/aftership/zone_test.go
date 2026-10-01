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
	"testing"
	"time"
)

func TestSessionZoneAnalysis(t *testing.T) {
	const from = " FROM e WHERE ts >= toDateTime(1700000000) AND ts < toDateTime(1700003600)"
	for _, c := range []struct {
		query              string
		readsZone, bounded bool
	}{
		// the bucket alone, in its SELECT, GROUP BY and ORDER BY, is judged by its alignment
		{"SELECT toStartOfHour(ts) AS t, count() AS c" + from + " GROUP BY t ORDER BY t", false, false},
		{"SELECT toStartOfHour(ts) AS t, count() AS c" + from + " GROUP BY toStartOfHour(ts) ORDER BY toStartOfHour(ts)", false, false},
		{"SELECT (intDiv(toUInt32(ts), 60) * 60) * 1000 AS t, count() AS c" + from + " GROUP BY t", false, false},
		// another function or text that reads the zone
		{"SELECT toStartOfHour(ts) AS t, count() AS c" + from + " AND toHour(ts) > 8 GROUP BY t", true, false},
		{"SELECT toStartOfHour(ts) AS t, uniq(toDate(ts)) AS c" + from + " GROUP BY t", true, false},
		{"SELECT toStartOfHour(ts) AS t, count() AS c" + from + " AND d = today() GROUP BY t", true, false},
		{"SELECT toStartOfHour(ts) AS t, count() AS c" + from + " AND other > '2024-01-01 00:00:00' GROUP BY t", true, false},
		// a bound read as text or a date, unless its text names its zone
		{"SELECT toStartOfHour(ts) AS t, count() AS c FROM e WHERE ts >= '2023-11-14 22:00:00' AND ts < '2023-11-14 23:00:00' GROUP BY t", true, true},
		{"SELECT toStartOfHour(ts) AS t, count() AS c FROM e WHERE ts >= toDateTime64('2023-11-14 22:00:00', 3, 'UTC') " +
			"AND ts < toDateTime64('2023-11-14 23:00:00', 3, 'UTC') GROUP BY t", false, false},
		{"SELECT toStartOfHour(ts) AS t, count() AS c" + from + " AND other > toDateTime('2024-01-01 00:00:00', 'Asia/Tokyo') GROUP BY t",
			false, false},
	} {
		analysis := NewAnalyzer(Options{}).Analyze(c.query, time.Unix(1_700_010_000, 0))
		if analysis.Plan == nil {
			t.Fatalf("%s: %v", c.query, analysis.Err)
		}
		if analysis.Plan.ReadsZone != c.readsZone || analysis.Plan.ZonedBounds != c.bounded {
			t.Errorf("%s: reads zone %v, zoned bounds %v", c.query, analysis.Plan.ReadsZone, analysis.Plan.ZonedBounds)
		}
	}
	// a DateTime64 bound names a zone other than UTC, which isn't read as the analysis reads it
	if a := NewAnalyzer(Options{}).Analyze("SELECT toStartOfHour(ts) AS t, count() AS c FROM e WHERE "+
		"ts >= toDateTime64('2023-11-14 22:00:00', 3, 'Asia/Tokyo') AND ts < toDateTime(1700003600) GROUP BY t",
		time.Unix(1_700_010_000, 0)); a.Plan != nil {
		t.Error("a DateTime64 bound in another zone was analyzed")
	}
	for name, want := range map[string]bool{"toStartOfDay": true, "TODATE": true, "formatDateTime": true,
		"fromUnixTimestamp64Milli": true, "toUInt32": false, "count": false, "toDateTime": false} {
		if zoneFunction(name) != want {
			t.Errorf("%s: %v", name, !want)
		}
	}
}
