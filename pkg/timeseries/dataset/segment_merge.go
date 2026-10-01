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
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

// The functions in this file never modify their inputs: a result shares their memory or is new.

// JoinSegments joins a series' sorted lists as they are when they follow one another and span at most
// maxSegments Segments, or else into one sorted Segment, the later list winning each epoch.
func JoinSegments(lists []Segments, maxSegments int) Segments {
	var all Segments
	ordered, started := true, false
	var last epoch.Epoch
	for _, l := range lists {
		first, ok := l.First()
		if !ok {
			continue
		}
		if started && first <= last {
			ordered = false
		}
		last, _ = l.Last()
		started = true
		for i := range l {
			if len(l[i].epochs) > 0 {
				all = append(all, l[i])
			}
		}
	}
	switch {
	case len(all) == 0:
		return nil
	case !ordered:
		// a stable sort keeps the lists' order among equal epochs, so the later list's row is last
		refs := all.refs()
		slices.SortStableFunc(refs, all.refCompare)
		k := 0
		for i := 1; i < len(refs); i++ {
			if all.epochOf(refs[i]) != all.epochOf(refs[k]) {
				k++
			}
			refs[k] = refs[i]
		}
		return all.gather(refs[:k+1])
	case len(all) > maxSegments:
		return all.Compact()
	}
	return slices.Clip(all)
}

// MergeSegments merges b's rows into a's: joined, then, when opts.SortPoints is set, sorted by epoch and
// deduplicated or aggregated by opts.Strategy.
func MergeSegments(a, b Segments, opts MergeOpts) Segments {
	all := slices.Concat(a, b)
	n := all.Len()
	if n == 0 {
		return nil
	}
	countValues := opts.Strategy == merge.StrategyCount && all.NumCols() > 0
	sorting := opts.SortPoints && n > 1
	if !sorting && !countValues {
		return slices.Clip(all)
	}
	refs := all.refs()
	if sorting {
		if a.IsSorted() && b.IsSorted() {
			refs = all.mergeSorted(refs, a.Len())
		} else {
			slices.SortStableFunc(refs, all.refCompare)
		}
	}
	if opts.Strategy == merge.StrategyDedup {
		if sorting {
			refs = all.dedupeTolerant(refs, opts.ToleranceNanos)
		}
		return all.gather(refs)
	}
	return all.aggregate(refs, opts, countValues, sorting)
}

// MergeSegmentParts merges next into cs as a sorting MergeSegments would, keeping cs's longest Segment
// in place when every other row falls outside it; without sortPoints, next is joined after cs.
func MergeSegmentParts(cs, next Segments, sortPoints bool) Segments {
	if !sortPoints {
		switch {
		case next.Len() == 0:
			return cs
		case cs.Len() == 0:
			return next
		}
		return slices.Concat(cs, next)
	}
	core := -1
	for i := range cs {
		if len(cs[i].epochs) > 0 && (core < 0 || len(cs[i].epochs) > len(cs[core].epochs)) {
			core = i
		}
	}
	if core >= 0 && strictlyIncreasingEpochs(cs[core].epochs) {
		e := cs[core].epochs
		lo, hi := e[0], e[len(e)-1]
		others := slices.Concat(cs[:core], cs[core+1:], next)
		if others.outside(lo, hi) {
			// the others sort and dedupe among themselves exactly as they would with the core among them
			refs := others.refs()
			slices.SortStableFunc(refs, others.refCompare)
			sorted := others.gather(others.dedupeTolerant(refs, 0))
			if len(sorted) == 0 {
				return Segments{cs[core]}
			}
			s := sorted[0]
			k, _ := slices.BinarySearch(s.epochs, lo)
			out := make(Segments, 0, 3)
			if k > 0 {
				out = append(out, s.slice(0, k))
			}
			out = append(out, cs[core])
			if k < len(s.epochs) {
				out = append(out, s.slice(k, len(s.epochs)))
			}
			return out
		}
	}
	return MergeSegments(cs, next, MergeOpts{SortPoints: true})
}

