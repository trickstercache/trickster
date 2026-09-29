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
	step                           time.Duration
	interior, partial              time.Duration
	inclusiveEnd, fails, closeOpen bool
}

func (r rangeRenderer) RenderExtent(e timeseries.Extent) (string, error) {
	// "lower,upper,lowerExclusive,upperInclusive" in Unix seconds, which rangeAnalyzer reads back; the
	// shifts and flags model faulty renderers
	if r.fails {
		return "", errors.New("render failed")
	}
	upper, inclusive := e.End.Add(r.step), false
	if r.inclusiveEnd {
		// the tick-below-the-boundary spelling of the same exclusive end
		upper, inclusive = upper.Add(-time.Second), true
	}
	return fmt.Sprintf("%d,%d,false,%t", e.Start.Add(r.interior).Unix(), upper.Unix(), inclusive), nil
}

func (r rangeRenderer) RenderRange(pb timeseries.PartialBucket) (string, error) {
	upper := int64(0)
	switch {
	case !pb.Upper.IsZero():
		upper = pb.Upper.Unix()
	case r.closeOpen:
		upper = pb.Lower.Add(r.step).Unix()
	}
	return fmt.Sprintf("%d,%d,%t,%t", pb.Lower.Add(r.partial).Unix(), upper, pb.LowerExclusive, pb.UpperInclusive), nil
}

type rangeAnalyzer struct{ step time.Duration }

func (a rangeAnalyzer) Analyze(statement string, _ time.Time) sqlanalyzer.Analysis {
	var lower, upper int64
	var lowerExclusive, upperInclusive bool
	if _, err := fmt.Sscanf(statement, "%d,%d,%t,%t", &lower, &upper, &lowerExclusive, &upperInclusive); err != nil {
		return sqlanalyzer.Analysis{Mode: sqlanalyzer.CacheModeObject, Err: err}
	}
	plan := &sqlanalyzer.QueryPlan{
		Step: a.step, RawLower: &sqlanalyzer.Bound{Value: time.Unix(lower, 0), Inclusive: !lowerExclusive},
	}
	if upper != 0 {
		plan.RawUpper = &sqlanalyzer.Bound{Value: time.Unix(upper, 0), Inclusive: upperInclusive}
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
		name     string
		plan     *sqlanalyzer.QueryPlan
		compared int
		fails    bool
	}{
		// truncate, drop and partial each render their interior, and partial its two edges
		{"faithful", plan(rangeRenderer{}, 30, 630), 5, false},
		{"an inclusive end a tick below the boundary", plan(rangeRenderer{inclusiveEnd: true}, 30, 630), 5, false},
		{"an open end", plan(rangeRenderer{}, 30, 0), 5, false},
		{"an aligned range has no partial buckets", plan(rangeRenderer{}, 60, 600), 3, false},
		{"no complete bucket", plan(rangeRenderer{}, 10, 50), 0, false},
		{"an open end rendered closed", plan(rangeRenderer{closeOpen: true}, 30, 0), 4, true},
		{"an interior a bucket late", plan(rangeRenderer{interior: time.Minute}, 30, 630), 0, true},
		{"a partial bucket rendered whole", plan(rangeRenderer{partial: -30 * time.Second}, 30, 630), 3, true},
		{"a failing renderer", plan(rangeRenderer{fails: true}, 30, 630), 0, true},
		{"no raw bounds", &sqlanalyzer.QueryPlan{Step: time.Minute}, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			compared, err := RenderParity(a, test.plan, now)
			if compared != test.compared || (err != nil) != test.fails {
				t.Errorf("got %d compared, err %v", compared, err)
			}
		})
	}
	// a rendered statement the analyzer won't plan
	if _, err := RenderParity(objectAnalyzer{}, plan(rangeRenderer{}, 30, 630), now); err == nil {
		t.Fatal("a statement read back off the delta path passed")
	}
}

type objectAnalyzer struct{}

func (objectAnalyzer) Analyze(string, time.Time) sqlanalyzer.Analysis {
	return sqlanalyzer.Analysis{Mode: sqlanalyzer.CacheModeObject}
}
