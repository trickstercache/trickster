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

// Package sqlanalyzertest checks SQL dialect analyzers against the step alignment planner.
package sqlanalyzertest

import (
	"fmt"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// DropParity errs when a drop plan of the raw bounds differs from the analyzer's rounded extent,
// and returns false for a range past now, where the rounded extent keeps the live bucket
func DropParity(plan *sqlanalyzer.QueryPlan, now time.Time) (bool, error) {
	if plan == nil || plan.RawLower == nil {
		return false, fmt.Errorf("plan %v has no raw bounds", plan)
	}
	r := plan.RequestedRange(now)
	if r.OpenEnded || r.End.After(now) {
		return false, nil
	}
	rounded := plan.RequestExtent(now)
	p := timeseries.PlanRange(r, plan.Step, plan.Phase, timeseries.SampleModelBucket,
		timeseries.StepAlignmentDrop, now)
	if full := !rounded.End.Before(rounded.Start); p.Full != full {
		return true, fmt.Errorf("planner complete buckets %t, analyzer %t: raw %+v, rounded %s",
			p.Full, full, r, rounded)
	}
	if p.Full && (!p.Interior.Start.Equal(rounded.Start) || !p.Interior.End.Equal(rounded.End)) {
		return true, fmt.Errorf("planner interior %s, analyzer %s: raw %+v", p.Interior, rounded, r)
	}
	return true, nil
}

// RenderParity errs when a plan's statement, rendered for any mode's interior or partial buckets,
// reads back through a as another range; it returns how many ranges it compared
func RenderParity(a sqlanalyzer.DialectAnalyzer, plan *sqlanalyzer.QueryPlan, now time.Time) (int, error) {
	if plan == nil || plan.RawLower == nil {
		return 0, fmt.Errorf("plan %v has no raw bounds", plan)
	}
	r := plan.RequestedRange(now)
	compared := 0
	for _, mode := range []timeseries.StepAlignment{
		timeseries.StepAlignmentTruncate, timeseries.StepAlignmentDrop, timeseries.StepAlignmentPartial,
	} {
		p := timeseries.PlanRange(r, plan.Step, plan.Phase, timeseries.SampleModelBucket, mode, now)
		if !p.Full {
			continue
		}
		statement, err := plan.RenderExtent(p.Interior)
		if err != nil {
			return compared, fmt.Errorf("%s interior %s: %w", mode, p.Interior, err)
		}
		// the interior's statement holds exactly its complete buckets
		back, err := readBack(a, statement, now)
		if err != nil {
			return compared, fmt.Errorf("%s interior %s: %w", mode, p.Interior, err)
		}
		want := timeseries.RequestedRange{Start: p.Interior.Start, End: p.Interior.End.Add(plan.Step)}
		if !sameRange(back, want) {
			return compared, fmt.Errorf("%s interior %s reads back as %+v:\n%s", mode, p.Interior, back, statement)
		}
		compared++
		for _, pb := range p.Partials[:p.PartialCount] {
			statement, err := plan.RenderRange(pb)
			if err != nil {
				return compared, fmt.Errorf("%s %s partial %+v: %w", mode, pb.Edge, pb, err)
			}
			back, err := readBack(a, statement, now)
			if err != nil {
				return compared, fmt.Errorf("%s %s partial %+v: %w", mode, pb.Edge, pb, err)
			}
			want := timeseries.RequestedRange{
				Start: pb.Lower, End: pb.Upper, StartExclusive: pb.LowerExclusive, EndInclusive: pb.UpperInclusive,
			}
			if !sameRange(back, want) {
				return compared, fmt.Errorf("%s %s partial %+v reads back as %+v:\n%s", mode, pb.Edge, pb, back, statement)
			}
			compared++
		}
	}
	return compared, nil
}

func readBack(a sqlanalyzer.DialectAnalyzer, statement string, now time.Time) (timeseries.RequestedRange, error) {
	got := a.Analyze(statement, now)
	if got.Mode != sqlanalyzer.CacheModeDelta || got.Plan == nil {
		return timeseries.RequestedRange{}, fmt.Errorf("rendered statement is %s (%s: %w):\n%s", got.Mode,
			got.Reason, got.Err, statement)
	}
	return got.Plan.RequestedRange(now), nil
}

var ticks = [...]time.Duration{time.Nanosecond, time.Microsecond, time.Millisecond, time.Second}

func sameRange(got, want timeseries.RequestedRange) bool {
	// equal bounds, or an inclusive bound one literal tick inside an exclusive one; a zero wanted end
	// is an open range
	if want.End.IsZero() {
		return got.OpenEnded && sameBound(got.Start, want.Start, got.StartExclusive, want.StartExclusive, 1)
	}
	return !got.OpenEnded && sameBound(got.Start, want.Start, got.StartExclusive, want.StartExclusive, 1) &&
		sameBound(got.End, want.End, !got.EndInclusive, !want.EndInclusive, -1)
}

func sameBound(a, b time.Time, aOutside, bOutside bool, inward time.Duration) bool {
	// an outside bound excludes its instant: a lower exclusive or an upper exclusive one
	if aOutside == bOutside {
		return a.Equal(b)
	}
	outside, inside := a, b
	if bOutside {
		outside, inside = b, a
	}
	for _, tick := range ticks {
		if outside.Add(inward * tick).Equal(inside) {
			return true
		}
	}
	return false
}
