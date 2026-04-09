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

package timeseries

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"
)

var (
	t0    = time.Unix(0, 0)
	t98   = time.Unix(98, 0)
	t99   = time.Unix(99, 0)
	t100  = time.Unix(100, 0)
	t101  = time.Unix(101, 0)
	t200  = time.Unix(200, 0)
	t201  = time.Unix(201, 0)
	t300  = time.Unix(300, 0)
	t400  = time.Unix(400, 0) //lint:ignore U1000 - unused, but placeholder for future use
	t500  = time.Unix(500, 0)
	t600  = time.Unix(600, 0)
	t700  = time.Unix(700, 0)
	t800  = time.Unix(800, 0)
	t900  = time.Unix(900, 0)
	t1000 = time.Unix(1000, 0)
	t1100 = time.Unix(1100, 0)
	t1200 = time.Unix(1200, 0)
	t1300 = time.Unix(1300, 0)
	t1400 = time.Unix(1400, 0)
	t1500 = time.Unix(1500, 0)
	t1600 = time.Unix(1600, 0)
	t1700 = time.Unix(1700, 0)
	t1800 = time.Unix(1800, 0)
	t1900 = time.Unix(1900, 0)
)

func TestUpdateLastUsed(t *testing.T) {
	now := time.Now().Truncate(time.Second).Unix()

	tests := []struct {
		name     string
		el       ExtentListLRU
		lu       Extent
		step     time.Duration
		expected string
	}{
		{
			name:     "split into three",
			el:       ExtentListLRU{Extent{Start: t100, End: t1300, LastUsed: t1300}},
			lu:       Extent{Start: t200, End: t600},
			step:     time.Duration(100) * time.Second,
			expected: fmt.Sprintf("100-100:1300,200-600:%d,700-1300:1300", now),
		},
		{
			name: "encompass multiple extents",
			el: ExtentListLRU{
				Extent{Start: t100, End: t200, LastUsed: t200},
				Extent{Start: t600, End: t900, LastUsed: t900},
				Extent{Start: t1100, End: t1300, LastUsed: t900},
				Extent{Start: t1400, End: t1400, LastUsed: t1400},
			},
			lu:       Extent{Start: t1100, End: t1400},
			step:     time.Duration(100) * time.Second,
			expected: fmt.Sprintf("100-200:200,600-900:900,1100-1400:%d", now),
		},
		{
			name: "partial right overlap",
			el: ExtentListLRU{
				Extent{Start: t100, End: t200, LastUsed: t200},
				Extent{Start: t600, End: t900, LastUsed: t900},
				Extent{Start: t1100, End: t1300, LastUsed: t900},
				Extent{Start: t1400, End: t1400, LastUsed: t1400},
			},
			lu:       Extent{Start: t1200, End: t1400},
			step:     time.Duration(100) * time.Second,
			expected: fmt.Sprintf("100-200:200,600-900:900,1100-1100:900,1200-1400:%d", now),
		},
		{
			name: "exact extent match",
			el: ExtentListLRU{
				Extent{Start: t100, End: t200, LastUsed: t200},
				Extent{Start: t600, End: t900, LastUsed: t900},
				Extent{Start: t1100, End: t1300, LastUsed: t900},
				Extent{Start: t1400, End: t1400, LastUsed: t1400},
			},
			lu:       Extent{Start: t600, End: t900},
			step:     time.Duration(100) * time.Second,
			expected: fmt.Sprintf("100-200:200,600-900:%d,1100-1300:900,1400-1400:1400", now),
		},
		{
			name: "span across extents",
			el: ExtentListLRU{
				Extent{Start: t100, End: t200, LastUsed: t200},
				Extent{Start: t300, End: t900, LastUsed: t900},
				Extent{Start: t1000, End: t1300, LastUsed: t900},
				Extent{Start: t1400, End: t1400, LastUsed: t1400},
			},
			lu:       Extent{Start: t200, End: t1300},
			step:     time.Duration(100) * time.Second,
			expected: fmt.Sprintf("100-100:200,200-1300:%d,1400-1400:1400", now),
		},
		{
			name:     "nil list",
			el:       nil,
			lu:       Extent{Start: t200, End: t1300},
			step:     time.Duration(100) * time.Second,
			expected: "",
		},
		{
			name:     "empty list",
			el:       ExtentListLRU{},
			lu:       Extent{Start: t200, End: t1300},
			step:     time.Duration(100) * time.Second,
			expected: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			el := test.el.UpdateLastUsed(test.lu, test.step)
			if el.String() != test.expected {
				t.Errorf("got %s expected %s", el.String(), test.expected)
			}
		})
	}
}

