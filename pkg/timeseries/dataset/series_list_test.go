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
	"slices"
	"sort"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func TestEqualHeader(t *testing.T) {
	t.Run("nil comparison", func(t *testing.T) {
		sl := SeriesList{testSeries()}
		if sl.EqualHeader(nil) {
			t.Error("expected false")
		}
	})

	t.Run("different names", func(t *testing.T) {
		sl := SeriesList{testSeries()}
		s := testSeries()
		sl2 := SeriesList{s}
		sl2[0].Header.Name = "test2"
		if sl.EqualHeader(sl2) {
			t.Error("expected false")
		}
	})

	t.Run("identical headers", func(t *testing.T) {
		sl := SeriesList{testSeries()}
		sl2 := SeriesList{testSeries()}
		if !sl.EqualHeader(sl2) {
			t.Error("expected true")
		}
	})

	t.Run("both nil at same index", func(t *testing.T) {
		sl := SeriesList{nil}
		sl2 := SeriesList{nil}
		if !sl.EqualHeader(sl2) {
			t.Error("expected true for matching nil entries")
		}
	})

	t.Run("one nil one non-nil at same index", func(t *testing.T) {
		sl := SeriesList{testSeries()}
		sl2 := SeriesList{nil}
		if sl.EqualHeader(sl2) {
			t.Error("expected false")
		}
	})

	t.Run("different lengths", func(t *testing.T) {
		sl := SeriesList{testSeries(), testSeries2()}
		sl2 := SeriesList{testSeries()}
		if sl.EqualHeader(sl2) {
			t.Error("expected false for different lengths")
		}
	})
}

func TestListMerge(t *testing.T) {
	tests := []struct {
		name     string
		sl1, sl2 SeriesList
		expected []string
	}{
		{
			name:     "disjoint series",
			sl1:      SeriesList{testSeries()},
			sl2:      SeriesList{testSeries2()},
			expected: []string{"test", "test2"},
		},
		{
			name:     "overlapping with dedup",
			sl1:      SeriesList{testSeries(), testSeries3()},
			sl2:      SeriesList{testSeries(), testSeries2()},
			expected: []string{"test", "test2", "test3"},
		},
		{
			name:     "heavy dedup",
			sl1:      SeriesList{testSeries3(), testSeries2(), testSeries(), testSeries3()},
			sl2:      SeriesList{testSeries(), testSeries2(), testSeries3(), testSeries3(), testSeries()},
			expected: []string{"test", "test2", "test3"},
		},
		{
			name:     "empty sl1",
			sl1:      SeriesList{},
			sl2:      SeriesList{testSeries(), testSeries2()},
			expected: []string{"test", "test2"},
		},
		{
			name:     "empty sl2",
			sl1:      SeriesList{testSeries(), testSeries2()},
			sl2:      SeriesList{},
			expected: []string{"test", "test2"},
		},
		{
			name:     "nil entries in both lists",
			sl1:      SeriesList{testSeries(), nil},
			sl2:      SeriesList{nil, testSeries2()},
			expected: []string{"test", "test2"},
		},
	}

	namesFromList := func(sl SeriesList) []string {
		out := make([]string, len(sl))
		for i, s := range sl {
			out[i] = s.Header.Name
		}
		return out
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := test.sl1.Merge(test.sl2, true)
			if len(out) != len(test.expected) {
				t.Errorf("expected %d got %d", len(test.expected), len(out))
			} else {
				names := namesFromList(out)
				if !slices.Equal(names, test.expected) {
					t.Errorf("expected %v got %v", test.expected, names)
				}
			}
		})
	}

	// verify Merge deduplicates correctly even when hashes haven't been pre-calculated
	t.Run("uncached hashes", func(t *testing.T) {
		// create series with fresh headers (hash field = 0)
		s1 := &Series{
			Header: SeriesHeader{Name: "metric", Tags: Tags{"env": "prod"}},
			Points: testPoints(),
		}
		s2 := &Series{
			Header: SeriesHeader{Name: "metric", Tags: Tags{"env": "prod"}},
			Points: Points{{Epoch: epoch.Epoch(15 * timeseries.Second), Size: 16, Values: []any{1}}},
		}
		out := SeriesList{s1}.Merge(SeriesList{s2}, true)
		if len(out) != 1 {
			t.Fatalf("expected 1 series (deduped), got %d", len(out))
		}
		if len(out[0].Points) != 3 {
			t.Errorf("expected 3 points after merge, got %d", len(out[0].Points))
		}
	})

	// verify point merging when same series appears in both lists
	t.Run("point merge on overlap", func(t *testing.T) {
		s1 := testSeries()
		s1.Points = testPoints() // epochs 5, 10
		s2 := testSeries()       // same header hash
		s2.Points = Points{
			{Epoch: epoch.Epoch(15 * timeseries.Second), Size: 27, Values: []any{1, 34}},
		}
		out := SeriesList{s1}.Merge(SeriesList{s2}, true)
		if len(out) != 1 {
			t.Fatalf("expected 1 series, got %d", len(out))
		}
		if len(out[0].Points) != 3 {
			t.Errorf("expected 3 points after merge, got %d", len(out[0].Points))
		}
	})
}

