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

package timeseries

import "time"

// EdgePlan is a bucketed range's cacheable interior and the partial buckets at its edges
type EdgePlan struct {
	// Interior holds the complete buckets to serve from the time series cache, as inclusive labels
	Interior     Extent
	Partials     [2]PartialBucket
	PartialCount uint8
	// Full is false when the range holds no complete bucket, which leaves Interior and Partials empty
	Full bool
}

// PlanRange plans a bucketed range under a step alignment mode. Only buckets that ended by now form
// the interior; an inclusive or exclusive bound leaves its bucket partial
func PlanRange(r RequestedRange, step, phase time.Duration, model SampleModel,
	mode StepAlignment, now time.Time,
) EdgePlan {
	var p EdgePlan
	if step <= 0 {
		return p
	}
	startBucket := FloorToGrid(r.Start, step, phase)
	startAligned := !r.StartExclusive && startBucket.Equal(r.Start)
	firstFull := startBucket
	if !startAligned {
		firstFull = startBucket.Add(step)
	}
	// the bucket holding now is still filling, so it is never complete
	end := r.End
	if r.OpenEnded || end.After(now) {
		end = now
	}
	endBucket := FloorToGrid(end, step, phase)
	endAligned := !r.OpenEnded && !r.EndInclusive && end.Equal(r.End) && endBucket.Equal(end)
	if !firstFull.Before(endBucket) {
		return p
	}
	p.Full = true
	startPolicy, endPolicy := mode.Edges()
	p.Interior = Extent{Start: firstFull, End: endBucket.Add(-step)}
	if !startAligned {
		switch startPolicy {
		case EdgeTruncate:
			p.Interior.Start = startBucket
		case EdgePartial:
			p.Partials[p.PartialCount] = PartialBucket{
				Label: startBucket, Lower: r.Start, LowerExclusive: r.StartExclusive,
				Upper: firstFull, Edge: BucketEdgeStart,
			}
			p.PartialCount++
		}
	}
	if !endAligned && endPolicy == EdgePartial {
		pb := PartialBucket{Label: endBucket, Lower: endBucket, Edge: BucketEdgeEnd}
		if !r.OpenEnded {
			pb.Upper, pb.UpperInclusive = r.End, r.EndInclusive
		}
		p.Partials[p.PartialCount] = pb
		p.PartialCount++
	}
	if model == SampleModelBucketStop {
		p.shiftToStopLabels(step)
	}
	return p
}

func (p *EdgePlan) shiftToStopLabels(step time.Duration) {
	// windows labeled by their stop time: complete windows by their end on the grid, and partial
	// windows by the end of their rows, as Flux's _stop does
	p.Interior.Start = p.Interior.Start.Add(step)
	p.Interior.End = p.Interior.End.Add(step)
	for i := range p.PartialCount {
		pb := &p.Partials[i]
		if pb.Upper.IsZero() {
			pb.Label = pb.Label.Add(step)
			continue
		}
		pb.Label = pb.Upper
	}
}

// IsLive reports whether the partial bucket's span has not ended by now, so it is still filling
func (pb PartialBucket) IsLive(step, phase time.Duration, now time.Time) bool {
	return FloorToGrid(pb.Lower, step, phase).Add(step).After(now)
}

// PlanEdges sets Extent to the query's cacheable interior and Partials to its partial buckets under
// StepAlignment, returning false when the range holds no complete bucket
func (trq *TimeRangeQuery) PlanEdges(now time.Time) bool {
	switch trq.SampleModel {
	case SampleModelBucket, SampleModelBucketStop:
		if trq.Step > 0 && !trq.Requested.IsZero() {
			p := PlanRange(trq.Requested, trq.Step, trq.Phase, trq.SampleModel, trq.StepAlignment, now)
			trq.Partials, trq.PartialCount = p.Partials, p.PartialCount
			if p.Full {
				trq.Extent = p.Interior
			}
			return p.Full
		}
	}
	// instant and stored samples, and queries whose parser recorded no requested range, keep the
	// parser's extent and align it to the step
	trq.PartialCount = 0
	trq.alignExtent(now)
	return true
}
