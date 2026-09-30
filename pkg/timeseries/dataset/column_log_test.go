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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

var testPolicies = []DuplicatePolicy{DuplicatesKeep, DuplicatesFirstWins, DuplicatesLastWins, DuplicatesError}

func TestColumnLogMatchesLegacyBuild(t *testing.T) {
	rng := weaktest.NewRand(9, 10)
	for iter := range 2000 {
		policy := testPolicies[rng.IntN(len(testPolicies))]
		series := 1 + rng.IntN(4)
		cols := make([]int, series)
		profiles := make([][]int, series)
		headers := make([]SeriesHeader, series)
		legacy := &legacyBuild{policy: policy, series: make([]legacyBuildSeries, series)}
		b := NewBuilder(nil, BuilderOptions{Duplicates: policy})
		l := NewColumnLog(policy)
		for i := range series {
			cols[i] = 1 + rng.IntN(2)
			profiles[i] = randProfiles(rng, cols[i])
			headers[i] = SeriesHeader{Name: "s" + strconv.Itoa(i), ValueFieldsList: make(timeseries.FieldDefinitions, cols[i])}
			for c := range cols[i] {
				headers[i].ValueFieldsList[c].Name = "v" + strconv.Itoa(c)
			}
			b.StartSeries(headers[i])
			if got := l.AddSeries(cols[i]); got != i {
				t.Fatalf("AddSeries returned %d, want %d", got, i)
			}
		}
		ordered := rng.IntN(2) == 0
		var wantErr error
		clock := make([]int, series)
		for row := range rng.IntN(40) {
			s := rng.IntN(series)
			e := rng.IntN(12)
			if ordered {
				// mostly rising epochs per series, with repeats
				clock[s] += rng.IntN(2)
				e = clock[s]
			}
			pts := randPoints(rng, 1, cols[s], 1, true, profiles[s])
			pts[0].Epoch = epoch.Epoch(e)
			err1 := legacy.append(s, pts[0])
			b.StartSeries(headers[s])
			err2 := b.AppendPoint(pts[0])
			for _, v := range pts[0].Values {
				l.AddValue(v)
			}
			err3 := l.Commit(s, pts[0].Epoch)
			if !errors.Is(err2, err1) || !errors.Is(err3, err1) {
				t.Fatalf("iteration %d row %d: Commit %v, AppendPoint %v, legacy %v", iter, row, err3, err2, err1)
			}
			if err1 != nil && wantErr == nil {
				wantErr = err1
			}
		}
		if wantErr != nil {
			continue
		}
		err1 := legacy.finish()
		ds, err2 := b.Finish()
		segs, err3 := l.Finish()
		context := fmt.Sprintf("iteration %d policy %d ordered %t", iter, policy, ordered)
		if !errors.Is(err2, err1) || !errors.Is(err3, err1) {
			t.Fatalf("%s: Finish %v, Builder %v, legacy %v", context, err3, err2, err1)
		}
		if err1 != nil {
			continue
		}
		if len(segs) != series {
			t.Fatalf("%s: %d segments, want %d", context, len(segs), series)
		}
		for i := range series {
			want := legacy.series[i].pts
			requireSamePoints(t, want, pointsOf(Segments{segs[i]}), context+" series "+strconv.Itoa(i))
			requireSamePoints(t, want, ds.Results[0].SeriesList[i].Points(), context+" builder series "+strconv.Itoa(i))
		}
	}
}

func TestColumnLogTypedAdds(t *testing.T) {
	l := NewColumnLog(DuplicatesKeep)
	l.Grow(4, 40, 64)
	s := l.AddSeries(10)
	add := func(n int) {
		l.AddNull()
		l.AddBool(n%2 == 0)
		l.AddInt64(int64(-n))
		l.AddUint64(uint64(n))
		l.AddFloat64(float64(n) / 2)
		l.AddString([]byte("s" + strconv.Itoa(n)))
		l.AddBytes([]byte{byte(n)})
		l.AddNumber([]byte(strconv.Itoa(n * 10)))
		l.AddExt(struct{ N int }{n})
		l.AddBytes(nil)
	}
	for n := range 3 {
		add(n)
		if err := l.Commit(s, epoch.Epoch(3-n)); err != nil {
			t.Fatal(err)
		}
	}
	if l.Rows() != 3 || l.Series() != 1 {
		t.Fatalf("Rows %d Series %d", l.Rows(), l.Series())
	}
	segs, err := l.Finish()
	if err != nil {
		t.Fatal(err)
	}
	seg := segs[0]
	if !seg.IsSorted() || seg.Epoch(0) != 1 {
		t.Fatalf("rows not sorted: %v", seg.Epochs())
	}
	wantKinds := []Kind{
		KindNull, KindBool, KindInt64, KindUint64, KindFloat64, KindString, KindBytes,
		KindNumber, KindExt, KindNull,
	}
	for c, k := range wantKinds {
		col := seg.Col(c)
		if col.Kind() != k || col.KindAt(0) != k || col.Len() != 3 {
			t.Errorf("column %d: kind %d, want %d", c, col.Kind(), k)
		}
	}
	// row 0 is the last row added, n == 2
	checks := []struct {
		col  int
		want any
	}{
		{0, nil},
		{1, true},
		{2, int64(-2)},
		{3, uint64(2)},
		{4, 1.0},
		{5, "s2"},
		{6, []byte{2}},
		{7, json.Number("20")},
		{8, struct{ N int }{2}},
		{9, nil},
	}
	for _, c := range checks {
		col := seg.Col(c.col)
		if got := col.Value(0); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("column %d value %#v, want %#v", c.col, got, c.want)
		}
	}
	col := seg.Col(5)
	if col.Text(0) != "s2" || string(col.Bytes(0)) != "s2" {
		t.Errorf("text %q", col.Text(0))
	}
}

