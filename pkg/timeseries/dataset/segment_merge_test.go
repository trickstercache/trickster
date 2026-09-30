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
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

var testStrategies = []merge.Strategy{
	merge.StrategyDedup, merge.StrategySum, merge.StrategyAvg, merge.StrategyMin,
	merge.StrategyMax, merge.StrategyCount, merge.StrategyScalar,
}

func TestMergeSegmentsMatchesMergePoints(t *testing.T) {
	rng := weaktest.NewRand(1, 2)
	for iter := range 3000 {
		cols := 1 + rng.IntN(3)
		profiles := randProfiles(rng, cols)
		if rng.IntN(2) == 0 {
			// the aggregations read the first column, so most runs give it numbers
			profiles[0] = []int{profileFloat, profileNumericText}[rng.IntN(2)]
		}
		sortedInputs := rng.IntN(3) > 0
		a := randPoints(rng, rng.IntN(12), cols, 1+rng.IntN(10), sortedInputs, profiles)
		b := randPoints(rng, rng.IntN(12), cols, 1+rng.IntN(10), sortedInputs, profiles)
		opts := MergeOpts{
			SortPoints: rng.IntN(4) > 0,
			Strategy:   testStrategies[rng.IntN(len(testStrategies))],
		}
		if rng.IntN(3) == 0 {
			opts.ToleranceNanos = int64(rng.IntN(25))
		}
		if rng.IntN(2) == 0 {
			opts.ValueOperations = testValueOperations{}
		}
		sa := segmentsOf(a, cols, rng.IntN(len(a)+1))
		sb := segmentsOf(b, cols, rng.IntN(len(b)+1))
		want := legacyMergePointsWithOpts(a, b, opts)
		got := MergeSegments(sa, sb, opts)
		context := fmt.Sprintf("iteration %d, opts %+v\na %s\nb %s", iter, opts, describePoints(a),
			describePoints(b))
		requireSamePoints(t, want, pointsOf(got), context)
		// the inputs are never modified
		requireSamePoints(t, a, pointsOf(sa), context+" (a after)")
		requireSamePoints(t, b, pointsOf(sb), context+" (b after)")
	}
}

func TestMergeSegmentsEdges(t *testing.T) {
	if got := MergeSegments(nil, nil, MergeOpts{SortPoints: true}); got != nil {
		t.Errorf("merging nothing gave %v", got)
	}
	a := segmentsOf(Points{{Epoch: 1, Values: []any{"2"}}}, 1)
	if got := MergeSegments(a, nil, MergeOpts{SortPoints: true}); got.Len() != 1 {
		t.Errorf("merging one row gave %d rows", got.Len())
	}
	// a merge of rows without columns has nothing to aggregate
	empty := Points{{Epoch: 1}, {Epoch: 1}, {Epoch: 2}}
	got := MergeSegments(segmentsOf(empty, 0), nil, MergeOpts{SortPoints: true, Strategy: merge.StrategySum})
	if got.Len() != 2 {
		t.Errorf("summing rows without columns gave %d rows", got.Len())
	}
	got = MergeSegments(segmentsOf(empty, 0), nil, MergeOpts{Strategy: merge.StrategyCount})
	if got.Len() != 3 {
		t.Errorf("counting rows without columns gave %d rows", got.Len())
	}
	got = MergeSegments(segmentsOf(empty, 0), nil, MergeOpts{SortPoints: true, Strategy: merge.StrategyAvg})
	if got.Len() != 2 {
		t.Errorf("averaging rows without columns gave %d rows", got.Len())
	}
}

func TestMergeSegmentsFixedCases(t *testing.T) {
	cases := []struct {
		name string
		pts  Points
		opts MergeOpts
	}{
		// a sum of infinities is NaN, which then meets a value only ValueOperations can merge
		{
			"infinities",
			Points{
				{Epoch: 1, Values: []any{"+Inf"}},
				{Epoch: 1, Values: []any{"-Inf"}},
				{Epoch: 1, Values: []any{"h"}},
			},
			MergeOpts{SortPoints: true, Strategy: merge.StrategySum, ValueOperations: testValueOperations{}},
		},
		{
			"infinities averaged",
			Points{{Epoch: 1, Values: []any{"+Inf"}}, {Epoch: 1, Values: []any{"-Inf"}}},
			MergeOpts{SortPoints: true, Strategy: merge.StrategyAvg, ValueOperations: testValueOperations{}},
		},
		{
			"unknown strategy",
			Points{{Epoch: 1, Values: []any{"1"}}, {Epoch: 1, Values: []any{"2"}}},
			MergeOpts{SortPoints: true, Strategy: merge.Strategy(99)},
		},
	}
	for _, tc := range cases {
		want := legacyMergePointsWithOpts(tc.pts, nil, tc.opts)
		got := MergeSegments(segmentsOf(tc.pts, 1), nil, tc.opts)
		requireSamePoints(t, want, pointsOf(got), tc.name)
	}
}

