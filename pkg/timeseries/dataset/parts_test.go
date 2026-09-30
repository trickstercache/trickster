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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

// a description of everything a merge result holds that a reader could see
func mergedState(ds *DataSet) string {
	out := fmt.Sprintf("%v %v %q %q %q %v|", ds.ExtentList, ds.VolatileExtentList, ds.Status, ds.ErrorType,
		ds.Error, ds.Warnings)
	for _, r := range ds.Results {
		if r == nil {
			out += "nil;"
			continue
		}
		out += fmt.Sprintf("r%d %q:", r.StatementID, r.Name)
		for _, s := range r.SeriesList {
			if s == nil {
				out += "nil,"
				continue
			}
			out += fmt.Sprintf("%s %d[", s.Header.Name, s.PointCount())
			for _, p := range s.Points() {
				out += fmt.Sprintf("%d:%v ", p.Epoch, p.Values)
			}
			out += "],"
		}
	}
	return out
}

func randomMergeSet(rng *weaktest.Rand, names []string, lo, hi int, sorted bool) *DataSet {
	ds := &DataSet{TimeRangeQuery: &timeseries.TimeRangeQuery{Step: 1}}
	for r := range 1 + rng.IntN(2) {
		res := &Result{StatementID: r}
		for range rng.IntN(4) {
			s := NewSeries(SeriesHeader{Name: names[rng.IntN(len(names))]}, nil)
			at := lo
			for range rng.IntN(6) {
				if sorted {
					at += 1 + rng.IntN(2)
					if at > hi {
						break
					}
				} else {
					at = lo + rng.IntN(hi-lo+1)
				}
				s.SetPoints(append(s.Points(), Point{Epoch: epoch.Epoch(at), Values: []any{rng.IntN(100)}}))
			}
			res.SeriesList = append(res.SeriesList, s)
		}
		ds.Results = append(ds.Results, res)
	}
	if rng.IntN(3) == 0 {
		ds.Warnings, ds.Status = []string{fmt.Sprint("w", rng.IntN(9))}, []string{"success", "error"}[rng.IntN(2)]
	}
	ds.ExtentList = timeseries.ExtentList{{Start: epochTime(lo), End: epochTime(hi)}}
	return ds
}

func epochTime(n int) time.Time { return time.Unix(0, int64(n)) }

func TestMergePartsMatchesMerge(t *testing.T) {
	rng := weaktest.NewRand(31, 7)
	names := []string{"a", "b", "c"}
	var withParts int
	for trial := range 2000 {
		cached := randomMergeSet(rng, names, 100, 200, trial%5 != 0)
		before := mergedState(cached)
		want := cached.Clone().(*DataSet)
		got := cached.FullView()
		for range 1 + rng.IntN(2) {
			sortPoints := rng.IntN(2) == 0
			var parts []timeseries.Timeseries
			for range 1 + rng.IntN(2) {
				switch rng.IntN(3) {
				case 0: // a start bucket
					parts = append(parts, randomMergeSet(rng, names, 90, 99, true))
				case 1: // an end bucket or live point
					parts = append(parts, randomMergeSet(rng, names, 201, 210, true))
				default: // anywhere, which a merge must still get right
					parts = append(parts, randomMergeSet(rng, names, 95, 205, false))
				}
			}
			wantParts := make([]timeseries.Timeseries, len(parts))
			for i, p := range parts {
				// a clone leaves out the envelope, which the merge reads
				c := p.Clone().(*DataSet)
				c.Status, c.Warnings = p.(*DataSet).Status, slices.Clone(p.(*DataSet).Warnings)
				wantParts[i] = c
			}
			want.Merge(sortPoints, wantParts...)
			got.MergeParts(sortPoints, parts...)
		}
		if w, g := mergedState(want), mergedState(got.Flat()); w != g {
			t.Fatalf("trial %d:\nMerge      %s\nMergeParts %s", trial, w, g)
		}
		if w, g := mergedState(want), mergedState(got); w != g {
			t.Fatalf("trial %d, read through its parts:\nMerge      %s\nMergeParts %s", trial, w, g)
		}
		if got.HasParts() {
			withParts++
		}
		if mergedState(cached) != before {
			t.Fatalf("trial %d: the cached dataset changed", trial)
		}
		// a clone folds the parts (and, as ever, leaves out the envelope), and rows read through them
		series := func(state string) string { return state[strings.Index(state, "|"):] }
		if w, g := series(mergedState(want)), series(mergedState(got.Clone().(*DataSet))); w != g {
			t.Fatalf("trial %d: clone %s", trial, g)
		}
		for i, r := range got.Results {
			var rows, flat []string
			for row := range r.Rows(RowOrder{}) {
				rows = append(rows, fmt.Sprint(row.SeriesIndex, row.Epoch()))
			}
			for row := range got.Flat().Results[i].Rows(RowOrder{}) {
				flat = append(flat, fmt.Sprint(row.SeriesIndex, row.Epoch()))
			}
			if !slices.Equal(rows, flat) {
				t.Fatalf("trial %d: rows %v, flattened %v", trial, rows, flat)
			}
		}
	}
	if withParts < 500 {
		t.Fatalf("only %d of the trials kept parts", withParts)
	}
}