func TestListMergeWithStrategy(t *testing.T) {
	// helper to build series with string-encoded float values at given epochs
	makeSeries := func(name string, tags Tags, points ...ev) *Series {
		p := make(Points, len(points))
		for i, pt := range points {
			p[i] = Point{
				Epoch:  epoch.Epoch(pt.epoch),
				Size:   32,
				Values: []any{pt.value},
			}
		}
		return &Series{
			Header: SeriesHeader{Name: name, Tags: tags},
			Points: p,
		}
	}

	type ev = struct {
		epoch int64
		value string
	}

	t.Run("sum aggregates matching series", func(t *testing.T) {
		// Two series with identical labels but different values at the same epoch
		s1 := makeSeries("cpu", Tags{"host": "a"}, ev{100, "10"}, ev{200, "20"})
		s2 := makeSeries("cpu", Tags{"host": "a"}, ev{100, "30"}, ev{200, "40"})
		out := SeriesList{s1}.MergeWithStrategy(SeriesList{s2}, true, MergeStrategySum)
		if len(out) != 1 {
			t.Fatalf("expected 1 series, got %d", len(out))
		}
		if len(out[0].Points) != 2 {
			t.Fatalf("expected 2 points, got %d", len(out[0].Points))
		}
		if out[0].Points[0].Values[0] != "40" {
			t.Errorf("expected sum 40, got %v", out[0].Points[0].Values[0])
		}
		if out[0].Points[1].Values[0] != "60" {
			t.Errorf("expected sum 60, got %v", out[0].Points[1].Values[0])
		}
	})

	t.Run("different labels stay separate", func(t *testing.T) {
		s1 := makeSeries("cpu", Tags{"host": "a"}, ev{100, "10"})
		s2 := makeSeries("cpu", Tags{"host": "b"}, ev{100, "30"})
		out := SeriesList{s1}.MergeWithStrategy(SeriesList{s2}, true, MergeStrategySum)
		if len(out) != 2 {
			t.Fatalf("expected 2 series (different labels), got %d", len(out))
		}
	})

	t.Run("avg divides by count", func(t *testing.T) {
		s1 := makeSeries("mem", Tags{}, ev{100, "10"})
		s2 := makeSeries("mem", Tags{}, ev{100, "30"})
		out := SeriesList{s1}.MergeWithStrategy(SeriesList{s2}, true, MergeStrategyAvg)
		if len(out) != 1 {
			t.Fatalf("expected 1 series, got %d", len(out))
		}
		if out[0].Points[0].Values[0] != "20" {
			t.Errorf("expected avg 20, got %v", out[0].Points[0].Values[0])
		}
	})

	t.Run("min takes minimum", func(t *testing.T) {
		s1 := makeSeries("disk", Tags{}, ev{100, "50"})
		s2 := makeSeries("disk", Tags{}, ev{100, "20"})
		out := SeriesList{s1}.MergeWithStrategy(SeriesList{s2}, true, MergeStrategyMin)
		if out[0].Points[0].Values[0] != "20" {
			t.Errorf("expected min 20, got %v", out[0].Points[0].Values[0])
		}
	})

	t.Run("max takes maximum", func(t *testing.T) {
		s1 := makeSeries("disk", Tags{}, ev{100, "50"})
		s2 := makeSeries("disk", Tags{}, ev{100, "20"})
		out := SeriesList{s1}.MergeWithStrategy(SeriesList{s2}, true, MergeStrategyMax)
		if out[0].Points[0].Values[0] != "50" {
			t.Errorf("expected max 50, got %v", out[0].Points[0].Values[0])
		}
	})

	t.Run("count counts observations", func(t *testing.T) {
		s1 := makeSeries("req", Tags{}, ev{100, "999"})
		s2 := makeSeries("req", Tags{}, ev{100, "888"})
		out := SeriesList{s1}.MergeWithStrategy(SeriesList{s2}, true, MergeStrategyCount)
		if out[0].Points[0].Values[0] != "2" {
			t.Errorf("expected count 2, got %v", out[0].Points[0].Values[0])
		}
	})

	t.Run("dedup strategy delegates to Merge", func(t *testing.T) {
		s1 := makeSeries("cpu", Tags{"host": "a"}, ev{100, "10"})
		s2 := makeSeries("cpu", Tags{"host": "a"}, ev{100, "30"})
		out := SeriesList{s1}.MergeWithStrategy(SeriesList{s2}, true, MergeStrategyDedup)
		if len(out) != 1 {
			t.Fatalf("expected 1 series, got %d", len(out))
		}
		// dedup: last value wins
		if out[0].Points[0].Values[0] != "30" {
			t.Errorf("expected dedup value 30, got %v", out[0].Points[0].Values[0])
		}
	})

	t.Run("empty inputs", func(t *testing.T) {
		s1 := makeSeries("cpu", Tags{}, ev{100, "10"})
		out := SeriesList{s1}.MergeWithStrategy(SeriesList{}, true, MergeStrategySum)
		if len(out) != 1 {
			t.Errorf("expected 1, got %d", len(out))
		}
		out = SeriesList{}.MergeWithStrategy(SeriesList{s1}, true, MergeStrategySum)
		if len(out) != 1 {
			t.Errorf("expected 1, got %d", len(out))
		}
	})

	t.Run("non-overlapping epochs preserved", func(t *testing.T) {
		s1 := makeSeries("cpu", Tags{}, ev{100, "10"})
		s2 := makeSeries("cpu", Tags{}, ev{200, "20"})
		out := SeriesList{s1}.MergeWithStrategy(SeriesList{s2}, true, MergeStrategySum)
		if len(out) != 1 {
			t.Fatalf("expected 1 series, got %d", len(out))
		}
		if len(out[0].Points) != 2 {
			t.Fatalf("expected 2 points (no overlap), got %d", len(out[0].Points))
		}
	})
}