func TestColumnLogErrors(t *testing.T) {
	l := NewColumnLog(DuplicatesKeep)
	s := l.AddSeries(1)
	if err := l.Commit(s, 1); !errors.Is(err, ErrInvalidRow) {
		t.Errorf("a row missing a value gave %v", err)
	}
	l.AddFloat64(1)
	if err := l.Commit(5, 1); !errors.Is(err, ErrInvalidRow) {
		t.Errorf("an unknown series gave %v", err)
	}
	l.AddFloat64(1)
	l.AddExt(1)
	l.AddString([]byte("discarded"))
	l.Rollback()
	if len(l.data) != 1 || len(l.data[0]) != 0 || l.dataLen != 0 {
		t.Errorf("a rollback kept %d chunks, %d bytes", len(l.data), l.dataLen)
	}
	l.AddFloat64(2)
	if err := l.Commit(s, 1); err != nil {
		t.Fatal(err)
	}
	segs, err := l.Finish()
	if err != nil || segs[0].Len() != 1 {
		t.Fatalf("Finish gave %v, %d rows", err, segs[0].Len())
	}
	col := segs[0].Col(0)
	if col.Float64(0) != 2 {
		t.Errorf("value %v after a rollback", col.Float64(0))
	}
	if _, err := l.Finish(); !errors.Is(err, ErrBuilderFinished) {
		t.Errorf("a second Finish gave %v", err)
	}
	if err := l.Commit(s, 2); !errors.Is(err, ErrBuilderFinished) {
		t.Errorf("a Commit after Finish gave %v", err)
	}
	empty, err := NewColumnLog(DuplicatesKeep).Finish()
	if err != nil || len(empty) != 0 {
		t.Errorf("an empty log gave %v, %v", empty, err)
	}
}

func TestColumnLogColumnKinds(t *testing.T) {
	l := NewColumnLog(DuplicatesLastWins)
	s := l.AddSeries(3)
	rows := [][]any{
		{1.5, "a", nil},
		{2.5, 7.0, nil},
		{math.Inf(1), nil, nil},
	}
	for i, r := range rows {
		for _, v := range r {
			l.AddValue(v)
		}
		if err := l.Commit(s, epoch.Epoch(i)); err != nil {
			t.Fatal(err)
		}
	}
	segs, _ := l.Finish()
	c0, c1, c2 := segs[0].Col(0), segs[0].Col(1), segs[0].Col(2)
	if f, ok := c0.Float64s(); !ok || len(f) != 3 || f[1] != 2.5 || c0.HasNulls() {
		t.Errorf("uniform floats: %v %t", f, ok)
	}
	if _, ok := c0.Int64s(); ok {
		t.Error("floats read as ints")
	}
	if c1.Kind() != KindMixed || !c1.HasNulls() || !c1.IsNull(2) || c1.KindAt(1) != KindFloat64 {
		t.Errorf("mixed column: kind %d", c1.Kind())
	}
	if _, ok := c1.Float64s(); ok {
		t.Error("a mixed column read as floats")
	}
	if c2.Kind() != KindNull || !c2.HasNulls() || c2.Value(1) != nil {
		t.Errorf("null column: kind %d", c2.Kind())
	}
	if segs[0].Size() <= 0 || c1.Size() <= 0 {
		t.Error("no size")
	}
}

func TestColumnLogPooledBuffers(t *testing.T) {
	// a build reuses the arrays an earlier one discarded, which its Segments must never share
	rng := weaktest.NewRand(11, 12)
	build := func(seed int) (Points, Segments) {
		pts := randPoints(rng, 300+seed, 3, 50, seed%2 == 0, randProfiles(rng, 3))
		l := NewColumnLog(DuplicatesKeep)
		s := l.AddSeries(3)
		for i := range pts {
			for _, v := range pts[i].Values {
				l.AddValue(v)
			}
			if err := l.Commit(s, pts[i].Epoch); err != nil {
				t.Fatal(err)
			}
		}
		// a row left staged is discarded with the log
		l.AddString([]byte("staged"))
		segs, err := l.Finish()
		if err != nil {
			t.Fatal(err)
		}
		l.Rollback()
		if l.Staged() != 0 {
			t.Fatal("a finished log holds no staged values")
		}
		want := pts.Clone()
		slices.SortStableFunc(want, legacyPointCmp)
		return want, Segments(segs)
	}
	var built []Points
	var segs []Segments
	for seed := range 6 {
		want, got := build(seed)
		built, segs = append(built, want), append(segs, got)
	}
	for i := range built {
		requireSamePoints(t, built[i], pointsOf(segs[i]), fmt.Sprintf("build %d", i))
	}
	// a log too large to pool is dropped rather than kept
	l := NewColumnLog(DuplicatesKeep)
	l.Grow(1, 1, maxPooledLogBytes)
	l.AddSeries(1)
	l.AddInt64(1)
	if err := l.Commit(0, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Finish(); err != nil {
		t.Fatal(err)
	}
}
