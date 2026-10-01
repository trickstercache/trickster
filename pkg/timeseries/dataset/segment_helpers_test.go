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
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

// the value profiles random columns are drawn from; aggregation needs numeric text and floats, and
// the rest exercise every Kind, nulls and mixing within a column
const (
	profileFloat = iota
	profileNumericText
	profileMixed
	profileBytes
	profileAny
	profileCount
)

func randValue(rng *weaktest.Rand, profile int) any {
	switch profile {
	case profileFloat:
		if rng.IntN(10) == 0 {
			return nil
		}
		return float64(rng.IntN(1000)) / 8
	case profileNumericText:
		switch rng.IntN(12) {
		case 0:
			return "h" + strconv.Itoa(rng.IntN(5)) // not a number: the aggregation's NaN paths
		case 1:
			return nil
		}
		return strconv.FormatFloat(float64(rng.IntN(2000))/4, 'f', -1, 64)
	case profileBytes:
		b := make([]byte, 1+rng.IntN(12))
		for i := range b {
			b[i] = byte('a' + rng.IntN(26))
		}
		return b
	}
	switch rng.IntN(11) {
	case 0:
		return nil
	case 1:
		return rng.IntN(2) == 0
	case 2:
		return int64(rng.IntN(1<<20)) - 1<<19
	case 3:
		return uint64(rng.IntN(1 << 20))
	case 4:
		return float64(rng.IntN(1000)) / 3
	case 5:
		return "s" + strconv.Itoa(rng.IntN(100))
	case 6:
		return []byte("b" + strconv.Itoa(rng.IntN(100)))
	case 7:
		return json.Number(strconv.Itoa(rng.IntN(1000)))
	case 8:
		return map[string]any{"k": "v" + strconv.Itoa(rng.IntN(9))}
	case 9:
		return ""
	}
	return strconv.Itoa(rng.IntN(50))
}

// randPoints returns n points of cols values over epochs drawn from [0, span), in random order
// unless sorted is set; a small span repeats epochs
func randPoints(rng *weaktest.Rand, n, cols, span int, sorted bool, profiles []int) Points {
	pts := make(Points, n)
	for i := range pts {
		pts[i].Epoch = epoch.Epoch(rng.IntN(span)) * 10
		pts[i].Values = make([]any, cols)
		for c := range cols {
			pts[i].Values[c] = randValue(rng, profiles[c])
		}
	}
	if sorted {
		slices.SortStableFunc(pts, legacyPointCmp)
	}
	return pts
}

func randProfiles(rng *weaktest.Rand, cols int) []int {
	out := make([]int, cols)
	for i := range out {
		out[i] = rng.IntN(profileCount)
	}
	return out
}

// segmentOf returns pts as one Segment in their order, sorted or not
func segmentOf(pts Points, cols int) Segment {
	w := newSegmentWriter(len(pts), cols)
	for i := range pts {
		w.epochs = append(w.epochs, pts[i].Epoch)
		for c := range cols {
			var v any
			if c < len(pts[i].Values) {
				v = pts[i].Values[c]
			}
			w.appendValue(c, v)
		}
	}
	return w.finish()
}

// segmentsOf splits pts into Segments at the given row counts, with an empty Segment among them
func segmentsOf(pts Points, cols int, splits ...int) Segments {
	var out Segments
	for _, n := range splits {
		n = min(n, len(pts))
		out = append(out, segmentOf(pts[:n], cols))
		pts = pts[n:]
	}
	out = append(out, Segment{}, segmentOf(pts, cols))
	return out
}

// pointsOf returns the rows of s as Points, without sizes
func pointsOf(s Segments) Points {
	out := make(Points, 0, s.Len())
	for i := range s {
		seg := &s[i]
		for j := range seg.Len() {
			p := Point{Epoch: seg.Epoch(j), Values: make([]any, seg.NumCols())}
			for c := range seg.NumCols() {
				col := seg.Col(c)
				p.Values[c] = col.Value(j)
			}
			out = append(out, p)
		}
	}
	return out
}

// rowValues returns a row's values, boxed
func rowValues(r Row) []any {
	out := make([]any, r.Seg.NumCols())
	for c := range out {
		out[c] = r.Value(c)
	}
	return out
}

// requireSamePoints fails unless want and got hold the same epochs and values, ignoring sizes
func requireSamePoints(t *testing.T, want, got Points, context string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: %d points, want %d\nwant %s\ngot  %s", context, len(got), len(want),
			describePoints(want), describePoints(got))
	}
	for i := range want {
		if want[i].Epoch != got[i].Epoch || !reflect.DeepEqual(want[i].Values, got[i].Values) {
			t.Fatalf("%s: point %d is %d %#v, want %d %#v", context, i, got[i].Epoch, got[i].Values,
				want[i].Epoch, want[i].Values)
		}
	}
}

func describePoints(p Points) string {
	s := ""
	for i := range p {
		s += fmt.Sprintf("[%d %v]", p[i].Epoch, p[i].Values)
	}
	return s
}

// joins non-numeric values under merges with ValueOperations, as histogram operations would
type testValueOperations struct{}

func (testValueOperations) MergeValues(dst, src any, _ merge.Strategy) (any, bool) {
	d, ok1 := dst.(string)
	s, ok2 := src.(string)
	if !ok1 || !ok2 {
		return nil, false
	}
	return d + "|" + s, true
}

func (testValueOperations) DivideValue(value any, divisor float64) (any, bool) {
	s, ok := value.(string)
	if !ok {
		return nil, false
	}
	return s + "/" + strconv.FormatFloat(divisor, 'f', -1, 64), true
}

func (testValueOperations) PairingHash(*SeriesHeader, string) Hash {
	return 0
}

func (testValueOperations) FinalizeMerge(*DataSet, merge.Strategy) {}
