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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

const viewStep = time.Minute

func viewSet(series map[string][]int) *DataSet {
	// one result whose series, named by host, hold a point at each listed minute, valued by it
	ds := &DataSet{TimeRangeQuery: &timeseries.TimeRangeQuery{Step: viewStep}, Results: Results{{}}}
	lo, hi := -1, -1
	for _, host := range sortedKeys(series) {
		s := &Series{Header: SeriesHeader{Name: "m", Tags: Tags{"host": host}}}
		for _, minute := range series[host] {
			s.Points = append(s.Points, Point{
				Epoch: epoch.Epoch(time.Duration(minute) * viewStep), Size: 1, Values: []any{host, minute},
			})
			if lo < 0 || minute < lo {
				lo = minute
			}
			hi = max(hi, minute)
		}
		s.PointSize = int64(len(s.Points))
		ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, s)
	}
	if lo >= 0 {
		ds.ExtentList = timeseries.ExtentList{{Start: minuteTime(lo), End: minuteTime(hi)}}
	}
	return ds
}

func sortedKeys(m map[string][]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func minuteTime(m int) time.Time {
	return time.Unix(0, int64(time.Duration(m)*viewStep))
}

func extentMinutes(el timeseries.ExtentList) [][2]int {
	out := make([][2]int, len(el))
	for i, e := range el {
		out[i] = [2]int{int(e.Start.UnixNano() / int64(viewStep)), int(e.End.UnixNano() / int64(viewStep))}
	}
	return out
}

func minutesOf(ds *DataSet) map[string][]int {
	out := map[string][]int{}
	for _, r := range ds.Results {
		for _, s := range r.SeriesList {
			for _, p := range s.Points {
				out[s.Header.Tags["host"]] = append(out[s.Header.Tags["host"]], int(time.Duration(p.Epoch)/viewStep))
			}
		}
	}
	return out
}

func TestView(t *testing.T) {
	src := viewSet(map[string][]int{"a": {1, 2, 3, 4}, "b": {1, 4}, "c": {9}})
	before := fmt.Sprint(minutesOf(src))
	v := src.View(timeseries.Extent{Start: minuteTime(2), End: minuteTime(4)})
	// inclusive at both ends; a series with nothing in range is left out
	require.Equal(t, map[string][]int{"a": {2, 3, 4}, "b": {4}}, minutesOf(v))
	require.Equal(t, [][2]int{{2, 4}}, extentMinutes(v.ExtentList))
	// points are shared, never copied, and the source is untouched
	require.Same(t, &src.Results[0].SeriesList[0].Points[1], &v.Results[0].SeriesList[0].Points[0])
	require.Equal(t, before, fmt.Sprint(minutesOf(src)))
	require.Equal(t, int64(3), v.Results[0].SeriesList[0].PointSize)
	// a series wholly inside the range is the source's own
	whole := src.View(timeseries.Extent{Start: minuteTime(0), End: minuteTime(9)})
	require.Same(t, src.Results[0].SeriesList[1], whole.Results[0].SeriesList[1])
}

func TestMergeDisjoint(t *testing.T) {
	cached := viewSet(map[string][]int{"a": {5, 6}, "b": {5}})
	older := viewSet(map[string][]int{"a": {1, 2}, "c": {2}})
	newer := viewSet(map[string][]int{"a": {7}, "b": {6, 7}})
	before := fmt.Sprint(minutesOf(cached), minutesOf(older), minutesOf(newer))
	merged := MergeDisjoint(cached.TimeRangeQuery, cached, older, newer)
	require.Equal(t, map[string][]int{"a": {1, 2, 5, 6, 7}, "b": {5, 6, 7}, "c": {2}}, minutesOf(merged))
	require.Equal(t, before, fmt.Sprint(minutesOf(cached), minutesOf(older), minutesOf(newer)))
	// series keep the order they were first seen in
	hosts := make([]string, 0, 3)
	for _, s := range merged.Results[0].SeriesList {
		hosts = append(hosts, s.Header.Tags["host"])
	}
	require.Equal(t, []string{"a", "b", "c"}, hosts)
	// coverage merges as the parts held it, gap and all
	require.Equal(t, [][2]int{{1, 2}, {5, 7}}, extentMinutes(merged.ExtentList))
	require.Equal(t, int64(5), merged.Results[0].SeriesList[0].PointSize)
	// a series from one part only is that part's own points
	require.Same(t, &older.Results[0].SeriesList[1].Points[0], &merged.Results[0].SeriesList[2].Points[0])

	// where parts share an epoch, the later part's point wins
	first := viewSet(map[string][]int{"a": {1, 2}})
	second := viewSet(map[string][]int{"a": {2}})
	second.Results[0].SeriesList[0].Points[0].Values = []any{"a", "newer"}
	over := MergeDisjoint(first.TimeRangeQuery, first, second, nil)
	pts := over.Results[0].SeriesList[0].Points
	require.Len(t, pts, 2)
	require.Equal(t, "newer", pts[1].Values[1])
	require.Empty(t, MergeDisjoint(nil).Results)

	// more parts than a series holds inline, and a later part adding more series than one chunk holds
	parts := make([]*DataSet, 0, 6)
	for m := range 5 {
		parts = append(parts, viewSet(map[string][]int{"a": {m}}))
	}
	wide := map[string][]int{}
	for i := range 2*maxMergingChunk + 1 {
		wide[fmt.Sprintf("w%03d", i)] = []int{9}
	}
	parts = append(parts, viewSet(wide))
	many := MergeDisjoint(nil, parts...)
	got := minutesOf(many)
	require.Equal(t, []int{0, 1, 2, 3, 4}, got["a"])
	require.Len(t, got, 2*maxMergingChunk+2)
	require.Equal(t, []int{9}, got["w128"])
}

func TestRetainNewest(t *testing.T) {
	src := viewSet(map[string][]int{"a": {1, 2, 3}, "b": {3, 4}})
	// epochs are counted across all series, so four of them hold 1 through 4
	v, oldest, retained := src.RetainNewest(2)
	require.True(t, retained)
	require.Equal(t, minuteTime(3), oldest)
	require.Equal(t, map[string][]int{"a": {3}, "b": {3, 4}}, minutesOf(v))
	// fewer points than twice the limit still hold more epochs than it
	v, oldest, retained = src.RetainNewest(3)
	require.True(t, retained)
	require.Equal(t, minuteTime(2), oldest)
	require.Equal(t, map[string][]int{"a": {2, 3}, "b": {3, 4}}, minutesOf(v))
	for _, n := range []int{4, 5, 0} {
		same, _, retained := src.RetainNewest(n)
		require.False(t, retained, n)
		require.Same(t, src, same, n)
	}
}

func TestRetainNewestMatchesSortingEveryEpoch(t *testing.T) {
	rng := weaktest.NewRand(7, 11)
	for trial := range 200 {
		// series over a shared range that often ends at epoch zero, some sparse, some with repeated
		// epochs, across two results
		ds := &DataSet{Results: Results{{}, nil, {}}}
		var all []epoch.Epoch
		for i := range 1 + rng.IntN(6) {
			s := &Series{}
			at := -rng.IntN(20)
			for range rng.IntN(12) {
				at = min(at+rng.IntN(3), 0)
				s.Points = append(s.Points, Point{Epoch: epoch.Epoch(at)})
				all = append(all, epoch.Epoch(at))
			}
			r := ds.Results[2*(i%2)]
			r.SeriesList = append(r.SeriesList, s, nil)
		}
		slices.Sort(all)
		all = slices.Compact(all)
		for n := range len(all) + 2 {
			got, ok := newestEpoch(ds.Results, n+1)
			want := len(all) > n+1
			if ok != want || (ok && got != all[len(all)-n-1]) {
				t.Fatalf("trial %d, n %d: got %d, %v; epochs %v", trial, n+1, got, ok, all)
			}
		}
	}
}

func TestRows(t *testing.T) {
	r := viewSet(map[string][]int{"a": {1, 3}, "b": {1, 2}, "c": {3}}).Results[0]
	r.SeriesList = append(r.SeriesList, nil, &Series{})
	collect := func(order RowOrder, limit int) []string {
		var out []string
		for row := range r.Rows(order) {
			out = append(out, fmt.Sprintf("%s%d", row.Series.Header.Tags["host"],
				time.Duration(row.Point.Epoch)/viewStep))
			if len(out) == limit {
				break
			}
		}
		return out
	}
	// by epoch, then by series order
	require.Equal(t, []string{"a1", "b1", "b2", "a3", "c3"}, collect(RowOrder{}, 0))
	require.Equal(t, []string{"a3", "c3", "b2", "a1", "b1"}, collect(RowOrder{Descending: true}, 0))
	// within an epoch by the comparator
	reverse := func(a, b Row) int { return cmp.Compare(b.Series.Header.Tags["host"], a.Series.Header.Tags["host"]) }
	require.Equal(t, []string{"b1", "a1", "b2", "c3", "a3"}, collect(RowOrder{Compare: reverse}, 0))
	// a consumer can stop early
	require.Equal(t, []string{"a1", "b1"}, collect(RowOrder{}, 2))
	var none *Result
	for range none.Rows(RowOrder{}) {
		t.Fatal("a nil result has no rows")
	}
}

func TestAddBytes(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{Fields: timeseries.SeriesFields{
		Values: timeseries.FieldDefinitions{{Name: "raw"}},
	}})
	frame := []byte("first")
	row := b.Row()
	row.SetEpoch(1)
	row.AddBytes(frame)
	require.NoError(t, row.Commit())
	// the value is a copy, so the reused frame can change underneath it
	copy(frame, "xxxxx")
	row = b.Row()
	row.SetEpoch(2)
	row.AddBytes(nil)
	require.NoError(t, row.Commit())
	// a value larger than a chunk gets a chunk of its own
	big := make([]byte, 2*maxByteChunk)
	row = b.Row()
	row.SetEpoch(3)
	row.AddBytes(big)
	require.NoError(t, row.Commit())
	// enough rows to fill a chunk of the slice headers the values point to
	for i := range 2 * maxValueChunk {
		row = b.Row()
		row.SetEpoch(epoch.Epoch(4 + i))
		row.AddBytes([]byte{byte(i)})
		require.NoError(t, row.Commit())
	}
	ds, err := b.Finish()
	require.NoError(t, err)
	pts := ds.Results[0].SeriesList[0].Points
	first, ok := BytesValue(pts[0].Values[0])
	require.True(t, ok)
	require.Equal(t, []byte("first"), first)
	require.Nil(t, pts[1].Values[0])
	own, _ := BytesValue(pts[2].Values[0])
	require.Len(t, own, 2*maxByteChunk)
	for i, p := range pts[3:] {
		got, _ := BytesValue(p.Values[0])
		require.Equal(t, []byte{byte(i)}, got, i)
	}
	// each value's size counts the bytes and the header it points to
	require.Equal(t, pointOverhead+valueOverhead+sliceHeader+len("first"), pts[0].Size)
	raw, ok := BytesValue([]byte("raw"))
	require.True(t, ok)
	require.Equal(t, []byte("raw"), raw)
	for _, v := range []any{(*[]byte)(nil), "text", nil} {
		_, ok := BytesValue(v)
		require.False(t, ok, "%T", v)
	}
}

