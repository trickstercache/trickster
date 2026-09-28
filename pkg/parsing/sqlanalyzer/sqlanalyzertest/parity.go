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