func TestJoinSegmentsMatchesJoinPoints(t *testing.T) {
	rng := weaktest.NewRand(3, 4)
	for iter := range 2000 {
		cols := 1 + rng.IntN(2)
		profiles := randProfiles(rng, cols)
		lists := make([]Points, rng.IntN(4))
		segs := make([]Segments, len(lists))
		start := 0
		overlap := rng.IntN(2) == 0
		for i := range lists {
			lists[i] = randPoints(rng, rng.IntN(8), cols, 1+rng.IntN(8), true, profiles)
			if !overlap {
				// lists that follow one another in time
				for j := range lists[i] {
					lists[i][j].Epoch += epoch.Epoch(start)
				}
				start += 1000
			}
			segs[i] = segmentsOf(lists[i], cols, rng.IntN(len(lists[i])+1))
		}
		var nonEmpty []Points
		for _, l := range lists {
			if len(l) > 0 {
				nonEmpty = append(nonEmpty, l)
			}
		}
		var want Points
		if len(nonEmpty) > 0 {
			want = legacyJoinPoints(nonEmpty)
		}
		context := fmt.Sprintf("iteration %d, lists %d, overlap %t", iter, len(lists), overlap)
		for _, maxSegments := range []int{math.MaxInt, 3, 1} {
			got := JoinSegments(segs, maxSegments)
			requireSamePoints(t, want, pointsOf(got), context)
			if len(got) > maxSegments {
				t.Fatalf("%s: a join limited to %d segments gave %d", context, maxSegments, len(got))
			}
		}
	}
}

func TestMergeSegmentPartsMatchesMergeSeriesParts(t *testing.T) {
	rng := weaktest.NewRand(5, 6)
	for iter := range 2000 {
		cols := 1 + rng.IntN(2)
		profiles := randProfiles(rng, cols)
		base := randPoints(rng, rng.IntN(10), cols, 20, true, profiles)
		base = legacySortAndDedupeTolerant(base, 0)
		cs := &legacyParts{points: base.Clone()}
		segs := Segments{segmentOf(base, cols)}
		// merge a few rounds, as partial buckets and fast forward add rows to a response
		for round := range 3 {
			span, offset := 1+rng.IntN(6), rng.IntN(30)-5
			next := randPoints(rng, rng.IntN(4), cols, span, rng.IntN(3) > 0, profiles)
			for j := range next {
				next[j].Epoch += epoch.Epoch(offset * 10)
			}
			sortPoints := rng.IntN(5) > 0
			legacyMergeSeriesParts(cs, next.Clone(), sortPoints)
			segs = MergeSegmentParts(segs, Segments{segmentOf(next, cols)}, sortPoints)
			context := fmt.Sprintf("iteration %d round %d sort %t", iter, round, sortPoints)
			requireSamePoints(t, cs.flat(), pointsOf(segs), context)
		}
	}
}

func TestSortedSegments(t *testing.T) {
	rng := weaktest.NewRand(7, 8)
	for range 500 {
		cols := 1 + rng.IntN(3)
		pts := randPoints(rng, rng.IntN(20), cols, 8, false, randProfiles(rng, cols))
		s := segmentsOf(pts, cols, rng.IntN(len(pts)+1))
		want := pts.Clone()
		slices.SortStableFunc(want, legacyPointCmp)
		got := s.Sorted()
		requireSamePoints(t, want, pointsOf(got), "sorted")
		if !got.IsSorted() {
			t.Fatal("Sorted gave unsorted rows")
		}
		if s.IsSorted() && len(got) != len(s) {
			t.Fatal("Sorted copied rows already in order")
		}
	}
}