// a dataset with every shape a view must carry as a clone does: a nil result, nil and empty series,
// volatile extents, an error and the fields a clone leaves behind
func viewEdgeSet() *DataSet {
	ds := viewSet(map[string][]int{"a": {1, 2, 3, 4, 5}, "b": {2, 4}, "c": {5}})
	ds.Results[0].Name = "r0"
	ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, nil, &Series{Header: SeriesHeader{Name: "empty"}})
	ds.Results = append(ds.Results, nil, &Result{StatementID: 2, Name: "r2", Error: "partial",
		SeriesList: SeriesList{viewSet(map[string][]int{"d": {1, 3}}).Results[0].SeriesList[0]}})
	ds.VolatileExtentList = timeseries.ExtentList{{Start: minuteTime(4), End: minuteTime(5)}}
	ds.Error, ds.SourceResultType = "an error", "matrix"
	ds.Status, ds.ErrorType, ds.Warnings = "success", "none", []string{"w"}
	return ds
}

// what a clone and a view must agree on, without the query, which a view shares and a clone copies
func viewComparable(ds *DataSet) string {
	return fmt.Sprintf("%+v %+v %+v %q %q %q %q %v", ds.ExtentList, ds.VolatileExtentList, ds.Results,
		ds.Error, ds.SourceResultType, ds.Status, ds.ErrorType, ds.Warnings)
}

