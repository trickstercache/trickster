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

package sqlanalyzertest

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestDropParity(t *testing.T) {
	now := time.Unix(100_000, 0)
	at := func(v int64) *sqlanalyzer.Bound { return &sqlanalyzer.Bound{Value: time.Unix(v, 0), Inclusive: true} }
	exclusive := func(v int64) *sqlanalyzer.Bound { return &sqlanalyzer.Bound{Value: time.Unix(v, 0)} }
	plan := func(rawLower, rawUpper, lower, upper *sqlanalyzer.Bound) *sqlanalyzer.QueryPlan {
		return &sqlanalyzer.QueryPlan{
			Step: time.Minute, RawLower: rawLower, RawUpper: rawUpper, LowerBound: lower, UpperBound: upper,
		}
	}
	tests := []struct {
		name     string
		plan     *sqlanalyzer.QueryPlan
		compared bool
		fails    bool
	}{
		{"no plan", nil, false, true},
		{"no raw bounds", plan(nil, nil, at(60), exclusive(600)), false, true},
		{"agreeing inward rounding", plan(at(30), exclusive(630), at(60), exclusive(600)), true, false},
		{"a rounded extent one bucket wider", plan(at(30), exclusive(630), at(0), exclusive(600)), true, true},
		{"no complete bucket on either side", plan(at(10), exclusive(50), at(60), exclusive(0)), true, false},
		{"complete buckets on one side only", plan(at(10), exclusive(50), at(0), exclusive(60)), true, true},
		{"open ended", plan(at(30), nil, at(60), nil), false, false},
		{"reaching past now", plan(at(30), exclusive(200_000), at(60), exclusive(199_980)), false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compared, err := DropParity(test.plan, now)
			if compared != test.compared || (err != nil) != test.fails {
				t.Errorf("got compared %t, err %v", compared, err)
			}
		})
	}
}

type rangeRenderer struct {
	step                         time.Duration
	interior, partial, tick      time.Duration
	fails, closeOpen, atBoundary bool
}

func (r rangeRenderer) RenderExtent(e timeseries.Extent) (string, error) {
	// "lower,upper,lowerExclusive,upperInclusive" in Unix nanoseconds for rangeAnalyzer; a tick writes
	// the end inclusively that far below, and the other fields model faults
	if r.fails {
		return "", errors.New("render failed")
	}
	upper, inclusive := e.End.Add(r.step), false
	if r.tick > 0 || r.atBoundary {
		upper, inclusive = upper.Add(-r.tick), true
	}
	return fmt.Sprintf("%d,%d,false,%t", e.Start.Add(r.interior).UnixNano(), upper.UnixNano(), inclusive), nil
}

func (r rangeRenderer) RenderRange(pb timeseries.PartialBucket) (string, error) {
	upper := int64(0)
	switch {
	case !pb.Upper.IsZero():
		upper = pb.Upper.UnixNano()
	case r.closeOpen:
		upper = pb.Lower.Add(r.step).UnixNano()
	}
	return fmt.Sprintf("%d,%d,%t,%t", pb.Lower.Add(r.partial).UnixNano(), upper, pb.LowerExclusive,
		pb.UpperInclusive), nil
}

type rangeAnalyzer struct{ step time.Duration }

func (a rangeAnalyzer) Analyze(statement string, _ time.Time) sqlanalyzer.Analysis {
	var lower, upper int64
	var lowerExclusive, upperInclusive bool
	if _, err := fmt.Sscanf(statement, "%d,%d,%t,%t", &lower, &upper, &lowerExclusive, &upperInclusive); err != nil {
		return sqlanalyzer.Analysis{Mode: sqlanalyzer.CacheModeObject, Err: err}
	}
	plan := &sqlanalyzer.QueryPlan{
		Step: a.step, RawLower: &sqlanalyzer.Bound{Value: time.Unix(0, lower), Inclusive: !lowerExclusive},
	}
	if upper != 0 {
		plan.RawUpper = &sqlanalyzer.Bound{Value: time.Unix(0, upper), Inclusive: upperInclusive}
	}
	return sqlanalyzer.Analysis{Mode: sqlanalyzer.CacheModeDelta, Plan: plan}
}

