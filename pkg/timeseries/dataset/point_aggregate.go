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
	return sortAndAggregateTolerant(p, strategy, 0)
}

// sortAndAggregateTolerant is sortAndAggregate with an opt-in tolerance window
// for the dedup strategy; non-dedup strategies ignore tolerance since
// aggregating across a multi-step window would change semantics that callers
// don't expect (sum-of-cluster, not sum-at-epoch).
func sortAndAggregateTolerant(p Points, strategy MergeStrategy, toleranceNanos int64) Points {
	if strategy == MergeStrategyDedup {
		return sortAndDedupeTolerant(p, toleranceNanos)
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
	dNaN := math.IsNaN(dv)
	sNaN := math.IsNaN(sv)
	if dNaN && sNaN {
		return // both non-numeric (e.g. histograms): keep dst as-is
	}
	if dNaN {
		dst.Values[0] = src.Values[0] // only dst is non-numeric: take src
		return
	}
	if sNaN {
		return // only src is non-numeric: keep dst
	}
	var result float64
	switch strategy {
	case MergeStrategySum, MergeStrategyAvg, MergeStrategyCount:
		result = dv + sv
	case MergeStrategyMin:
		result = math.Min(dv, sv)
	case MergeStrategyMax:
		result = math.Max(dv, sv)
	default:
		result = sv
	}
	dst.Values[0] = strconv.FormatFloat(result, 'f', -1, 64)
}

// finalizeAvg divides the accumulated sum in p by count.
func finalizeAvg(p *Point, count int) {
	if len(p.Values) == 0 || count <= 1 {
		return
	}
	v := parseFloat(p.Values[0])
	if math.IsNaN(v) {
		return
	}
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
	return MergePointsWithOpts(p, p2, MergeOpts{SortPoints: sortPoints, Strategy: strategy})
}

// MergePointsWithOpts is the MergeOpts-aware merge primitive. Tolerance > 0 is
// only honored when opts.Strategy == MergeStrategyDedup (see
// sortAndAggregateTolerant for the rationale).
func MergePointsWithOpts(p, p2 Points, opts MergeOpts) Points {
	if opts.Strategy == MergeStrategyDedup {
		if p == nil && p2 == nil {
			return nil
		}
		if len(p) == 0 && len(p2) == 0 {
			return Points{}
		}
		finalize := func(out Points) Points {
			if opts.SortPoints && len(out) > 1 {
				out = sortAndDedupeTolerant(out, opts.ToleranceNanos)
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
	if p == nil && p2 == nil {
		return nil
	}
	if len(p) == 0 && len(p2) == 0 {
		return Points{}
	}
	// For count strategy, we need to initialize src values to 1 before merging
	if opts.Strategy == MergeStrategyCount {
		p = initCountValues(p)
		p2 = initCountValues(p2)
	}
	finalize := func(out Points) Points {
		if opts.SortPoints && len(out) > 1 {
			out = sortAndAggregateTolerant(out, opts.Strategy, opts.ToleranceNanos)
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
