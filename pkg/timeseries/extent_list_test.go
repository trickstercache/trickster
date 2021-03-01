/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"sort"
	"strconv"
	"testing"
	"time"
)

var t98 = time.Unix(98, 0)
var t99 = time.Unix(99, 0)
var t100 = time.Unix(100, 0)
var t101 = time.Unix(101, 0)
var t200 = time.Unix(200, 0)
var t201 = time.Unix(201, 0)
var t300 = time.Unix(300, 0)
var t600 = time.Unix(600, 0)
var t900 = time.Unix(900, 0)
var t1000 = time.Unix(1000, 0)
var t1100 = time.Unix(1100, 0)
var t1200 = time.Unix(1200, 0)
var t1300 = time.Unix(1300, 0)
var t1400 = time.Unix(1400, 0)

func TestUpdateLastUsed(t *testing.T) {

	now := time.Now().Truncate(time.Second).Unix()

	tests := []struct {
		el       ExtentListLRU
		lu       Extent
		step     time.Duration
		expected string
	}{
		{ // Run 0 - split 1 into 3
			el:       ExtentListLRU{Extent{Start: t100, End: t1300, LastUsed: t1300}},
			lu:       Extent{Start: t200, End: t600},
			step:     time.Duration(100) * time.Second,
			expected: fmt.Sprintf("100-100:1300,200-600:%d,700-1300:1300", now),
		},

		{
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
			el:       nil,
			lu:       Extent{Start: t200, End: t1300},
			step:     time.Duration(100) * time.Second,
			expected: "",
		},

		{
			el:       ExtentListLRU{},
			lu:       Extent{Start: t200, End: t1300},
			step:     time.Duration(100) * time.Second,
			expected: "",
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			el := test.el.UpdateLastUsed(test.lu, test.step)
			if el.String() != test.expected {
				t.Errorf("got %s expected %s", el.String(), test.expected)
			}
		})
	}

}

func TestInsideOf(t *testing.T) {

	el := ExtentList{
		Extent{Start: t100, End: t200},
		Extent{Start: t600, End: t900},
		Extent{Start: t1100, End: t1300},
	}

	if el.InsideOf(Extent{Start: t100, End: t100}) {
		t.Errorf("expected false got %t", true)
	}

	if el.InsideOf(Extent{Start: time.Unix(0, 0), End: t100}) {
		t.Errorf("expected false got %t", true)
	}

	if el.InsideOf(Extent{Start: time.Unix(0, 0), End: time.Unix(0, 0)}) {
		t.Errorf("expected false got %t", true)
	}

	if el.InsideOf(Extent{Start: t201, End: t201}) {
		t.Errorf("expected false got %t", true)
	}

	if el.InsideOf(Extent{Start: t1400, End: t1400}) {
		t.Errorf("expected false got %t", true)
	}

	// test empty
	el = ExtentList{}
	if el.InsideOf(Extent{Start: t100, End: t100}) {
		t.Errorf("expected false got %t", true)
	}

}

func TestOutsideOf(t *testing.T) {

	el := ExtentList{
		Extent{Start: t100, End: t200},
		Extent{Start: t600, End: t900},
		Extent{Start: t1100, End: t1300},
	}

	if el.OutsideOf(Extent{Start: t100, End: t100}) {
		t.Errorf("expected false got %t", true)
	}

	if el.OutsideOf(Extent{Start: time.Unix(0, 0), End: t100}) {
		t.Errorf("expected false got %t", true)
	}

	if !el.OutsideOf(Extent{Start: time.Unix(0, 0), End: time.Unix(0, 0)}) {
		t.Errorf("expected true got %t", false)
	}

	if el.OutsideOf(Extent{Start: t201, End: t201}) {
		t.Errorf("expected false got %t", true)
	}

	if !el.OutsideOf(Extent{Start: t1400, End: t1400}) {
		t.Errorf("expected true got %t", false)
	}

	// test empty
	el = ExtentList{}
	if !el.OutsideOf(Extent{Start: t100, End: t100}) {
		t.Errorf("expected true got %t", false)
	}
}

func TestString(t *testing.T) {

	tests := []struct {
		el       ExtentList
		expected string
	}{
		{ // 0
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			"100000-200000,600000-900000,1100000-1300000",
		},

		{ // 1
			ExtentList{},
			"",
		},

		{ // 2
			ExtentList{
				Extent{Start: t100, End: t200},
			},
			"100000-200000",
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			if test.el.String() != test.expected {
				t.Errorf("got %s expected %s", test.el.String(), test.expected)
			}
		})
	}

}