func TestMergeDisjointPartsMatchesMergeDisjoint(t *testing.T) {
	rng := weaktest.NewRand(12, 12)
	names := []string{"a", "b", "c"}
	var withParts int
	for trial := range 1000 {
		interior := randomMergeSet(rng, names, 100, 200, true)
		var sets []*DataSet
		switch rng.IntN(3) {
		case 0: // start and end buckets
			sets = []*DataSet{randomMergeSet(rng, names, 90, 99, true), interior,
				randomMergeSet(rng, names, 201, 210, true)}
		case 1: // an end bucket only
			sets = []*DataSet{interior, randomMergeSet(rng, names, 201, 210, true)}
		default: // a part that overlaps, which must still join as MergeDisjoint joins it
			sets = []*DataSet{randomMergeSet(rng, names, 90, 150, true), interior, nil}
		}
		want := MergeDisjointStep(1, sets...)
		got := MergeDisjointParts(1, sets...)
		if got.HasParts() {
			withParts++
		}
		if w, g := mergedState(want), mergedState(got.Flat()); w != g {
			t.Fatalf("trial %d:\nMergeDisjoint      %s\nMergeDisjointParts %s", trial, w, g)
		}
		if w, g := mergedState(want), mergedState(got); w != g {
			t.Fatalf("trial %d, read through its parts:\nwant %s\n got %s", trial, w, g)
		}
		for i, r := range got.Results {
			var rows, flat []string
			for _, order := range []RowOrder{{}, {Descending: true}} {
				for row := range r.Rows(order) {
					rows = append(rows, fmt.Sprint(row.SeriesIndex, row.Epoch(), rowValues(row)))
				}
				for row := range want.Results[i].Rows(order) {
					flat = append(flat, fmt.Sprint(row.SeriesIndex, row.Epoch(), rowValues(row)))
				}
			}
			if !slices.Equal(rows, flat) {
				t.Fatalf("trial %d: rows %v, want %v", trial, rows, flat)
			}
		}
	}
	if withParts < 300 {
		t.Fatalf("only %d of the trials kept parts", withParts)
	}
}

func BenchmarkResponseWithPartialBuckets(b *testing.B) {
	// a response view of a 100 x 1000 entry, with a start bucket and an end bucket merged in, as a caller
	// with partial buckets assembles it
	const series, points = 100, 1000
	bucket := func(at int) *DataSet {
		r := &Result{}
		for i := range series {
			r.SeriesList = append(r.SeriesList, NewSeries(SeriesHeader{Name: fmt.Sprint("s", i)}, Points{{Epoch: epoch.Epoch(at), Values: []any{1.0}}}))
		}
		return &DataSet{Results: Results{r}}
	}
	cached := bucket(0)
	for _, s := range cached.Results[0].SeriesList {
		pts := make(Points, points)
		for j := range pts {
			pts[j] = Point{Epoch: epoch.Epoch(j + 1), Values: []any{float64(j)}}
		}
		s.SetPoints(pts)
	}
	for name, merge := range map[string]func(*DataSet, bool, ...timeseries.Timeseries){
		"merge": (*DataSet).Merge, "parts": (*DataSet).MergeParts,
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				view := cached.FullView()
				merge(view, true, bucket(0), bucket(points+1))
			}
		})
	}
}
