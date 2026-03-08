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
