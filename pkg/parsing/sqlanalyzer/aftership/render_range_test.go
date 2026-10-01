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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	rangeSelect = "SELECT toStartOfMinute(ts) AS t, count() AS c FROM e WHERE ts >= toDateTime(1756671607)"
	rangeGroup  = " GROUP BY t"
	rangeLower  = "ts >= toDateTime(1756671607)"
)

func TestRenderRange(t *testing.T) {
	a := NewAnalyzer(Options{})
	now := time.Unix(1756758100, 0)
	u := func(sec int64) time.Time { return time.Unix(sec, 0) }
	start := timeseries.PartialBucket{Lower: u(1756671607), Upper: u(1756671660)}
	end := timeseries.PartialBucket{Lower: u(1756671900), Upper: u(1756671913)}
	live := timeseries.PartialBucket{Lower: u(1756671900)}
	tests := []struct {
		name, where string
		pb          timeseries.PartialBucket
		want        []string
		unwanted    string
		err         error
	}{
		{
			"start edge", " AND ts < toDateTime(1756671913)", start,
			[]string{rangeLower, "ts < toDateTime(1756671660)"},
			"", nil,
		},
		{
			"end edge", " AND ts < toDateTime(1756671913)", end,
			[]string{"ts >= toDateTime(1756671900)", "ts < toDateTime(1756671913)"},
			"", nil,
		},
		// an inclusive upper renders one tick below the exclusive end, or at the client's own value
		{
			"start edge of an inclusive range", " AND ts <= toDateTime(1756671913)", start,
			[]string{rangeLower, "ts <= toDateTime64('2025-08-31 20:20:59.999999999', 9, 'UTC')"},
			"", nil,
		},
		{
			"inclusive end edge", " AND ts <= toDateTime(1756671913)",
			timeseries.PartialBucket{Lower: u(1756671900), Upper: u(1756671913), UpperInclusive: true},
			[]string{"ts <= toDateTime64('2025-08-31 20:25:13', 9, 'UTC')"},
			"", nil,
		},
		// a statement with no upper bound sends a range running to now with none
		{"open live bucket", "", live, []string{"ts >= toDateTime(1756671900)"}, "ts <", nil},
		{
			"now() live bucket", " AND ts < now()", live,
			[]string{"ts >= toDateTime(1756671900)", "ts < now()"},
			"", nil,
		},
		{"now() closed range", " AND ts < now()", end, []string{"ts < 1756671913"}, "now()", nil},
		{
			"exclusive lower", " AND ts < toDateTime(1756671913)",
			timeseries.PartialBucket{Lower: u(1756671607), Upper: u(1756671660), LowerExclusive: true},
			nil, "", sqlanalyzer.ErrUnsupportedRange,
		},
		{
			"inclusive range for an exclusive statement", " AND ts < toDateTime(1756671913)",
			timeseries.PartialBucket{Lower: u(1756671900), Upper: u(1756671913), UpperInclusive: true},
			nil, "", sqlanalyzer.ErrUnsupportedRange,
		},
		{
			"open range for a closed statement", " AND ts < toDateTime(1756671913)", live,
			nil, "", sqlanalyzer.ErrUnsupportedRange,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := a.Analyze(rangeSelect+test.where+rangeGroup, now)
			if analysis.Plan == nil {
				t.Fatalf("Analyze() = %s/%s (%v)", analysis.Mode, analysis.Reason, analysis.Err)
			}
			got, err := analysis.Plan.RenderRange(test.pb)
			if !errors.Is(err, test.err) {
				t.Fatalf("got error %v want %v", err, test.err)
			}
			for _, want := range test.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in %s", want, got)
				}
			}
			if test.unwanted != "" && strings.Contains(got, test.unwanted) {
				t.Errorf("unexpected %q in %s", test.unwanted, got)
			}
		})
	}
}

func TestUpperIsNow(t *testing.T) {
	a := NewAnalyzer(Options{})
	now := time.Unix(1756758100, 0)
	for where, want := range map[string]bool{
		" AND ts < now()":                  true,
		" AND ts <= now64(3)":              true,
		" AND ts < now() - 60":             false,
		" AND ts < toDateTime(now())":      false,
		" AND ts < toDateTime64(now(), 3)": false,
		" AND ts < toDateTime(1756671913)": false,
		" AND t <= toDateTime(1756758000)": false,
	} {
		analysis := a.Analyze(rangeSelect+where+rangeGroup, now)
		if analysis.Plan == nil {
			t.Fatalf("%s: Analyze() = %s/%s (%v)", where, analysis.Mode, analysis.Reason, analysis.Err)
		}
		if analysis.Plan.UpperIsNow != want {
			t.Errorf("%s: UpperIsNow = %t", where, analysis.Plan.UpperIsNow)
		}
		requested := analysis.Plan.RequestedRange(now)
		if requested.OpenEnded != want || want && !requested.End.Equal(now) {
			t.Errorf("%s: requested = %+v", where, requested)
		}
	}
}
