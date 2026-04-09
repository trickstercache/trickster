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

package dataset

import (
	"math"
	"slices"
	"strconv"
)

// sortAndAggregate sorts points by epoch and aggregates values at matching
// epochs using the specified strategy. For MergeStrategyDedup, it falls back
// to the existing sortAndDedupe behavior.
func sortAndAggregate(p Points, strategy MergeStrategy) Points {
	if strategy == MergeStrategyDedup {
		return sortAndDedupe(p)
	}
	if len(p) <= 1 {
		return p
	}
	// sort by epoch, keeping order between equal elements
	slices.SortStableFunc(p, func(a, b Point) int {
		if a.Epoch < b.Epoch {
			return -1
		} else if a.Epoch > b.Epoch {
			return 1
		}
		return 0
	})
	var k int
	var count int // tracks number of values aggregated at current epoch (for avg)
	for i := range p {
		if i == 0 {
			count = 1
			continue
		}
		if p[k].Epoch == p[i].Epoch {
			// same epoch: aggregate values
			aggregateValues(&p[k], &p[i], strategy)
			count++
			// for avg, we finalize after the run ends (see below)
		} else {
			// new epoch: finalize avg for previous run if needed
			if strategy == MergeStrategyAvg && count > 1 {
				finalizeAvg(&p[k], count)
			}
			count = 1
			k++
			if k < i {
				p[k] = p[i]
			}
		}
	}
	// finalize avg for the last run
	if strategy == MergeStrategyAvg && count > 1 {
		finalizeAvg(&p[k], count)
	}
	return p[:k+1]
}

// aggregateValues combines the value from src into dst using the given strategy.
// Both points are expected to have at least one value in Values[0] as a string-encoded float.
func aggregateValues(dst, src *Point, strategy MergeStrategy) {
	if len(dst.Values) == 0 || len(src.Values) == 0 {
		return
	}
	dv := parseFloat(dst.Values[0])
	sv := parseFloat(src.Values[0])
	var result float64
	switch strategy {
	case MergeStrategySum, MergeStrategyAvg, MergeStrategyCount:
		// sum/count: accumulate totals; avg: accumulate sum, divide later in finalizeAvg
		result = dv + sv
	case MergeStrategyMin:
		result = math.Min(dv, sv)
	case MergeStrategyMax:
		result = math.Max(dv, sv)
	default:
		result = sv // dedup fallback
	}
	dst.Values[0] = strconv.FormatFloat(result, 'f', -1, 64)
}

// finalizeAvg divides the accumulated sum in p by count.
func finalizeAvg(p *Point, count int) {
	if len(p.Values) == 0 || count <= 1 {
		return
	}
	v := parseFloat(p.Values[0])
	v /= float64(count)
	p.Values[0] = strconv.FormatFloat(v, 'f', -1, 64)
}

// parseFloat extracts a float64 from a point value. Returns NaN if unparsable.
func parseFloat(v any) float64 {
	switch val := v.(type) {
	case string:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return math.NaN()
		}
		return f
	case float64:
		return val
	default:
		return math.NaN()
	}
}

// MergePointsWithStrategy merges two Points slices using the specified strategy.
// For MergeStrategyDedup, this behaves identically to MergePoints.
func MergePointsWithStrategy(p, p2 Points, sortPoints bool, strategy MergeStrategy) Points {
	if strategy == MergeStrategyDedup {
		return MergePoints(p, p2, sortPoints)
	}
	if p == nil && p2 == nil {
		return nil
	}
	if len(p) == 0 && len(p2) == 0 {
		return Points{}
	}
	// For count strategy, we need to initialize src values to 1 before merging
	if strategy == MergeStrategyCount {
		p = initCountValues(p)
		p2 = initCountValues(p2)
	}
	finalize := func(out Points) Points {
		if sortPoints && len(out) > 1 {
			out = sortAndAggregate(out, strategy)
		}
		return out
	}
	if len(p2) == 0 {
		return finalize(p.Clone())
	} else if len(p) == 0 {
		return finalize(p2.Clone())
	}
	out := make(Points, len(p)+len(p2))
	copy(out, p)
	copy(out[len(p):], p2)
	return finalize(out)
}

// initCountValues clones points and sets each value to "1" (each point represents one observation).
func initCountValues(p Points) Points {
	if len(p) == 0 {
		return p
	}
	out := p.Clone()
	for i := range out {
		if len(out[i].Values) > 0 {
			out[i].Values[0] = "1"
		}
	}
	return out
}