func TestEncompasses(t *testing.T) {
	el := ExtentList{
		Extent{Start: t100, End: t200},
		Extent{Start: t600, End: t900},
		Extent{Start: t1100, End: t1300},
	}

	tests := []struct {
		name     string
		el       ExtentList
		extent   Extent
		expected bool
	}{
		{"single point inside", el, Extent{Start: t100, End: t100}, true},
		{"starts before list", el, Extent{Start: time.Unix(0, 0), End: t100}, false},
		{"zero extent before list", el, Extent{Start: time.Unix(0, 0), End: time.Unix(0, 0)}, false},
		{"between extents", el, Extent{Start: t201, End: t201}, true},
		{"after list end", el, Extent{Start: t1400, End: t1400}, false},
		{"empty list", ExtentList{}, Extent{Start: t100, End: t100}, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res := test.el.Encompasses(test.extent)
			if res != test.expected {
				t.Errorf("expected %t got %t", test.expected, res)
			}
		})
	}
}

func TestEncompassedBy(t *testing.T) {
	el := ExtentList{
		Extent{Start: t100, End: t200},
		Extent{Start: t600, End: t900},
		Extent{Start: t1100, End: t1300},
	}

	tests := []struct {
		name     string
		el       ExtentList
		extent   Extent
		expected bool
	}{
		{"narrower than first extent", el, Extent{Start: t100, End: t100}, false},
		{"before list start", el, Extent{Start: time.Unix(0, 0), End: t100}, false},
		{"zero extent", el, Extent{Start: time.Unix(0, 0), End: time.Unix(0, 0)}, false},
		{"between extents", el, Extent{Start: t201, End: t201}, false},
		{"after list end", el, Extent{Start: t1400, End: t1400}, false},
		{"empty list", ExtentList{}, Extent{Start: t100, End: t100}, false},
		{"exact match", el, Extent{Start: t100, End: t1300}, true},
		{"wider than list", el, Extent{Start: t0, End: t1900}, true},
		{"start match end wider", el, Extent{Start: t100, End: t1900}, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res := test.el.EncompassedBy(test.extent)
			if res != test.expected {
				t.Errorf("expected %t got %t", test.expected, res)
			}
		})
	}
}

