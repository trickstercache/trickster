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
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

func TestSegmentsView(t *testing.T) {
	rng := weaktest.NewRand(11, 12)
	for iter := range 1000 {
		cols := 1 + rng.IntN(2)
		pts := randPoints(rng, rng.IntN(30), cols, 20, true, randProfiles(rng, cols))
		s := segmentsOf(pts, cols, rng.IntN(len(pts)+1), rng.IntN(len(pts)+1))
		start, end := epoch.Epoch(rng.IntN(220)-10), epoch.Epoch(rng.IntN(220)-10)
		from, to := legacyPointsWithin(pts, start, end)
		var want Points
		if from < to {
			want = pts[from:to]
		}
		v := s.View(start, end)
		context := fmt.Sprintf("iteration %d [%d, %d]", iter, start, end)
		requireSamePoints(t, want, pointsOf(v), context)
		// a view of everything is the list itself
		if all := s.View(-1, 1<<40); len(pts) > 0 && &all[0] != &s[0] {
			t.Fatalf("%s: a view of every row copied the list", context)
		}
		requireSamePoints(t, want, pointsOf(v.Compact()), context+" compact")
		requireSamePoints(t, want, pointsOf(v.Clone()), context+" clone")
		if len(v.Compact()) > 1 {
			t.Fatalf("%s: Compact gave %d segments", context, len(v.Compact()))
		}
		first, ok1 := v.First()
		last, ok2 := v.Last()
		if ok1 != (len(want) > 0) || ok2 != ok1 || (ok1 && (first != want[0].Epoch || last != want[len(want)-1].Epoch)) {
			t.Fatalf("%s: First %d %t Last %d %t", context, first, ok1, last, ok2)
		}
		if v.Len() > 0 && (v.NumCols() != cols || v.Size() <= 0) {
			t.Fatalf("%s: NumCols %d Size %d", context, v.NumCols(), v.Size())
		}
	}
}

func TestSegmentsCompactShares(t *testing.T) {
	one := Segments{segmentOf(Points{{Epoch: 1, Values: []any{1.0}}}, 1)}
	if got := one.Compact(); &got[0] != &one[0] {
		t.Error("compacting one segment copied it")
	}
	withEmpty := append(Segments{{}}, one...)
	if got := withEmpty.Compact(); len(got) != 1 || got[0].Len() != 1 {
		t.Errorf("compacting around an empty segment gave %d", len(got))
	}
	if got := (Segments{{}}).Compact(); got != nil {
		t.Errorf("compacting nothing gave %v", got)
	}
	if got := Segments(nil).Clone(); got != nil {
		t.Errorf("cloning nothing gave %v", got)
	}
	if (Segments{{}}).NumCols() != 0 || !(Segments{{}}).IsSorted() {
		t.Error("an empty segment has columns or isn't sorted")
	}
	unsorted := Segments{segmentOf(Points{{Epoch: 5}, {Epoch: 6}}, 0), segmentOf(Points{{Epoch: 1}}, 0)}
	if unsorted.IsSorted() {
		t.Error("segments out of order reported sorted")
	}
}

func TestSegmentRowsMatchesLegacyRows(t *testing.T) {
	rng := weaktest.NewRand(13, 14)
	for iter := range 2000 {
		n := rng.IntN(5)
		if iter%4 == 0 {
			// the merge's tree takes any number of lists, not just powers of two
			n = rng.IntN(40)
		}
		lists := make([]Points, n)
		segs := make([]Segments, n)
		r := &Result{}
		for i := range lists {
			lists[i] = randPoints(rng, rng.IntN(10), 1, 12, true, []int{profileFloat})
			segs[i] = segmentsOf(lists[i], 1, rng.IntN(len(lists[i])+1))
			if len(lists[i]) == 0 && rng.IntN(2) == 0 {
				segs[i] = nil
			}
			r.SeriesList = append(r.SeriesList, NewSeriesOf(SeriesHeader{}, segs[i]))
		}
		descending := rng.IntN(2) == 0
		order := RowOrder{Descending: descending}
		segOrder := SegmentRowOrder{Descending: descending}
		var legacyCompare func(a, b legacyRow) int
		if rng.IntN(2) == 0 {
			// newest value first within an epoch, which differs from series order
			legacyCompare = func(a, b legacyRow) int {
				return -cmp.Compare(fmt.Sprint(a.point.Values[0]), fmt.Sprint(b.point.Values[0]))
			}
			order.Compare = func(a, b Row) int {
				return -cmp.Compare(fmt.Sprint(a.Value(0)), fmt.Sprint(b.Value(0)))
			}
			segOrder.Compare = func(a, b SegmentRow) int {
				return -cmp.Compare(fmt.Sprint(a.Seg.Value(0, a.Index)), fmt.Sprint(b.Seg.Value(0, b.Index)))
			}
		}
		var want []string
		for _, row := range legacyRows(lists, descending, legacyCompare) {
			want = append(want, fmt.Sprint(row.series, row.point.Epoch, row.point.Values))
		}
		var got, gotResult []string
		for row := range SegmentRows(segs, segOrder) {
			got = append(got, fmt.Sprint(row.List, row.Epoch(), []any{row.Seg.Value(0, row.Index)}))
		}
		for row := range r.Rows(order) {
			gotResult = append(gotResult, fmt.Sprint(row.SeriesIndex, row.Epoch(), []any{row.Value(0)}))
		}
		if !reflect.DeepEqual(want, got) || !reflect.DeepEqual(want, gotResult) {
			t.Fatalf("iteration %d descending %t:\nwant %v\ngot  %v\nrows %v", iter, descending, want, got, gotResult)
		}
		// a consumer can stop early
		for range SegmentRows(segs, segOrder) {
			break
		}
		for range r.Rows(order) {
			break
		}
	}
}

func TestNewestEpochMatches(t *testing.T) {
	rng := weaktest.NewRand(15, 16)
	for iter := range 2000 {
		n := rng.IntN(5)
		if iter%4 == 0 {
			// the merge's tree takes any number of lists, not just powers of two
			n = rng.IntN(40)
		}
		lists := make([]Points, n)
		segs := make([]Segments, n)
		for i := range n {
			lists[i] = randPoints(rng, rng.IntN(10), 0, 15, true, nil)
			segs[i] = segmentsOf(lists[i], 0, rng.IntN(len(lists[i])+1))
		}
		k := rng.IntN(12)
		wantAt, wantOK := legacyNewestEpoch(lists, k)
		gotAt, gotOK := NewestEpoch(segs, k)
		if wantAt != gotAt || wantOK != gotOK {
			t.Fatalf("iteration %d n %d: got %d %t, want %d %t", iter, k, gotAt, gotOK, wantAt, wantOK)
		}
	}
}

func TestSegmentRowsExtremeEpochs(t *testing.T) {
	// a finished list never outranks a live row, even one at the edge of the epoch range
	for _, descending := range []bool{false, true} {
		edge := epoch.Epoch(math.MaxInt64)
		if descending {
			edge = math.MinInt64
		}
		lists := []Segments{
			segmentsOf(Points{{Epoch: 1, Values: []any{"a"}}}, 1),
			segmentsOf(Points{{Epoch: edge, Values: []any{"b"}}}, 1),
			segmentsOf(Points{{Epoch: 2, Values: []any{"c"}}}, 1),
		}
		var got []string
		for row := range SegmentRows(lists, SegmentRowOrder{Descending: descending}) {
			got = append(got, row.Seg.Text(0, row.Index))
		}
		want := []string{"a", "c", "b"}
		if descending {
			want = []string{"c", "a", "b"}
		}
		require.Equal(t, want, got)
	}
}