func strictlyIncreasingEpochs(e []epoch.Epoch) bool {
	for i := 1; i < len(e); i++ {
		if e[i] <= e[i-1] {
			return false
		}
	}
	return true
}

// whether every row falls before lo or after hi
func (s Segments) outside(lo, hi epoch.Epoch) bool {
	for i := range s {
		for _, e := range s[i].epochs {
			if e >= lo && e <= hi {
				return false
			}
		}
	}
	return true
}

func (s Segments) epochOf(r rowRef) epoch.Epoch {
	return s[r.seg].epochs[r.row]
}

func (s Segments) refCompare(a, b rowRef) int {
	return cmp.Compare(s.epochOf(a), s.epochOf(b))
}

// mergeSorted merges refs' two sorted runs, split at n, keeping the first run's rows first among
// equal epochs, as a stable sort would
func (s Segments) mergeSorted(refs []rowRef, n int) []rowRef {
	if n == 0 || n == len(refs) {
		return refs
	}
	out := make([]rowRef, 0, len(refs))
	i, j := 0, n
	for i < n && j < len(refs) {
		if s.epochOf(refs[j]) < s.epochOf(refs[i]) {
			out = append(out, refs[j])
			j++
			continue
		}
		out = append(out, refs[i])
		i++
	}
	out = append(out, refs[i:n]...)
	return append(out, refs[j:]...)
}

// dedupeTolerant drops duplicate rows of sorted refs: at a tolerance of 0, the last row of an epoch wins;
// above it, a row within the tolerance of the last kept row is dropped
func (s Segments) dedupeTolerant(refs []rowRef, toleranceNanos int64) []rowRef {
	if len(refs) == 0 {
		return refs
	}
	k := 0
	for i := 1; i < len(refs); i++ {
		d := int64(s.epochOf(refs[i]) - s.epochOf(refs[k]))
		switch {
		case toleranceNanos <= 0 && d == 0:
			refs[k] = refs[i]
		case toleranceNanos > 0 && d <= toleranceNanos:
		default:
			k++
			refs[k] = refs[i]
		}
	}
	return refs[:k+1]
}

// the value a row's first column holds for an aggregation: its own, a sum or other result not yet
// formatted, or a value from ValueMergeOperations
type aggValue struct {
	ref      rowRef
	computed bool
	f        float64
	value    any
	hasValue bool
}

// aggregate writes refs, sorted when sorting, reducing each epoch's rows to its first, whose first
// column becomes the strategy's result over the epoch's parseable values
func (s Segments) aggregate(refs []rowRef, opts MergeOpts, countValues, sorting bool) Segments {
	w := newSegmentWriter(len(refs), s.NumCols(), s)
	emit := func(first rowRef, v *aggValue) {
		seg := &s[first.seg]
		if seg.NumCols() == 0 {
			w.copyRun(seg, int(first.row), int(first.row)+1)
			return
		}
		cv := s.aggCell(v, countValues)
		w.copyRow(seg, int(first.row), &cv)
	}
	// rows alone at their epoch keep their values, so they're copied in runs
	single := -1
	for i := 0; i < len(refs); {
		j := i + 1
		if sorting {
			for j < len(refs) && s.epochOf(refs[j]) == s.epochOf(refs[i]) {
				j++
			}
		}
		if j == i+1 && !countValues {
			if single < 0 {
				single = i
			}
			i = j
			continue
		}
		if single >= 0 {
			w.copyRefs(s, refs[single:i])
			single = -1
		}
		v := aggValue{ref: refs[i]}
		for k := i + 1; k < j; k++ {
			s.aggregateInto(&v, refs[k], opts, countValues)
		}
		if count := j - i; opts.Strategy == merge.StrategyAvg && count > 1 {
			s.finalizeAvg(&v, count, opts.ValueOperations, countValues)
		}
		emit(refs[i], &v)
		i = j
	}
	if single >= 0 {
		w.copyRefs(s, refs[single:])
	}
	return Segments{w.finish()}
}