func TestRemove(t *testing.T) {
	step := time.Second * 1

	tests := []struct {
		name     string
		el       ExtentList
		removals ExtentList
		expected ExtentList
	}{
		{ // remove exact extent
			"remove exact extent",
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			ExtentList{
				Extent{Start: t100, End: t200},
			},
			ExtentList{
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{
			name: "trim start",
			el: ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			removals: ExtentList{
				Extent{Start: t100, End: t100},
			},
			expected: ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{
			name: "trim end",
			el: ExtentList{
				Extent{Start: t100, End: t201},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			removals: ExtentList{
				Extent{Start: t201, End: t201},
			},
			expected: ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{
			name: "trim both ends",
			el: ExtentList{
				Extent{Start: t100, End: t201},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			removals: ExtentList{
				Extent{Start: t100, End: t100},
				Extent{Start: t201, End: t201},
			},
			expected: ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{
			name: "split middle",
			el: ExtentList{
				Extent{Start: t100, End: t201},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			removals: ExtentList{
				Extent{Start: t101, End: t200},
			},
			expected: ExtentList{
				Extent{Start: t100, End: t100},
				Extent{Start: t201, End: t201},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{
			name: "remove wider than extent",
			el: ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			removals: ExtentList{
				Extent{Start: t100, End: t200},
			},
			expected: ExtentList{
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{
			name: "remove fully enclosing",
			el: ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			removals: ExtentList{
				Extent{Start: t100, End: t201},
			},
			expected: ExtentList{
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{
			name: "empty removals",
			el: ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			removals: ExtentList{},
			expected: ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{
			name: "empty subject",
			el:   ExtentList{},
			removals: ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			expected: ExtentList{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			v := test.el.Remove(test.removals, step)
			if !slices.Equal(v, test.expected) {
				t.Errorf("expected %v got %v", test.expected, v)
			}
		})
	}
}

func TestOutsideOf(t *testing.T) {
	el := ExtentList{
		Extent{Start: t100, End: t200},
		Extent{Start: t600, End: t900},
		Extent{Start: t1100, End: t1300},
	}

	tests := []struct {
		name     string
		el       ExtentList
		extent   Extent
		expected bool
	}{
		{"inside at start", el, Extent{Start: t100, End: t100}, false},
		{"overlaps start", el, Extent{Start: time.Unix(0, 0), End: t100}, false},
		{"entirely before list", el, Extent{Start: time.Unix(0, 0), End: time.Unix(0, 0)}, true},
		{"between extents", el, Extent{Start: t201, End: t201}, false},
		{"after list end", el, Extent{Start: t1400, End: t1400}, true},
		{"empty list", ExtentList{}, Extent{Start: t100, End: t100}, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if res := test.el.OutsideOf(test.extent); res != test.expected {
				t.Errorf("expected %t got %t", test.expected, res)
			}
		})
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		name     string
		el       ExtentList
		expected string
	}{
		{
			"multiple extents",
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			"100000-200000,600000-900000,1100000-1300000",
		},
		{
			"empty list",
			ExtentList{},
			"",
		},
		{
			"single extent",
			ExtentList{
				Extent{Start: t100, End: t200},
			},
			"100000-200000",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.el.String() != test.expected {
				t.Errorf("got %s expected %s", test.el.String(), test.expected)
			}
		})
	}
}

func TestCloneRange(t *testing.T) {
	el := ExtentList{
		Extent{Start: t600, End: t900},
	}

	res := el.CloneRange(-1, -1)
	if len(res) != 0 {
		t.Error("expected zero-length result", res)
	}

	res = el.CloneRange(0, 200)
	if len(res) != 0 {
		t.Error("expected zero-length result", res)
	}

	el = ExtentList{
		Extent{Start: t100, End: t200},
		Extent{Start: t600, End: t900},
		Extent{Start: t1100, End: t1300},
	}

	res = el.CloneRange(1, 3)
	if len(res) != 2 {
		t.Error("expected 2 got", len(res))
	}
}

func TestCrop(t *testing.T) {
	el := ExtentList{
		Extent{Start: t100, End: t200},
		Extent{Start: t600, End: t900},
		Extent{Start: t1100, End: t1300},
	}

	tests := []struct {
		name           string
		cropRange      Extent
		seed, expected ExtentList
	}{
		{"wider before start", Extent{Start: t98, End: t1300}, el.Clone(), ExtentList{
			Extent{Start: t100, End: t200}, Extent{Start: t600, End: t900}, Extent{Start: t1100, End: t1300},
		}},
		{"exact range", Extent{Start: t100, End: t1300}, el.Clone(), ExtentList{
			Extent{Start: t100, End: t200}, Extent{Start: t600, End: t900}, Extent{Start: t1100, End: t1300},
		}},
		{"start inside first", Extent{Start: t101, End: t1300}, el.Clone(), ExtentList{
			Extent{Start: t101, End: t200}, Extent{Start: t600, End: t900}, Extent{Start: t1100, End: t1300},
		}},
		{"start at first end", Extent{Start: t200, End: t1300}, el.Clone(), ExtentList{
			Extent{Start: t200, End: t200}, Extent{Start: t600, End: t900}, Extent{Start: t1100, End: t1300},
		}},
		{"start after first end", Extent{Start: t201, End: t1300}, el.Clone(), ExtentList{
			Extent{Start: t600, End: t900}, Extent{Start: t1100, End: t1300},
		}},
		{"end inside last", Extent{Start: t99, End: t1200}, el.Clone(), ExtentList{
			Extent{Start: t100, End: t200}, Extent{Start: t600, End: t900}, Extent{Start: t1100, End: t1200},
		}},
		{"exact start end inside last", Extent{Start: t100, End: t1200}, el.Clone(), ExtentList{
			Extent{Start: t100, End: t200}, Extent{Start: t600, End: t900}, Extent{Start: t1100, End: t1200},
		}},
		{"inside first end inside last", Extent{Start: t101, End: t1200}, el.Clone(), ExtentList{
			Extent{Start: t101, End: t200}, Extent{Start: t600, End: t900}, Extent{Start: t1100, End: t1200},
		}},
		{"at first end to inside last", Extent{Start: t200, End: t1200}, el.Clone(), ExtentList{
			Extent{Start: t200, End: t200}, Extent{Start: t600, End: t900}, Extent{Start: t1100, End: t1200},
		}},
		{"after first to inside last", Extent{Start: t201, End: t1200}, el.Clone(), ExtentList{
			Extent{Start: t600, End: t900}, Extent{Start: t1100, End: t1200},
		}},
		{"entirely before data", Extent{Start: t98, End: t98}, el.Clone(), ExtentList{}},
		{"just before first extent", Extent{Start: t98, End: t99}, el.Clone(), ExtentList{}},
		{"touching first start", Extent{Start: t98, End: t100}, el.Clone(), ExtentList{
			Extent{Start: t100, End: t100},
		}},
		{"one past first start", Extent{Start: t98, End: t101}, el.Clone(), ExtentList{
			Extent{Start: t100, End: t101},
		}},
		{"before to first end", Extent{Start: t98, End: t200}, el.Clone(), ExtentList{
			Extent{Start: t100, End: t200},
		}},
		{"exact first extent", Extent{Start: t100, End: t200}, el.Clone(), ExtentList{
			Extent{Start: t100, End: t200},
		}},
		{"first start to inside first", Extent{Start: t100, End: t101}, el.Clone(), ExtentList{
			Extent{Start: t100, End: t101},
		}},
		{"gap before last", Extent{Start: t1000, End: t1300}, el.Clone(), ExtentList{
			Extent{Start: t1100, End: t1300},
		}},
		{"exact last extent", Extent{Start: t1100, End: t1300}, el.Clone(), ExtentList{
			Extent{Start: t1100, End: t1300},
		}},
		{"inside last to last end", Extent{Start: t1200, End: t1300}, el.Clone(), ExtentList{
			Extent{Start: t1200, End: t1300},
		}},
		{"single point last end", Extent{Start: t1300, End: t1300}, el.Clone(), ExtentList{
			Extent{Start: t1300, End: t1300},
		}},
		{"last end to after", Extent{Start: t1300, End: t1400}, el.Clone(), ExtentList{
			Extent{Start: t1300, End: t1300},
		}},
		{"inside last to after", Extent{Start: t1200, End: t1400}, el.Clone(), ExtentList{
			Extent{Start: t1200, End: t1300},
		}},
		{"gap to after last", Extent{Start: t1000, End: t1400}, el.Clone(), ExtentList{
			Extent{Start: t1100, End: t1300},
		}},
		{"second end to after last", Extent{Start: t900, End: t1400}, el.Clone(), ExtentList{
			Extent{Start: t900, End: t900}, Extent{Start: t1100, End: t1300},
		}},
		{"full superset", Extent{Start: t98, End: t1400}, el.Clone(), ExtentList{
			Extent{Start: t100, End: t200}, Extent{Start: t600, End: t900}, Extent{Start: t1100, End: t1300},
		}},
		{"empty seed", Extent{Start: t98, End: t1400}, ExtentList{}, ExtentList{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.seed.Clone().Crop(test.cropRange)
			if !reflect.DeepEqual(test.expected, result) {
				t.Errorf("mismatch in Crop: expected=%s got=%s", test.expected, result)
			}
		})
	}
}

func TestExtentListLRUSort(t *testing.T) {
	el := ExtentListLRU{
		Extent{Start: t600, End: t900, LastUsed: t900},
		Extent{Start: t100, End: t200, LastUsed: t200},
		Extent{Start: t1100, End: t1300, LastUsed: t1100},
	}
	el2 := ExtentListLRU{
		Extent{Start: t100, End: t200, LastUsed: t200},
		Extent{Start: t600, End: t900, LastUsed: t900},
		Extent{Start: t1100, End: t1300, LastUsed: t1100},
	}
	sort.Sort(el)
	if !reflect.DeepEqual(el, el2) {
		t.Errorf("mismatch in sort: expected=%s got=%s", el2, el)
	}
}

func TestExtentListLRUCopy(t *testing.T) {
	el := ExtentListLRU{
		Extent{Start: t100, End: t200, LastUsed: t200},
		Extent{Start: t600, End: t900, LastUsed: t900},
		Extent{Start: t1100, End: t1300, LastUsed: t1100},
	}

	el2 := el.Clone()

	if !reflect.DeepEqual(el, el2) {
		t.Errorf("mismatch in sort: expected=%s got=%s", el2, el)
	}
}

func TestCompress(t *testing.T) {
	tests := []struct {
		name                     string
		uncompressed, compressed ExtentList
	}{
		{"empty list", ExtentList{}, ExtentList{}},
		{
			"adjacent extents merge with gap",
			ExtentList{
				Extent{Start: time.Unix(30, 0), End: time.Unix(30, 0)},
				Extent{Start: time.Unix(90, 0), End: time.Unix(120, 0)},
				Extent{Start: time.Unix(120, 0), End: time.Unix(180, 0)},
				Extent{Start: time.Unix(180, 0), End: time.Unix(210, 0)},
			},
			ExtentList{
				Extent{Start: time.Unix(30, 0), End: time.Unix(30, 0)},
				Extent{Start: time.Unix(90, 0), End: time.Unix(210, 0)},
			},
		},
		{
			"single extent unchanged",
			ExtentList{
				Extent{Start: time.Unix(0, 0), End: time.Unix(30, 0)},
			},
			ExtentList{
				Extent{Start: time.Unix(0, 0), End: time.Unix(30, 0)},
			},
		},
		{
			"unsorted with gaps",
			ExtentList{
				Extent{Start: time.Unix(0, 0), End: time.Unix(30, 0)},
				Extent{Start: time.Unix(90, 0), End: time.Unix(120, 0)},
				Extent{Start: time.Unix(120, 0), End: time.Unix(180, 0)},
				Extent{Start: time.Unix(270, 0), End: time.Unix(360, 0)},
				Extent{Start: time.Unix(180, 0), End: time.Unix(210, 0)},
				Extent{Start: time.Unix(420, 0), End: time.Unix(480, 0)},
			},
			ExtentList{
				Extent{Start: time.Unix(0, 0), End: time.Unix(30, 0)},
				Extent{Start: time.Unix(90, 0), End: time.Unix(210, 0)},
				Extent{Start: time.Unix(270, 0), End: time.Unix(360, 0)},
				Extent{Start: time.Unix(420, 0), End: time.Unix(480, 0)},
			},
		},
		{
			"duplicates compressed",
			ExtentList{
				Extent{Start: time.Unix(90, 0), End: time.Unix(120, 0)},
				Extent{Start: time.Unix(90, 0), End: time.Unix(120, 0)},
				Extent{Start: time.Unix(180, 0), End: time.Unix(180, 0)},
				Extent{Start: time.Unix(180, 0), End: time.Unix(180, 0)},
			},
			ExtentList{
				Extent{Start: time.Unix(90, 0), End: time.Unix(120, 0)},
				Extent{Start: time.Unix(180, 0), End: time.Unix(180, 0)},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.uncompressed.Compress(time.Duration(30) * time.Second)

			if !reflect.DeepEqual(result, test.compressed) {
				t.Errorf("mismatch in Compress: expected=%s got=%s", test.compressed, result)
			}
		})
	}
}

func TestSize(t *testing.T) {
	el := ExtentList{
		Extent{Start: time.Unix(90, 0), End: time.Unix(120, 0)},
		Extent{Start: time.Unix(90, 0), End: time.Unix(120, 0)},
		Extent{Start: time.Unix(180, 0), End: time.Unix(180, 0)},
		Extent{Start: time.Unix(180, 0), End: time.Unix(180, 0)},
	}

	expected := 288
	if el.Size() != expected {
		t.Errorf("expected %d got %d", expected, el.Size())
	}
}

func TestCalculateDeltas(t *testing.T) {
	tests := []struct {
		name                 string
		have                 []Extent
		expected             []Extent
		start, end, stepSecs int64
	}{
		{
			"empty have needs full range",
			[]Extent{},
			[]Extent{{Start: time.Unix(1, 0), End: time.Unix(100, 0)}},
			1, 100, 1,
		},
		{
			"partial have needs prefix",
			[]Extent{{Start: time.Unix(50, 0), End: time.Unix(100, 0)}},
			[]Extent{{Start: time.Unix(1, 0), End: time.Unix(49, 0)}},
			1, 100, 1,
		},
		{
			"partial have needs prefix and suffix",
			[]Extent{{Start: time.Unix(50, 0), End: time.Unix(100, 0)}},
			[]Extent{
				{Start: time.Unix(1, 0), End: time.Unix(49, 0)},
				{Start: time.Unix(101, 0), End: time.Unix(101, 0)},
			},
			1, 101, 1,
		},
		{
			"full have needs suffix only",
			[]Extent{{Start: time.Unix(1, 0), End: time.Unix(100, 0)}},
			[]Extent{{Start: time.Unix(101, 0), End: time.Unix(101, 0)}},
			1, 101, 1,
		},
		{
			"have completely covers need",
			[]Extent{{Start: time.Unix(1, 0), End: time.Unix(200, 0)}},
			[]Extent{},
			50, 100, 1,
		},
		{
			"multiple haves with gap returns only the gap",
			[]Extent{
				{Start: time.Unix(1, 0), End: time.Unix(40, 0)},
				{Start: time.Unix(60, 0), End: time.Unix(100, 0)},
			},
			[]Extent{{Start: time.Unix(41, 0), End: time.Unix(59, 0)}},
			1, 100, 1,
		},
		{
			"need entirely before all haves",
			[]Extent{{Start: time.Unix(200, 0), End: time.Unix(300, 0)}},
			[]Extent{{Start: time.Unix(1, 0), End: time.Unix(100, 0)}},
			1, 100, 1,
		},
		{
			"need entirely after all haves",
			[]Extent{{Start: time.Unix(1, 0), End: time.Unix(50, 0)}},
			[]Extent{{Start: time.Unix(100, 0), End: time.Unix(200, 0)}},
			100, 200, 1,
		},
		{
			"adjacent haves with no gap",
			[]Extent{
				{Start: time.Unix(1, 0), End: time.Unix(50, 0)},
				{Start: time.Unix(51, 0), End: time.Unix(100, 0)},
			},
			[]Extent{},
			1, 100, 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := ExtentList(test.have).CalculateDeltas(
				ExtentList{{Start: time.Unix(test.start, 0), End: time.Unix(test.end, 0)}},
				time.Duration(test.stepSecs)*time.Second,
			)
			if len(d) != len(test.expected) {
				t.Errorf("expected %v got %v", test.expected, d)
				return
			}
			for i := range d {
				if d[i].Start != test.expected[i].Start {
					t.Errorf("expected %d got %d", test.expected[i].Start.Unix(), d[i].Start.Unix())
				}
				if d[i].End != test.expected[i].End {
					t.Errorf("expected %d got %d", test.expected[i].End.Unix(), d[i].End.Unix())
				}
			}
		})
	}
}

func TestTimestampCount(t *testing.T) {
	tests := []struct {
		name     string
		el       ExtentList
		step     time.Duration
		expected int64
	}{
		{
			"multiple extents with zero extent",
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{},
				Extent{Start: t1100, End: t1300},
			},
			time.Second * 100,
			9,
		},
		{"empty list", ExtentList{}, time.Second * 100, 0},
		{"single point extent", ExtentList{Extent{Start: t100, End: t100}}, time.Second * 100, 1},
		{"only zero extents", ExtentList{Extent{}, Extent{}}, time.Second * 100, 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if v := test.el.TimestampCount(test.step); v != test.expected {
				t.Errorf("expected %d got %d", test.expected, v)
			}
		})
	}
}

func TestSplice(t *testing.T) {
	tests := []struct {
		name                       string
		el, expected               ExtentList
		step, maxRange, spliceStep time.Duration
		maxPoints                  int
	}{
		{ // spliceByPoints basic
			name: "spliceByPoints basic",
			el: ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{},
				Extent{Start: t1100, End: t1300},
			},
			expected: ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t700},
				Extent{Start: t800, End: t900},
				Extent{},
				Extent{Start: t1100, End: t1200},
				Extent{Start: t1300, End: t1300},
			},
			step:      time.Second * 100,
			maxPoints: 2,
		},
		{ // spliceByTime fast fail
			name: "spliceByTime fast fail",
			el: ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t700},
				Extent{Start: t800, End: t900},
				Extent{},
				Extent{Start: t1100, End: t1200},
				Extent{Start: t1300, End: t1300},
			},
			expected: ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t700},
				Extent{Start: t800, End: t900},
				Extent{},
				Extent{Start: t1100, End: t1200},
				Extent{Start: t1300, End: t1300},
			},
		},
		{
			name:     "empty list",
			el:       ExtentList{},
			expected: ExtentList{},
		},
		{
			name:     "nil list",
			el:       nil,
			expected: nil,
		},
		{ // spliceByTimeAligned fast fail
			name: "spliceByTimeAligned fast fail",
			el: ExtentList{
				Extent{Start: t100, End: t200},
			},
			expected: ExtentList{
				Extent{Start: t100, End: t200},
			},
			step:       time.Second * 100,
			spliceStep: time.Second * 100,
		},
		{
			name: "spliceByPoints fast fail",
			el: ExtentList{
				Extent{Start: t100, End: t200},
			},
			expected: ExtentList{
				Extent{Start: t100, End: t200},
			},
			maxPoints: 2,
		},
		{
			name: "spliceByTimeAligned left-side only",
			el: ExtentList{
				Extent{Start: t0, End: t200},
				Extent{Start: t500, End: t600},
				Extent{Start: t900, End: t1600},
			},
			expected: ExtentList{
				Extent{Start: t0, End: t200},
				Extent{Start: t500, End: t500},
				Extent{Start: t600, End: t600},
				Extent{Start: t900, End: t1100},
				Extent{Start: t1200, End: t1600},
			},
			step:       time.Second * 100,
			maxRange:   time.Second * 600,
			spliceStep: time.Second * 600,
		},
		{
			name: "spliceByTimeAligned left and right",
			el: ExtentList{
				Extent{Start: t0, End: t200},
				Extent{Start: t500, End: t600},
				Extent{Start: t900, End: t1900},
			},
			expected: ExtentList{
				Extent{Start: t0, End: t200},
				Extent{Start: t500, End: t500},
				Extent{Start: t600, End: t600},
				Extent{Start: t900, End: t1100},
				Extent{Start: t1200, End: t1700},
				Extent{Start: t1800, End: t1900},
			},
			step:       time.Second * 100,
			maxRange:   time.Second * 600,
			spliceStep: time.Second * 600,
		},
		{
			name: "spliceByTime basic",
			el: ExtentList{
				Extent{Start: t0, End: t200},
				Extent{Start: t500, End: t600},
				Extent{Start: t900, End: t1900},
			},
			expected: ExtentList{
				Extent{Start: t0, End: t200},
				Extent{Start: t500, End: t600},
				Extent{Start: t900, End: t1400},
				Extent{Start: t1500, End: t1900},
			},
			step:     time.Second * 100,
			maxRange: time.Second * 600,
		},
		{
			name: "spliceByTimeAligned spliceStep lt maxRange",
			el: ExtentList{
				Extent{Start: t0, End: t200},
				Extent{Start: t500, End: t600},
				Extent{Start: t900, End: t1900},
			},
			expected: ExtentList{
				Extent{Start: t0, End: t200},
				Extent{Start: t500, End: t500},
				Extent{Start: t600, End: t600},
				Extent{Start: t900, End: t1400},
				Extent{Start: t1500, End: t1900},
			},
			step:       time.Second * 100,
			maxRange:   time.Second * 600,
			spliceStep: time.Second * 300,
		},
		{
			name: "spliceByTimeAligned step gt spliceStep",
			el: ExtentList{
				Extent{Start: t0, End: t0},
				Extent{Start: t300, End: t600},
				Extent{Start: t900, End: t1800},
			},
			expected: ExtentList{
				Extent{Start: t0, End: t0},
				Extent{Start: t300, End: t600},
				Extent{Start: t900, End: t1200},
				Extent{Start: t1500, End: t1800},
			},
			step:       time.Second * 300,
			maxRange:   time.Second * 600,
			spliceStep: time.Second * 100,
		},
		{
			name: "spliceByTimeAligned step gt maxRange",
			el: ExtentList{
				Extent{Start: t0, End: t600},
				Extent{Start: t1200, End: t1800},
			},
			expected: ExtentList{
				Extent{Start: t0, End: t0},
				Extent{Start: t600, End: t600},
				Extent{Start: t1200, End: t1200},
				Extent{Start: t1800, End: t1800},
			},
			step:       time.Second * 600,
			maxRange:   time.Second * 400,
			spliceStep: time.Second * 200,
		},
		{
			name: "spliceByPoints step gt maxPoints spread",
			el: ExtentList{
				Extent{Start: t0, End: t1800},
			},
			expected: ExtentList{
				Extent{Start: t0, End: t0},
				Extent{Start: t600, End: t600},
				Extent{Start: t1200, End: t1200},
				Extent{Start: t1800, End: t1800},
			},
			step:      time.Second * 600,
			maxPoints: 1,
		},
		{
			name: "spliceByTime step gt maxRange",
			el: ExtentList{
				Extent{Start: t0, End: t1800},
			},
			expected: ExtentList{
				Extent{Start: t0, End: t0},
				Extent{Start: t600, End: t600},
				Extent{Start: t1200, End: t1200},
				Extent{Start: t1800, End: t1800},
			},
			step:     time.Second * 600,
			maxRange: 100,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := test.el.Splice(test.step, test.maxRange, test.spliceStep, test.maxPoints)
			if out == nil && test.expected == nil {
				return
			}
			if (test.expected == nil && out != nil) ||
				(out == nil && test.expected != nil) ||
				(!slices.Equal(out, test.expected)) {
				t.Errorf("expected %s\ngot      %s", test.expected, out)
			}
		})
	}
}

func TestExtentListMerge(t *testing.T) {
	tests := []struct {
		name     string
		el1, el2 ExtentList
		step     time.Duration
		expected ExtentList
	}{
		{
			name:     "both empty",
			el1:      ExtentList{},
			el2:      ExtentList{},
			step:     time.Second * 10,
			expected: ExtentList{},
		},
		{
			name:     "el1 empty",
			el1:      ExtentList{},
			el2:      ExtentList{Extent{Start: t100, End: t200}},
			step:     time.Second * 10,
			expected: ExtentList{Extent{Start: t100, End: t200}},
		},
		{
			name:     "el2 empty",
			el1:      ExtentList{Extent{Start: t100, End: t200}},
			el2:      ExtentList{},
			step:     time.Second * 10,
			expected: ExtentList{Extent{Start: t100, End: t200}},
		},
		{
			name:     "disjoint",
			el1:      ExtentList{Extent{Start: t100, End: t200}},
			el2:      ExtentList{Extent{Start: t600, End: t900}},
			step:     time.Second * 100,
			expected: ExtentList{Extent{Start: t100, End: t200}, Extent{Start: t600, End: t900}},
		},
		{
			name:     "overlapping",
			el1:      ExtentList{Extent{Start: t100, End: t500}},
			el2:      ExtentList{Extent{Start: t300, End: t700}},
			step:     time.Second * 100,
			expected: ExtentList{Extent{Start: t100, End: t700}},
		},
		{
			name:     "adjacent at step boundary",
			el1:      ExtentList{Extent{Start: t100, End: t200}},
			el2:      ExtentList{Extent{Start: t300, End: t500}},
			step:     time.Second * 100,
			expected: ExtentList{Extent{Start: t100, End: t500}},
		},
		{
			name:     "gap larger than step",
			el1:      ExtentList{Extent{Start: t100, End: t200}},
			el2:      ExtentList{Extent{Start: t500, End: t600}},
			step:     time.Second * 100,
			expected: ExtentList{Extent{Start: t100, End: t200}, Extent{Start: t500, End: t600}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := test.el1.Merge(test.el2, test.step)
			if !slices.Equal(out, test.expected) {
				t.Errorf("expected %s got %s", test.expected, out)
			}
		})
	}
}

func TestExtentListClone(t *testing.T) {
	t.Run("clone equals original", func(t *testing.T) {
		el := ExtentList{
			Extent{Start: t100, End: t200},
			Extent{Start: t600, End: t900},
		}
		c := el.Clone()
		if !slices.Equal(el, c) {
			t.Errorf("expected %s got %s", el, c)
		}
	})

	t.Run("mutating clone does not affect original", func(t *testing.T) {
		el := ExtentList{
			Extent{Start: t100, End: t200},
			Extent{Start: t600, End: t900},
		}
		c := el.Clone()
		c[0].Start = t0
		if el[0].Start.Equal(t0) {
			t.Error("clone mutation affected original")
		}
	})

	t.Run("empty list", func(t *testing.T) {
		el := ExtentList{}
		c := el.Clone()
		if len(c) != 0 {
			t.Errorf("expected empty, got %d", len(c))
		}
	})
}

func genBenchmarkExtentList(len int, step time.Duration) (el ExtentList, start, end time.Time) {
	end = time.Now()
	start = end.Add(time.Duration(-1*len) * step)
	el = make(ExtentList, len)
	for i := range len {
		el[i] = Extent{
			Start: start.Add(time.Duration(i) * step),
			End:   start.Add(time.Duration(i+1) * step),
		}
	}
	return
}

var res any

func BenchmarkCalculateDeltas(b *testing.B) {
	bmTimeStep := time.Duration(10)
	haves := make([]ExtentList, b.N)
	wants := make([]Extent, b.N)
	for i := 0; i < b.N; i++ {
		el, s, e := genBenchmarkExtentList(10, bmTimeStep)
		haves[i] = el
		wants[i] = Extent{
			Start: s.Add(2 * bmTimeStep),
			End:   e.Add(-2 * bmTimeStep),
		}
	}
	b.ResetTimer()
	var r ExtentList
	for i := 0; i < b.N; i++ {
		r = haves[i].CalculateDeltas(ExtentList{wants[i]}, bmTimeStep)
	}
	res = r
}

func BenchmarkCrop(b *testing.B) {
	bmTimeStep := time.Duration(10)
	haves := make([]ExtentList, b.N)
	wants := make([]Extent, b.N)
	for i := 0; i < b.N; i++ {
		el, s, e := genBenchmarkExtentList(10, bmTimeStep)
		haves[i] = el
		wants[i] = Extent{
			Start: s.Add(2 * bmTimeStep),
			End:   e.Add(-2 * bmTimeStep),
		}
	}
	b.ResetTimer()
	var r ExtentList
	for i := 0; i < b.N; i++ {
		r = haves[i].Crop(wants[i])
	}
	res = r
}

func BenchmarkCompress(b *testing.B) {
	bmTimeStep := time.Duration(10)
	haves := make([]ExtentList, b.N)
	for i := 0; i < b.N; i++ {
		el, _, _ := genBenchmarkExtentList(10, bmTimeStep)
		haves[i] = el
	}
	b.ResetTimer()
	var r ExtentList
	for i := 0; i < b.N; i++ {
		r = haves[i].Compress(bmTimeStep)
	}
	res = r
}
