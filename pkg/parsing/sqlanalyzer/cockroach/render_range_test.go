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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	rangeSelect = `SELECT date_bin(INTERVAL '1 minute', time) AS time, avg(v) AS v FROM cpu ` +
		`WHERE time >= '2024-01-01T00:00:07Z'`
	rangeGroup  = " GROUP BY 1"
	rangeLower  = `"time" >= '2024-01-01T00:00:07Z'`
	rangeClosed = " AND time < '2024-01-01T00:05:13Z'"
)

func TestRenderRange(t *testing.T) {
	a := newDataFusionAnalyzer()
	now := time.Unix(1704153607, 0)
	u := func(sec int64) time.Time { return time.Unix(sec, 0) }
	start := timeseries.PartialBucket{Lower: u(1704067207), Upper: u(1704067260)}
	end := timeseries.PartialBucket{Lower: u(1704067500), Upper: u(1704067513)}
	live := timeseries.PartialBucket{Lower: u(1704067500)}
	tests := []struct {
		name, where string
		pb          timeseries.PartialBucket
		want        []string
		unwanted    string
		err         error
	}{
		{"start edge", rangeClosed, start, []string{rangeLower, `"time" < '2024-01-01T00:01:00Z'`}, "", nil},
		{
			"end edge", rangeClosed, end,
			[]string{`"time" >= '2024-01-01T00:05:00Z'`, `"time" < '2024-01-01T00:05:13Z'`},
			"", nil,
		},
		// an inclusive upper renders one tick below the exclusive end, or at the client's own value
		{
			"start edge of an inclusive range", " AND time <= '2024-01-01T00:05:13Z'", start,
			[]string{rangeLower, `"time" <= '2024-01-01T00:00:59.999999999Z'`},
			"", nil,
		},
		{
			"inclusive end edge", " AND time <= '2024-01-01T00:05:13Z'",
			timeseries.PartialBucket{Lower: u(1704067500), Upper: u(1704067513), UpperInclusive: true},
			[]string{`"time" <= '2024-01-01T00:05:13Z'`},
			"", nil,
		},
		// a statement with no upper bound sends a range running to now with none
		{"open live bucket", "", live, []string{`"time" >= '2024-01-01T00:05:00Z'`}, `"time" <`, nil},
		{"now() live bucket", " AND time < now()", live, []string{`"time" < now()`}, "", nil},
		{"now() closed range", " AND time < now()", end, []string{`"time" < '2024-01-01T00:05:13Z'`}, "now()", nil},
		{
			"exclusive lower", rangeClosed,
			timeseries.PartialBucket{Lower: u(1704067207), Upper: u(1704067260), LowerExclusive: true},
			nil, "", sqlanalyzer.ErrUnsupportedRange,
		},
		{
			"inclusive range for an exclusive statement", rangeClosed,
			timeseries.PartialBucket{Lower: u(1704067500), Upper: u(1704067513), UpperInclusive: true},
			nil, "", sqlanalyzer.ErrUnsupportedRange,
		},
		{"open range for a closed statement", rangeClosed, live, nil, "", sqlanalyzer.ErrUnsupportedRange},
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
	a := newDataFusionAnalyzer()
	now := time.Unix(1704153607, 0)
	// current_timestamp is read as now, but it can't be written back unchanged in every dialect
	for where, want := range map[string]bool{
		" AND time < now()":                       true,
		" AND time <= now()":                      true,
		" AND time < now() - INTERVAL '1 minute'": false,
		" AND time <= CURRENT_TIMESTAMP":          false,
		rangeClosed:                               false,
	} {
		analysis := a.Analyze(rangeSelect+where+rangeGroup, now)
		if analysis.Plan == nil {
			t.Fatalf("%s: Analyze() = %s/%s (%v)", where, analysis.Mode, analysis.Reason, analysis.Err)
		}
		if analysis.Plan.UpperIsNow != want {
			t.Errorf("%s: UpperIsNow = %t", where, analysis.Plan.UpperIsNow)
		}
		if requested := analysis.Plan.RequestedRange(now); requested.OpenEnded != want {
			t.Errorf("%s: requested = %+v", where, requested)
		}
	}
}