// the float a row's first column parses to, or NaN, as parseFloat reads a point's first value
func (s Segments) rawFloat(r rowRef, countValues bool) float64 {
	if countValues {
		return 1
	}
	return s[r.seg].floatAt(0, int(r.row))
}

// the value a row's first column holds, boxed, as a point's first value would be; the count strategy's
// values are never NaN, so never reach here
func (s Segments) rawValue(r rowRef) any {
	col := s[r.seg].Col(0)
	return col.Value(int(r.row))
}

// each row's first value under the count strategy, before rows are summed
const countValue = "1"

var countValueBytes = []byte(countValue)

// parseFloat returns a boxed value as a float: a float's own, a number parsed from a string, or NaN
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
	}
	return math.NaN()
}

func (s Segments) aggFloat(v *aggValue, countValues bool) float64 {
	switch {
	case v.computed:
		return v.f
	case v.hasValue:
		return parseFloat(v.value)
	}
	return s.rawFloat(v.ref, countValues)
}

func (s Segments) aggAny(v *aggValue) any {
	switch {
	case v.computed:
		return strconv.FormatFloat(v.f, 'f', -1, 64)
	case v.hasValue:
		return v.value
	}
	return s.rawValue(v.ref)
}

// aggregateInto folds src's first value into v, as aggregateValuesWithOperations folds a point
func (s Segments) aggregateInto(v *aggValue, src rowRef, opts MergeOpts, countValues bool) {
	if s[v.ref.seg].NumCols() == 0 || s[src.seg].NumCols() == 0 {
		return
	}
	dv, sv := s.aggFloat(v, countValues), s.rawFloat(src, countValues)
	dNaN, sNaN := math.IsNaN(dv), math.IsNaN(sv)
	switch {
	case dNaN && sNaN:
		if opts.ValueOperations != nil {
			if value, handled := opts.ValueOperations.MergeValues(s.aggAny(v),
				s.rawValue(src), opts.Strategy); handled {
				*v = aggValue{ref: v.ref, value: value, hasValue: true}
			}
		}
		return
	case dNaN:
		// only the kept value is non-numeric, so the row's own value replaces it
		*v = aggValue{ref: v.ref, value: s.rawValue(src), hasValue: true}
		return
	case sNaN:
		return
	}
	var result float64
	switch opts.Strategy {
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
	*v = aggValue{ref: v.ref, computed: true, f: result}
}

func (s Segments) finalizeAvg(v *aggValue, count int, ops ValueMergeOperations, countValues bool) {
	if s[v.ref.seg].NumCols() == 0 {
		return
	}
	f := s.aggFloat(v, countValues)
	if math.IsNaN(f) {
		if ops != nil {
			if value, handled := ops.DivideValue(s.aggAny(v), float64(count)); handled {
				*v = aggValue{ref: v.ref, value: value, hasValue: true}
			}
		}
		return
	}
	*v = aggValue{ref: v.ref, computed: true, f: f / float64(count)}
}

// aggCell returns the cell to write for v
func (s Segments) aggCell(v *aggValue, countValues bool) cellValue {
	switch {
	case v.computed:
		return floatTextCell(v.f)
	case v.hasValue:
		return valueCell(v.value)
	case countValues:
		return cellValue{kind: KindString, bytes: countValueBytes}
	}
	col := s[v.ref.seg].Col(0)
	i := int(v.ref.row)
	k := col.KindAt(i)
	switch {
	case k.IsBytes():
		return cellValue{kind: k, bytes: col.Bytes(i)}
	case k == KindExt:
		return cellValue{kind: k, ext: col.Ext(i)}
	}
	return cellValue{kind: k, val: col.vals[i]}
}
