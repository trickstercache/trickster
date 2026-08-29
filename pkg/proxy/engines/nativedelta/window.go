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

// Window is a delta request window normalized to the plan's bucket cadence.
type Window struct {
	// Output is the inclusive-bucket extent of the client's request.
	Output timeseries.Extent
	// Cacheable is the extent list eligible for delta caching.
	Cacheable timeseries.ExtentList
	// Lower and Upper are the normalized half-open time bounds.
	Lower, Upper time.Time
	// Empty indicates the window contains no complete bucket.
	Empty bool
}

// ErrUnsupportedBounds indicates a plan whose bounds cannot form a delta
// window; callers should proxy the original statement instead.
var ErrUnsupportedBounds = errors.New("unsupported delta request bounds")

// BuildWindow converts a plan's comparator-preserving bounds into a bucket
// window. Bounds are rounded inward to the cadence (lower up, upper down), so
// partial edge buckets are excluded. When requireUpperBound is false, a plan
// without an upper bound runs to the present: the window includes the bucket
// containing now, and storage-side volatility (StableExtents) keeps that
// still-filling bucket out of the cache.
func BuildWindow(plan *sqlanalyzer.QueryPlan, now time.Time,
	requireUpperBound bool,
) (Window, error) {
	if plan == nil || plan.Step <= 0 || plan.LowerBound == nil ||
		!plan.LowerBound.Inclusive ||
		(plan.UpperBound == nil && requireUpperBound) ||
		(plan.UpperBound != nil && requireUpperBound && plan.UpperBound.Inclusive) {
		return Window{}, ErrUnsupportedBounds
	}
	rawLower := plan.LowerBound.Value
	var rawUpper time.Time
	switch {
	case plan.UpperBound == nil:
		rawUpper = sqlanalyzer.FloorBucket(now, plan.Step, plan.Phase).Add(plan.Step)
	case plan.UpperBound.Inclusive:
		// an inclusive upper names the final bucket; the equivalent exclusive
		// bound is one cadence beyond it
		rawUpper = plan.UpperBound.Value.Add(plan.Step)
	default:
		rawUpper = plan.UpperBound.Value
	}
	if rawUpper.Before(rawLower) {
		return Window{}, ErrUnsupportedBounds
	}
	lower := sqlanalyzer.CeilBucket(rawLower, plan.Step, plan.Phase)
	upper := sqlanalyzer.FloorBucket(rawUpper, plan.Step, plan.Phase)
	if rawUpper.Sub(rawLower) < plan.Step || lower.After(upper) {
		upper = lower
	}
	window := Window{Lower: lower, Upper: upper}
	if lower.Equal(upper) {
		window.Output = timeseries.Extent{Start: lower, End: lower}
		window.Empty = true
		return window, nil
	}
	requested := timeseries.Extent{Start: lower, End: upper.Add(-plan.Step)}
	window.Output = requested
	window.Cacheable = timeseries.ExtentList{requested}
	return window, nil
}

// StableExtents removes the volatile tail — everything newer than
// now - window, truncated to the cadence — from the extents recorded against
// a cache entry, so recently written buckets are refetched rather than served
// stale from cache. A non-positive window disables trimming.
func StableExtents(extents timeseries.ExtentList, step time.Duration,
	window time.Duration, now time.Time,
) timeseries.ExtentList {
	if window <= 0 || len(extents) == 0 || step <= 0 {
		return extents
	}
	cutoff := now.Add(-window).Truncate(step)
	if cutoff.After(extents[len(extents)-1].End) {
		return extents
	}
	if !cutoff.After(extents[0].Start) {
		return timeseries.ExtentList{}
	}
	volatile := timeseries.ExtentList{{Start: cutoff, End: extents[len(extents)-1].End}}
	return extents.Remove(volatile, step)
}
