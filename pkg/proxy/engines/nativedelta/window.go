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

package nativedelta

import (
	"errors"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Window is a delta request window: the complete buckets it serves from the delta tier and the
// partial buckets at its edges.
type Window struct {
	// Output is the inclusive-bucket extent of the complete buckets the response holds.
	Output timeseries.Extent
	// Cacheable is the extent list eligible for delta caching.
	Cacheable timeseries.ExtentList
	// Partials are the edge buckets fetched through the object tier and never delta-cached.
	Partials     [2]timeseries.PartialBucket
	PartialCount uint8
	// Empty indicates the window contains no complete bucket.
	Empty bool
}

// ErrUnsupportedBounds indicates a plan whose bounds cannot form a delta
// window; callers should proxy the original statement instead.
var ErrUnsupportedBounds = errors.New("unsupported delta request bounds")

// BuildWindow plans a delta window from raw bounds under a mode, never with a still-filling complete
// bucket; requireUpperBound rejects open plans, which otherwise run to now
func BuildWindow(plan *sqlanalyzer.QueryPlan, now time.Time, requireUpperBound bool,
	mode timeseries.StepAlignment,
) (Window, error) {
	if plan == nil || plan.Step <= 0 || plan.LowerBound == nil ||
		!plan.LowerBound.Inclusive ||
		(plan.UpperBound == nil && requireUpperBound) ||
		(plan.UpperBound != nil && requireUpperBound && plan.UpperBound.Inclusive) {
		return Window{}, ErrUnsupportedBounds
	}
	r := plan.RequestedRange(now)
	if r.End.Before(r.Start) {
		return Window{}, ErrUnsupportedBounds
	}
	p := timeseries.PlanRange(r, plan.Step, plan.Phase, timeseries.SampleModelBucket, mode, now)
	if !p.Full {
		lower := timeseries.CeilToGrid(r.Start, plan.Step, plan.Phase)
		return Window{Output: timeseries.Extent{Start: lower, End: lower}, Empty: true}, nil
	}
	return Window{
		Output: p.Interior, Cacheable: timeseries.ExtentList{p.Interior},
		Partials: p.Partials, PartialCount: p.PartialCount,
	}, nil
}

// StableExtents removes buckets newer than now - window, floored to the phased grid, from a
// cache entry's extents, and always removes the still-aggregating bucket containing now.
func StableExtents(extents timeseries.ExtentList, step, phase time.Duration,
	window time.Duration, now time.Time,
) timeseries.ExtentList {
	if len(extents) == 0 || step <= 0 {
		return extents
	}
	cutoff := timeseries.FloorToGrid(now.Add(-max(window, 0)), step, phase)
	if cutoff.After(extents[len(extents)-1].End) {
		return extents
	}
	if !cutoff.After(extents[0].Start) {
		return timeseries.ExtentList{}
	}
	volatile := timeseries.ExtentList{{Start: cutoff, End: extents[len(extents)-1].End}}
	return extents.Remove(volatile, step)
}

// VolatileWindow returns a plan's backfill tolerance: the largest of the configured duration,
// the configured points times the plan's step, and the query's requested tolerance.
func VolatileWindow(configured time.Duration, points int, step,
	requested time.Duration,
) time.Duration {
	return max(configured, time.Duration(points)*step, requested)
}