func TestRenderParity(t *testing.T) {
	now := time.Unix(100_000, 0)
	a := rangeAnalyzer{step: time.Minute}
	plan := func(r rangeRenderer, lower, upper int64) *sqlanalyzer.QueryPlan {
		r.step = time.Minute
		p := &sqlanalyzer.QueryPlan{
			Step: time.Minute, Renderer: r,
			RawLower: &sqlanalyzer.Bound{Value: time.Unix(lower, 0), Inclusive: true},
		}
		if upper != 0 {
			p.RawUpper = &sqlanalyzer.Bound{Value: time.Unix(upper, 0)}
		}
		return p
	}
	for _, test := range []struct {
		name      string
		plan      *sqlanalyzer.QueryPlan
		precision time.Duration
		compared  int
		fails     bool
	}{
		// truncate, drop and partial each render their interior, and partial its two edges
		{"faithful", plan(rangeRenderer{}, 30, 630), time.Nanosecond, 5, false},
		// a whole-second column holds no row between X-1s and X, so <= X-1s is < X
		{
			"an inclusive end a second below on whole seconds", plan(rangeRenderer{tick: time.Second}, 30, 630),
			time.Second, 5, false,
		},
		{
			"an inclusive end a millisecond below on milliseconds", plan(rangeRenderer{tick: time.Millisecond}, 30, 630),
			time.Millisecond, 5, false,
		},
		{
			"an inclusive end a nanosecond below on whole seconds", plan(rangeRenderer{tick: time.Nanosecond}, 30, 630),
			time.Second, 5, false,
		},
		// but it drops a millisecond column's rows in that last second
		{
			"an inclusive end a second below on milliseconds", plan(rangeRenderer{tick: time.Second}, 30, 630),
			time.Millisecond, 0, true,
		},
		// <= X also selects the row at X, which < X doesn't
		{"an inclusive end at the boundary", plan(rangeRenderer{atBoundary: true}, 30, 630), time.Second, 0, true},
		{"an inclusive end with no precision", plan(rangeRenderer{tick: time.Second}, 30, 630), 0, 0, true},
		{"an open end", plan(rangeRenderer{}, 30, 0), time.Nanosecond, 5, false},
		{"an aligned range has no partial buckets", plan(rangeRenderer{}, 60, 600), time.Nanosecond, 3, false},
		{"no complete bucket", plan(rangeRenderer{}, 10, 50), time.Nanosecond, 0, false},
		{"an open end rendered closed", plan(rangeRenderer{closeOpen: true}, 30, 0), time.Nanosecond, 4, true},
		{"an interior a bucket late", plan(rangeRenderer{interior: time.Minute}, 30, 630), time.Nanosecond, 0, true},
		{
			"a partial bucket rendered whole", plan(rangeRenderer{partial: -30 * time.Second}, 30, 630),
			time.Nanosecond, 3, true,
		},
		{"a failing renderer", plan(rangeRenderer{fails: true}, 30, 630), time.Nanosecond, 0, true},
		{"no raw bounds", &sqlanalyzer.QueryPlan{Step: time.Minute}, time.Nanosecond, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			compared, err := RenderParity(a, test.plan, now, test.precision)
			if compared != test.compared || (err != nil) != test.fails {
				t.Errorf("got %d compared, err %v", compared, err)
			}
		})
	}
	// a rendered statement the analyzer won't plan
	if _, err := RenderParity(objectAnalyzer{}, plan(rangeRenderer{}, 30, 630), now, time.Nanosecond); err == nil {
		t.Fatal("a statement read back off the delta path passed")
	}
}

type objectAnalyzer struct{}

func (objectAnalyzer) Analyze(string, time.Time) sqlanalyzer.Analysis {
	return sqlanalyzer.Analysis{Mode: sqlanalyzer.CacheModeObject}
}

func TestSameRange(t *testing.T) {
	at := func(ms float64) time.Time { return time.Unix(0, int64(ms*float64(time.Millisecond))) }
	upper := func(ms float64, inclusive bool) timeseries.RequestedRange {
		return timeseries.RequestedRange{Start: at(0), End: at(ms), EndInclusive: inclusive}
	}
	lower := func(ms float64, exclusive bool) timeseries.RequestedRange {
		return timeseries.RequestedRange{Start: at(ms), StartExclusive: exclusive, End: at(120_000)}
	}
	for _, test := range []struct {
		name      string
		got, want timeseries.RequestedRange
		precision time.Duration
		same      bool
	}{
		{"equal", upper(60_000, false), upper(60_000, false), time.Millisecond, true},
		{"<= one step below < on the grid", upper(59_999, true), upper(60_000, false), time.Millisecond, true},
		{"<= a nanosecond below < on the grid", upper(59_999.999999, true), upper(60_000, false), time.Millisecond, true},
		{"<= two steps below < on the grid", upper(59_998, true), upper(60_000, false), time.Millisecond, false},
		{"<= at the < boundary", upper(60_000, true), upper(60_000, false), time.Millisecond, false},
		// < 60.0005s admits the row at 60.000s, which <= 59.9996s leaves out
		{"<= below an off-grid <", upper(59_999.6, true), upper(60_000.5, false), time.Millisecond, false},
		{"<= at the row an off-grid < admits", upper(60_000, true), upper(60_000.5, false), time.Millisecond, true},
		{"off-grid < selecting the same rows", upper(60_000.8, false), upper(60_000.5, false), time.Millisecond, true},
		{"off-grid < across a row", upper(60_001.2, false), upper(60_000.5, false), time.Millisecond, false},
		{"> one step below >= on the grid", lower(59_999, true), lower(60_000, false), time.Millisecond, true},
		// > 59.9986s admits the row at 59.999s, which >= 59.9995s leaves out
		{"> below an off-grid >=", lower(59_998.6, true), lower(59_999.5, false), time.Millisecond, false},
		{
			"> and an off-grid >= admitting the same first row", lower(59_999.4, true), lower(59_999.5, false),
			time.Millisecond, true,
		},
		{"> at the row before an off-grid >=", lower(59_999, true), lower(59_999.5, false), time.Millisecond, true},
		{"negative instants", upper(-59_999, true), upper(-59_998, false), time.Millisecond, true},
		{"no precision is exact", upper(59_999, true), upper(60_000, false), 0, false},
		{"no precision, equal", upper(60_000, false), upper(60_000, false), 0, true},
		{"no precision, <= and < at one instant", upper(60_000, true), upper(60_000, false), 0, false},
		{"no precision, > and >= at one instant", lower(60_000, true), lower(60_000, false), 0, false},
		{
			"an open range",
			timeseries.RequestedRange{Start: at(0), End: at(1), OpenEnded: true},
			timeseries.RequestedRange{Start: at(0)},
			time.Millisecond, true,
		},
		{
			"a closed range for an open one", upper(60_000, false),
			timeseries.RequestedRange{Start: at(0)},
			time.Millisecond, false,
		},
		{
			"an open range for a closed one",
			timeseries.RequestedRange{Start: at(0), End: at(60_000), OpenEnded: true},
			upper(60_000, false), time.Millisecond, false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sameRange(test.got, test.want, test.precision); got != test.same {
				t.Errorf("sameRange = %t", got)
			}
		})
	}
}
