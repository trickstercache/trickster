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

// This file keeps the point-by-point implementations that DataSets used before their rows were held
// by column, as oracles: the tests compare the column code with them over random data.

import (
	"cmp"
	"math"
	"slices"
	"sort"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

func legacyPointCmp(a, b Point) int {
	return cmp.Compare(a.Epoch, b.Epoch)
}

func legacySortAndDedupeTolerant(p Points, toleranceNanos int64) Points {
	if len(p) == 0 {
		return p
	}
	slices.SortStableFunc(p, legacyPointCmp)
	var k int
	for i := 1; i < len(p); i++ {
		if toleranceNanos <= 0 {
			if p[k].Epoch == p[i].Epoch {
				p[k] = p[i]
			} else {
				k++
				p[k] = p[i]
			}
			continue
		}
		if int64(p[i].Epoch-p[k].Epoch) <= toleranceNanos {
			continue
		}
		k++
		p[k] = p[i]
	}
	return p[:k+1]
}

func legacySortAndAggregate(p Points, strategy merge.Strategy, toleranceNanos int64,
	ops ValueMergeOperations,
) Points {
	if strategy == merge.StrategyDedup {
		return legacySortAndDedupeTolerant(p, toleranceNanos)
	}
	if len(p) <= 1 {
		return p
	}
	slices.SortStableFunc(p, legacyPointCmp)
	var k int
	count := 1
	for i := 1; i < len(p); i++ {
		if p[k].Epoch == p[i].Epoch {
			legacyAggregateValues(&p[k], &p[i], strategy, ops)
			count++
			continue
		}
		if strategy == merge.StrategyAvg && count > 1 {
			legacyFinalizeAvg(&p[k], count, ops)
		}
		count = 1
		k++
		p[k] = p[i]
	}
	if strategy == merge.StrategyAvg && count > 1 {
		legacyFinalizeAvg(&p[k], count, ops)
	}
	return p[:k+1]
}

func legacyAggregateValues(dst, src *Point, strategy merge.Strategy, ops ValueMergeOperations) {
	if len(dst.Values) == 0 || len(src.Values) == 0 {
		return
	}
	dv, sv := parseFloat(dst.Values[0]), parseFloat(src.Values[0])
	dNaN, sNaN := math.IsNaN(dv), math.IsNaN(sv)
	switch {
	case dNaN && sNaN:
		if ops != nil {
			if value, handled := ops.MergeValues(dst.Values[0], src.Values[0], strategy); handled {
				dst.Values[0] = value
			}
		}
		return
	case dNaN:
		dst.Values[0] = src.Values[0]
		return
	case sNaN:
		return
	}
	var result float64
	switch strategy {
	case merge.StrategySum, merge.StrategyAvg, merge.StrategyCount:
		result = dv + sv
	case merge.StrategyMin:
		result = math.Min(dv, sv)
	case merge.StrategyMax:
		result = math.Max(dv, sv)
	case merge.StrategyScalar:
		result = dv
	default:
		result = sv
	}
	dst.Values[0] = strconv.FormatFloat(result, 'f', -1, 64)
}

func legacyFinalizeAvg(p *Point, count int, ops ValueMergeOperations) {
	if len(p.Values) == 0 || count <= 1 {
		return
	}
	v := parseFloat(p.Values[0])
	if math.IsNaN(v) {
		if ops != nil {
			if value, handled := ops.DivideValue(p.Values[0], float64(count)); handled {
				p.Values[0] = value
			}
		}
		return
	}
	p.Values[0] = strconv.FormatFloat(v/float64(count), 'f', -1, 64)
}

// legacyMergePointsWithOpts is MergePointsWithOpts; its inputs' values are cloned first, as the
// original wrote through its inputs' values
func legacyMergePointsWithOpts(p, p2 Points, opts MergeOpts) Points {
	if p == nil && p2 == nil {
		return nil
	}
	if len(p) == 0 && len(p2) == 0 {
		return Points{}
	}
	p, p2 = p.Clone(), p2.Clone()
	if opts.Strategy == merge.StrategyCount {
		for _, list := range []Points{p, p2} {
			for i := range list {
				if len(list[i].Values) > 0 {
					list[i].Values[0] = countValue
				}
			}
		}
	}
	out := slices.Concat(p, p2)
	if opts.SortPoints && len(out) > 1 {
		out = legacySortAndAggregate(out, opts.Strategy, opts.ToleranceNanos, opts.ValueOperations)
	}
	return out
}

func legacyListsOrdered(lists []Points) bool {
	for i := 1; i < len(lists); i++ {
		if prev := lists[i-1]; len(prev) > 0 && len(lists[i]) > 0 && lists[i][0].Epoch <= prev[len(prev)-1].Epoch {
			return false
		}
	}
	return true
}

func legacyJoinPoints(lists []Points) Points {
	out := slices.Concat(lists...)
	if legacyListsOrdered(lists) {
		return out
	}
	slices.SortStableFunc(out, legacyPointCmp)
	k := 0
	for i := 1; i < len(out); i++ {
		if out[i].Epoch != out[k].Epoch {
			k++
		}
		out[k] = out[i]
	}
	return out[:k+1]
}

// legacyParts is a series as it was held before Segments: its own points, and parts before and after
type legacyParts struct {
	head, points, tail Points
}

func (s *legacyParts) flat() Points {
	return slices.Concat(s.head, s.points, s.tail)
}

func legacyMergeSeriesParts(cs *legacyParts, pts Points, sortPoints bool) {
	if !sortPoints {
		switch {
		case len(pts) == 0:
		case len(cs.flat()) == 0:
			cs.points, cs.head, cs.tail = pts.Clone(), nil, nil
		default:
			cs.tail = slices.Concat(cs.tail, pts)
		}
		return
	}
	if len(cs.points) > 0 && strictlyIncreasingPoints(cs.points) {
		lo, hi := cs.points[0].Epoch, cs.points[len(cs.points)-1].Epoch
		others := slices.Concat(cs.head, cs.tail, pts)
		outside := true
		for i := range others {
			if others[i].Epoch >= lo && others[i].Epoch <= hi {
				outside = false
				break
			}
		}
		if outside {
			others = legacySortAndDedupeTolerant(others, 0)
			k, _ := slices.BinarySearchFunc(others, lo, func(p Point, e epoch.Epoch) int {
				return cmp.Compare(p.Epoch, e)
			})
			cs.head, cs.tail = slices.Clip(others[:k]), others[k:]
			return
		}
	}
	cs.points = legacyMergePointsWithOpts(cs.flat(), pts, MergeOpts{SortPoints: true})
	cs.head, cs.tail = nil, nil
}

func strictlyIncreasingPoints(p Points) bool {
	for i := 1; i < len(p); i++ {
		if p[i].Epoch <= p[i-1].Epoch {
			return false
		}
	}
	return true
}

func legacyPointsWithin(pts Points, start, end epoch.Epoch) (int, int) {
	from := sort.Search(len(pts), func(i int) bool { return pts[i].Epoch >= start })
	to := from + sort.Search(len(pts)-from, func(i int) bool { return pts[from+i].Epoch > end })
	return from, to
}

// legacyRow is a row as Result.Rows yielded it before Segments
type legacyRow struct {
	series int
	point  *Point
}

// legacyRows orders the lists' rows as Result.Rows did: by epoch, then by list, then in list order
// (reversed with the epochs when descending), each epoch's rows stable-sorted by compare when set
func legacyRows(lists []Points, descending bool, compare func(a, b legacyRow) int) []legacyRow {
	type ranked struct {
		legacyRow
		index int
	}
	var all []ranked
	for s, pts := range lists {
		for i := range pts {
			all = append(all, ranked{legacyRow{s, &pts[i]}, i})
		}
	}
	slices.SortStableFunc(all, func(a, b ranked) int {
		if c := cmp.Compare(a.point.Epoch, b.point.Epoch); c != 0 {
			if descending {
				return -c
			}
			return c
		}
		if c := cmp.Compare(a.series, b.series); c != 0 {
			return c
		}
		if descending {
			return cmp.Compare(b.index, a.index)
		}
		return cmp.Compare(a.index, b.index)
	})
	out := make([]legacyRow, len(all))
	for i := range all {
		out[i] = all[i].legacyRow
	}
	if compare == nil {
		return out
	}
	for i := 0; i < len(out); {
		j := i + 1
		for j < len(out) && out[j].point.Epoch == out[i].point.Epoch {
			j++
		}
		slices.SortStableFunc(out[i:j], compare)
		i = j
	}
	return out
}

// legacyNewestEpoch is newestEpoch: the nth newest distinct epoch, and whether an older one exists
func legacyNewestEpoch(lists []Points, n int) (epoch.Epoch, bool) {
	var all []epoch.Epoch
	for _, pts := range lists {
		for i := range pts {
			all = append(all, pts[i].Epoch)
		}
	}
	if len(all) == 0 {
		return 0, false
	}
	slices.Sort(all)
	all = slices.Compact(all)
	slices.Reverse(all)
	if n == 0 {
		return 0, true
	}
	if len(all) <= n {
		return 0, false
	}
	return all[n-1], true
}

// legacyBuild is the Builder's point-by-point duplicate handling before Segments
type legacyBuild struct {
	policy DuplicatePolicy
	series []legacyBuildSeries
}

type legacyBuildSeries struct {
	pts       Points
	unordered bool
}

func (b *legacyBuild) append(s int, p Point) error {
	ls := &b.series[s]
	if n := len(ls.pts); n > 0 && !ls.unordered {
		last := &ls.pts[n-1]
		switch {
		case p.Epoch < last.Epoch:
			ls.unordered = true
		case p.Epoch == last.Epoch:
			switch b.policy {
			case DuplicatesError:
				return ErrDuplicateEpoch
			case DuplicatesFirstWins:
				return nil
			case DuplicatesLastWins:
				last.Values = p.Values
				return nil
			}
		}
	}
	ls.pts = append(ls.pts, p)
	return nil
}

func (b *legacyBuild) finish() error {
	for s := range b.series {
		ls := &b.series[s]
		if !ls.unordered {
			continue
		}
		slices.SortStableFunc(ls.pts, legacyPointCmp)
		if b.policy == DuplicatesKeep {
			continue
		}
		pts, k := ls.pts, 0
		for i := 1; i < len(pts); i++ {
			if pts[i].Epoch != pts[k].Epoch {
				k++
				pts[k] = pts[i]
				continue
			}
			switch b.policy {
			case DuplicatesError:
				return ErrDuplicateEpoch
			case DuplicatesLastWins:
				pts[k] = pts[i]
			}
		}
		if len(pts) > 0 {
			ls.pts = pts[:k+1]
		}
	}
	return nil
}