func TestSeriesListClone(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		sl := SeriesList{}
		out := sl.Clone()
		if len(out) != 0 {
			t.Errorf("expected empty, got %d", len(out))
		}
	})

	t.Run("nil entries filtered", func(t *testing.T) {
		sl := SeriesList{testSeries(), nil, testSeries2()}
		out := sl.Clone()
		if len(out) != 2 {
			t.Errorf("expected 2, got %d", len(out))
		}
	})

	t.Run("deep copy independence", func(t *testing.T) {
		sl := SeriesList{testSeries()}
		out := sl.Clone()
		out[0].Header.Name = "mutated"
		if sl[0].Header.Name == "mutated" {
			t.Error("clone mutation affected original")
		}
	})
}

func TestSortByTags(t *testing.T) {
	t.Run("already sorted", func(t *testing.T) {
		// testSeries tags: test1=value1, testSeries2 tags: test2=value2, testSeries3 tags: test3=value3
		// sorted by tags+name: test (test1=value1), test2 (test2=value2), test3 (test3=value3)
		sl := SeriesList{testSeries(), testSeries2(), testSeries3()}
		sl.SortByTags()
		if sl[0].Header.Name != "test" || sl[1].Header.Name != "test2" || sl[2].Header.Name != "test3" {
			t.Errorf("unexpected order: %s, %s, %s", sl[0].Header.Name, sl[1].Header.Name, sl[2].Header.Name)
		}
	})

	t.Run("unsorted", func(t *testing.T) {
		sl := SeriesList{testSeries3(), testSeries(), testSeries2()}
		sl.SortByTags()
		if sl[0].Header.Name != "test" || sl[1].Header.Name != "test2" || sl[2].Header.Name != "test3" {
			t.Errorf("unexpected order: %s, %s, %s", sl[0].Header.Name, sl[1].Header.Name, sl[2].Header.Name)
		}
	})

	t.Run("with nil entries", func(t *testing.T) {
		sl := SeriesList{testSeries2(), nil, testSeries()}
		sl.SortByTags()
		// nils are skipped; only 2 entries should be placed
		if sl[0].Header.Name != "test" || sl[1].Header.Name != "test2" {
			t.Errorf("unexpected order: %s, %s", sl[0].Header.Name, sl[1].Header.Name)
		}
	})

	t.Run("empty vs non-empty tags are deterministic", func(t *testing.T) {
		sEmpty := &Series{Header: SeriesHeader{Name: "aaa", Tags: Tags{}}, Points: testPoints()}
		sTagged := &Series{Header: SeriesHeader{Name: "bbb", Tags: Tags{"z": "1"}}, Points: testPoints()}
		sl := SeriesList{sTagged, sEmpty}
		sl.SortByTags()
		sl2 := SeriesList{sEmpty, sTagged}
		sl2.SortByTags()
		if sl[0].Header.Name != sl2[0].Header.Name {
			t.Error("sort not deterministic")
		}
	})

	t.Run("same tags different names", func(t *testing.T) {
		s1 := &Series{Header: SeriesHeader{Name: "beta", Tags: Tags{"env": "prod"}}, Points: testPoints()}
		s2 := &Series{Header: SeriesHeader{Name: "alpha", Tags: Tags{"env": "prod"}}, Points: testPoints()}
		sl := SeriesList{s1, s2}
		sl.SortByTags()
		if sl[0].Header.Name != "alpha" || sl[1].Header.Name != "beta" {
			t.Errorf("expected alpha,beta got %s,%s", sl[0].Header.Name, sl[1].Header.Name)
		}
	})

	t.Run("all nil no panic", func(t *testing.T) {
		sl := SeriesList{nil, nil, nil}
		sl.SortByTags() // should not panic
	})
}

func TestSortPoints(t *testing.T) {
	s1 := testSeries()
	s1.Points = Points{
		{Epoch: epoch.Epoch(10 * timeseries.Second), Size: 27, Values: []any{1}},
		{Epoch: epoch.Epoch(5 * timeseries.Second), Size: 27, Values: []any{2}},
	}
	s2 := testSeries2()
	s2.Points = Points{
		{Epoch: epoch.Epoch(20 * timeseries.Second), Size: 27, Values: []any{3}},
		{Epoch: epoch.Epoch(1 * timeseries.Second), Size: 27, Values: []any{4}},
	}
	sl := SeriesList{s1, s2}
	sl.SortPoints()

	if !sort.IsSorted(sl[0].Points) {
		t.Error("series 0 points not sorted")
	}
	if !sort.IsSorted(sl[1].Points) {
		t.Error("series 1 points not sorted")
	}
}
