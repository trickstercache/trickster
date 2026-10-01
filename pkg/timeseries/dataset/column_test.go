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
	"reflect"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

func TestColumnValueTypes(t *testing.T) {
	blob, n := []byte("row"), int64(-7)
	tests := []struct {
		in   any
		kind Kind
		want any
	}{
		{nil, KindNull, nil},
		{false, KindBool, false},
		{int(-3), KindInt64, int64(-3)},
		{int32(-4), KindInt64, int64(-4)},
		{int16(-5), KindInt64, int64(-5)},
		{int8(-6), KindInt64, int64(-6)},
		{&n, KindInt64, int64(-7)},
		{(*int64)(nil), KindNull, nil},
		{uint32(3), KindUint64, uint64(3)},
		{uint16(4), KindUint64, uint64(4)},
		{uint8(5), KindUint64, uint64(5)},
		{&blob, KindBytes, []byte("row")},
		{(*[]byte)(nil), KindNull, nil},
		{[]byte(nil), KindNull, nil},
		{[]byte{}, KindBytes, []byte{}},
		{"", KindString, ""},
		{float32(1.5), KindExt, float32(1.5)},
	}
	for _, tc := range tests {
		l := NewColumnLog(DuplicatesKeep)
		s := l.AddSeries(1)
		l.AddValue(tc.in)
		if err := l.Commit(s, 1); err != nil {
			t.Fatal(err)
		}
		segs, _ := l.Finish()
		col := segs[0].Col(0)
		if col.Kind() != tc.kind || !reflect.DeepEqual(col.Value(0), tc.want) {
			t.Errorf("%#v: kind %d value %#v, want %d %#v", tc.in, col.Kind(), col.Value(0), tc.kind, tc.want)
		}
	}
}

func TestColumnBulkReads(t *testing.T) {
	ints := segmentOf(Points{{Epoch: 1, Values: []any{int64(4)}}, {Epoch: 2, Values: []any{int64(5)}}}, 1)
	col := ints.Col(0)
	if v, ok := col.Int64s(); !ok || len(v) != 2 || v[1] != 5 {
		t.Errorf("Int64s gave %v %t", v, ok)
	}
	if _, ok := col.Float64s(); ok {
		t.Error("ints read as floats")
	}
	var empty Column
	empty.kind = KindInt64
	if v, ok := empty.Int64s(); !ok || len(v) != 0 {
		t.Errorf("empty Int64s gave %v %t", v, ok)
	}
	empty.kind = KindFloat64
	if v, ok := empty.Float64s(); !ok || len(v) != 0 {
		t.Errorf("empty Float64s gave %v %t", v, ok)
	}
	withNull := segmentOf(Points{{Epoch: 1, Values: []any{int64(4)}}, {Epoch: 2, Values: []any{nil}}}, 1)
	col = withNull.Col(0)
	if _, ok := col.Int64s(); ok || !col.HasNulls() || col.Kind() != KindInt64 {
		t.Errorf("a column with a null: kind %d nulls %t", col.Kind(), col.HasNulls())
	}
	col = ints.Col(0)
	if col.HasNulls() {
		t.Error("a full column reported nulls")
	}
	mixed := segmentOf(Points{{Epoch: 1, Values: []any{int64(4)}}, {Epoch: 2, Values: []any{"a"}}}, 1)
	col = mixed.Col(0)
	if col.HasNulls() || col.Text(1) != "a" {
		t.Error("a mixed column without nulls reported nulls")
	}
	if !KindString.IsBytes() || KindExt.IsBytes() {
		t.Error("IsBytes")
	}
}

func TestSegmentWriterColumnCounts(t *testing.T) {
	// rows missing a column get nulls, and a column the writer lacks is dropped
	narrow := Segments{segmentOf(Points{{Epoch: 1, Values: []any{1.0}}, {Epoch: 1, Values: []any{2.0}}}, 1)}
	wide := Segments{segmentOf(Points{{Epoch: 1, Values: []any{"3", "x", "y"}}}, 3)}
	w := newSegmentWriter(3, 2, narrow, wide)
	w.copyRun(&narrow[0], 0, 1)
	w.copyRow(&narrow[0], 1, &cellValue{kind: KindFloat64})
	w.copyRow(&wide[0], 0, nil)
	seg := w.finish()
	c1 := seg.Col(1)
	if seg.Len() != 3 || !c1.IsNull(0) || !c1.IsNull(1) || c1.Text(2) != "x" {
		t.Errorf("rows %d, second column %v", seg.Len(), pointsOf(Segments{seg}))
	}
	// a sum over rows missing their first column's peers keeps the first row
	got := MergeSegments(narrow, wide, MergeOpts{SortPoints: true, Strategy: merge.StrategySum})
	if got.Len() != 1 {
		t.Errorf("summing gave %d rows", got.Len())
	}
}

func TestCopyBytesRunFallbacks(t *testing.T) {
	cell := func(off, n uint64) uint64 { return off<<cellOffsetShift | n }
	cases := []struct {
		name string
		col  Column
		want []string
	}{
		// values whose bytes aren't in row order copy one by one
		{
			"reversed",
			Column{kind: KindString, vals: []uint64{cell(2, 2), cell(0, 2)}, data: []byte("aabb")},
			[]string{"bb", "aa"},
		},
		// values spread thin through their data copy one by one, leaving the rest behind
		{
			"sparse",
			Column{kind: KindString, vals: []uint64{cell(0, 1), cell(100, 1)}, data: make([]byte, 101)},
			[]string{"\x00", "\x00"},
		},
		{
			"empty values",
			Column{kind: KindBytes, vals: []uint64{cell(7, 0), cell(0, 2), cell(9, 0)}, data: []byte("xy")},
			[]string{"", "xy", ""},
		},
	}
	for _, tc := range cases {
		src := Segment{epochs: []epoch.Epoch{1, 2, 3}[:len(tc.col.vals)], cols: []Column{tc.col}}
		w := newSegmentWriter(len(tc.col.vals), 1, Segments{src})
		w.copyRun(&src, 0, len(tc.col.vals))
		seg := w.finish()
		col := seg.Col(0)
		for i, want := range tc.want {
			if got := col.Text(i); got != want {
				t.Errorf("%s: value %d is %q, want %q", tc.name, i, got, want)
			}
		}
		if tc.name == "sparse" && len(col.data) != 2 {
			t.Errorf("sparse values kept %d bytes of data", len(col.data))
		}
	}
}

func TestColumnLogGrowHint(t *testing.T) {
	l := NewColumnLog(DuplicatesKeep)
	l.Grow(2, 2, 64)
	s := l.AddSeries(1)
	for i := range 2 {
		l.AddString([]byte("value" + strconv.Itoa(i)))
		if err := l.Commit(s, epoch.Epoch(i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(l.data) != 1 {
		t.Errorf("hinted values took %d chunks", len(l.data))
	}
	// a rollback trims the staged bytes from a chunk that holds committed ones
	used := len(l.data[0])
	l.AddString([]byte("staged"))
	l.Rollback()
	if len(l.data[0]) != used {
		t.Errorf("a rollback left %d bytes, want %d", len(l.data[0]), used)
	}
	segs, _ := l.Finish()
	col := segs[0].Col(0)
	if col.Text(1) != "value1" {
		t.Errorf("value %q", col.Text(1))
	}
}