func TestViewsMatchClones(t *testing.T) {
	src := viewEdgeSet()
	for name, e := range map[string]timeseries.Extent{
		"within":      {Start: minuteTime(2), End: minuteTime(4)},
		"encompasses": {Start: minuteTime(0), End: minuteTime(9)},
		"outside":     {Start: minuteTime(20), End: minuteTime(30)},
		"overlaps":    {Start: minuteTime(4), End: minuteTime(9)},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, viewComparable(src.CroppedClone(e).(*DataSet)), viewComparable(src.CroppedView(e)))
		})
	}
	require.Equal(t, viewComparable(src.Clone().(*DataSet)), viewComparable(src.FullView()))
	noExtents := viewEdgeSet()
	noExtents.ExtentList = nil
	e := timeseries.Extent{Start: minuteTime(2), End: minuteTime(3)}
	require.Equal(t, viewComparable(noExtents.CroppedClone(e).(*DataSet)), viewComparable(noExtents.CroppedView(e)))
}

// every value a view shares or copies from its source, nil entries included
func viewSnapshot(ds *DataSet) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%v|%v|%q|%q|%q|%q|%v|%p\n", ds.ExtentList, ds.VolatileExtentList, ds.Error,
		ds.SourceResultType, ds.Status, ds.ErrorType, ds.Warnings, ds.TimeRangeQuery)
	for _, r := range ds.Results {
		if r == nil {
			b.WriteString("nil result\n")
			continue
		}
		fmt.Fprintf(&b, "result %d %q %q\n", r.StatementID, r.Name, r.Error)
		for _, s := range r.SeriesList {
			if s == nil {
				b.WriteString(" nil series\n")
				continue
			}
			fmt.Fprintf(&b, " %q %v %d %d %v\n", s.Header.Name, s.Header.Tags, s.PointSize, len(s.Points), s.Points)
		}
	}
	return b.String()
}

func TestViewsLeaveTheirSourceAsItWas(t *testing.T) {
	for name, view := range map[string]func(*DataSet) *DataSet{
		"full": (*DataSet).FullView,
		"cropped": func(ds *DataSet) *DataSet {
			return ds.CroppedView(timeseries.Extent{Start: minuteTime(1), End: minuteTime(4)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			src := viewEdgeSet()
			before := viewSnapshot(src)
			v := view(src)
			require.Same(t, &src.Results[0].SeriesList[0].Points[1].Values[0], &v.Results[0].SeriesList[0].Points[1].Values[0],
				"a view shares its source's values")
			// a merge adds to and overlaps the view's series, and brings a new one
			v.Merge(true, viewSet(map[string][]int{"a": {3, 6}, "e": {7}}))
			v.Merge(false, viewSet(map[string][]int{"b": {8}}))
			v.SetExtents(nil)
			v.SetVolatileExtents(timeseries.ExtentList{{Start: minuteTime(1), End: minuteTime(9)}})
			v.CropToRange(timeseries.Extent{Start: minuteTime(3), End: minuteTime(8)})
			v.Sort()
			require.Equal(t, before, viewSnapshot(src), "a change to the view reached its source")
		})
	}
}