func TestCrop(t *testing.T) {

	el := ExtentList{
		Extent{Start: t100, End: t200},
		Extent{Start: t600, End: t900},
		Extent{Start: t1100, End: t1300},
	}

	tests := []struct {
		cropRange      Extent
		seed, expected ExtentList
	}{

		{ // Run 0
			Extent{Start: t98, End: t1300},
			el.Clone(),
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 1
			Extent{Start: t100, End: t1300},
			el.Clone(),
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 2
			Extent{Start: t101, End: t1300},
			el.Clone(),
			ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 3
			Extent{Start: t200, End: t1300},
			el.Clone(),
			ExtentList{
				Extent{Start: t200, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 4
			Extent{Start: t201, End: t1300},
			el.Clone(),
			ExtentList{
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 5
			Extent{Start: t99, End: t1200},
			el.Clone(),
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1200},
			},
		},

		{ // Run 6
			Extent{Start: t100, End: t1200},
			el.Clone(),
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1200},
			},
		},

		{ // Run 7
			Extent{Start: t101, End: t1200},
			el.Clone(),
			ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1200},
			},
		},

		{ // Run 8
			Extent{Start: t200, End: t1200},
			el.Clone(),
			ExtentList{
				Extent{Start: t200, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1200},
			},
		},

		{ // Run 9
			Extent{Start: t201, End: t1200},
			el.Clone(),
			ExtentList{
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1200},
			},
		},

		{ // Run 10
			Extent{Start: t98, End: t98},
			el.Clone(),
			ExtentList{},
		},

		{ // Run 11
			Extent{Start: t98, End: t99},
			el.Clone(),
			ExtentList{},
		},

		{ // Run 12
			Extent{Start: t98, End: t100},
			el.Clone(),
			ExtentList{
				Extent{Start: t100, End: t100},
			},
		},

		{ // Run 13
			Extent{Start: t98, End: t101},
			el.Clone(),
			ExtentList{
				Extent{Start: t100, End: t101},
			},
		},

		{ // Run 14
			Extent{Start: t98, End: t200},
			el.Clone(),
			ExtentList{
				Extent{Start: t100, End: t200},
			},
		},

		{ // Run 15
			Extent{Start: t100, End: t200},
			el.Clone(),
			ExtentList{
				Extent{Start: t100, End: t200},
			},
		},

		{ // Run 16
			Extent{Start: t100, End: t101},
			el.Clone(),
			ExtentList{
				Extent{Start: t100, End: t101},
			},
		},

		{ // Run 17
			Extent{Start: t1000, End: t1300},
			el.Clone(),
			ExtentList{
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 18
			Extent{Start: t1100, End: t1300},
			el.Clone(),
			ExtentList{
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 19
			Extent{Start: t1200, End: t1300},
			el.Clone(),
			ExtentList{
				Extent{Start: t1200, End: t1300},
			},
		},

		{ // Run 20
			Extent{Start: t1300, End: t1300},
			el.Clone(),
			ExtentList{
				Extent{Start: t1300, End: t1300},
			},
		},

		{ // Run 21
			Extent{Start: t1300, End: t1400},
			el.Clone(),
			ExtentList{
				Extent{Start: t1300, End: t1300},
			},
		},

		{ // Run 22
			Extent{Start: t1200, End: t1400},
			el.Clone(),
			ExtentList{
				Extent{Start: t1200, End: t1300},
			},
		},

		{ // Run 23
			Extent{Start: t1000, End: t1400},
			el.Clone(),
			ExtentList{
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 24
			Extent{Start: t900, End: t1400},
			el.Clone(),
			ExtentList{
				Extent{Start: t900, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 25
			Extent{Start: t98, End: t1400},
			el.Clone(),
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 26
			Extent{Start: t98, End: t1400},
			ExtentList{},
			ExtentList{},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
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
		uncompressed, compressed ExtentList
	}{
		{
			ExtentList{},
			ExtentList{},
		},

		{
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
			ExtentList{
				Extent{Start: time.Unix(0, 0), End: time.Unix(30, 0)},
			},
			ExtentList{
				Extent{Start: time.Unix(0, 0), End: time.Unix(30, 0)},
			},
		},

		{
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

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {

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

	// test when start is after end
	trq := TimeRangeQuery{Statement: "up", Extent: Extent{Start: time.Unix(20, 0),
		End: time.Unix(10, 0)}, Step: time.Duration(10) * time.Second}
	ExtentList{Extent{}}.CalculateDeltas(trq.Extent, trq.Step)

	tests := []struct {
		have                 []Extent
		expected             []Extent
		start, end, stepSecs int64
	}{
		{
			[]Extent{},
			[]Extent{{Start: time.Unix(1, 0), End: time.Unix(100, 0)}},
			1, 100, 1,
		},
		{
			[]Extent{{Start: time.Unix(50, 0), End: time.Unix(100, 0)}},
			[]Extent{{Start: time.Unix(1, 0), End: time.Unix(49, 0)}},
			1, 100, 1,
		},
		{
			[]Extent{{Start: time.Unix(50, 0), End: time.Unix(100, 0)}},
			[]Extent{{Start: time.Unix(1, 0), End: time.Unix(49, 0)},
				{Start: time.Unix(101, 0), End: time.Unix(101, 0)}},
			1, 101, 1,
		},
		{
			[]Extent{{Start: time.Unix(1, 0), End: time.Unix(100, 0)}},
			[]Extent{{Start: time.Unix(101, 0), End: time.Unix(101, 0)}},
			1, 101, 1,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {

			trq := TimeRangeQuery{Statement: "up", Extent: Extent{Start: time.Unix(test.start, 0),
				End: time.Unix(test.end, 0)}, Step: time.Duration(test.stepSecs) * time.Second}
			trq.NormalizeExtent()
			d := ExtentList(test.have).CalculateDeltas(trq.Extent, trq.Step)

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
