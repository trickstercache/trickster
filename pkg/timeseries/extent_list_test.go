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
var t400 = time.Unix(400, 0)
var t500 = time.Unix(500, 0)
var t600 = time.Unix(600, 0)
var t700 = time.Unix(700, 0)
var t800 = time.Unix(800, 0)
var t900 = time.Unix(900, 0)
var t1000 = time.Unix(1000, 0)
var t1100 = time.Unix(1100, 0)
var t1200 = time.Unix(1200, 0)
var t1300 = time.Unix(1300, 0)
var t1400 = time.Unix(1400, 0)
var t1500 = time.Unix(1500, 0)
var t1600 = time.Unix(1600, 0)
var t1700 = time.Unix(1700, 0)
var t1800 = time.Unix(1800, 0)
var t1900 = time.Unix(1900, 0)

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

func TestEncompasses(t *testing.T) {

	el := ExtentList{
		Extent{Start: t100, End: t200},
		Extent{Start: t600, End: t900},
		Extent{Start: t1100, End: t1300},
	}

	tests := []struct {
		el       ExtentList
		extent   Extent
		expected bool
	}{
		{ // 0
			el,
			Extent{Start: t100, End: t100},
			true,
		},
		{ // 1
			el,
			Extent{Start: time.Unix(0, 0), End: t100},
			false,
		},
		{ // 2
			el,
			Extent{Start: time.Unix(0, 0), End: time.Unix(0, 0)},
			false,
		},
		{ // 3
			el,
			Extent{Start: t201, End: t201},
			true,
		},
		{ // 4
			el,
			Extent{Start: t1400, End: t1400},
			false,
		},
		{ // 5
			ExtentList{},
			Extent{Start: t100, End: t100},
			false,
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
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
		el       ExtentList
		extent   Extent
		expected bool
	}{
		{ // 0
			el,
			Extent{Start: t100, End: t100},
			false,
		},
		{ // 1
			el,
			Extent{Start: time.Unix(0, 0), End: t100},
			false,
		},
		{ // 2
			el,
			Extent{Start: time.Unix(0, 0), End: time.Unix(0, 0)},
			false,
		},
		{ // 3
			el,
			Extent{Start: t201, End: t201},
			false,
		},
		{ // 4
			el,
			Extent{Start: t1400, End: t1400},
			false,
		},
		{ // 5
			ExtentList{},
			Extent{Start: t100, End: t100},
			false,
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
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
		el       ExtentList
		removals ExtentList
		expected ExtentList
	}{
		{ // Case 0 (splice entire line)
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
		{ // case 1 (adjust start)
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			ExtentList{
				Extent{Start: t100, End: t100},
			},
			ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{ // case 2 (adjust end)
			ExtentList{
				Extent{Start: t100, End: t201},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			ExtentList{
				Extent{Start: t201, End: t201},
			},
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{ // case 3 (adjust start and end)
			ExtentList{
				Extent{Start: t100, End: t201},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			ExtentList{
				Extent{Start: t100, End: t100},
				Extent{Start: t201, End: t201},
			},
			ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{ // case 4 (overlap)
			ExtentList{
				Extent{Start: t100, End: t201},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			ExtentList{
				Extent{Start: t101, End: t200},
			},
			ExtentList{
				Extent{Start: t100, End: t100},
				Extent{Start: t201, End: t201},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{ // Case 5 (splice entire line 2)
			ExtentList{
				Extent{Start: t101, End: t200},
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
		{ // Case 6 (splice entire line 3)
			ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			ExtentList{
				Extent{Start: t100, End: t201},
			},
			ExtentList{
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{ // Case 7 empty removals
			ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			ExtentList{},
			ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
		{ // Case 8 subject list
			ExtentList{},
			ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			v := test.el.Remove(test.removals, step)
			if !v.Equal(test.expected) {
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

func TestCloneRange(t *testing.T) {

	el := ExtentList{
		Extent{Start: t600, End: t900},
	}

	res := el.CloneRange(-1, -1)
	if res != nil {
		t.Error("expected nil result", res)
	}

	res = el.CloneRange(0, 200)
	if res != nil {
		t.Error("expected nil result", res)
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

func TestEqual(t *testing.T) {

	el := ExtentList{
		Extent{Start: t100, End: t200},
		Extent{Start: t600, End: t900},
		Extent{Start: t1100, End: t1300},
	}

	b := el.Equal(nil)
	if b {
		t.Error("expected false")
	}

	b = el.Equal(ExtentList{})
	if b {
		t.Error("expected false")
	}

	el2 := ExtentList{
		Extent{Start: t101, End: t200},
		Extent{Start: t600, End: t900},
		Extent{Start: t1100, End: t1300},
	}

	b = el.Equal(el2)
	if b {
		t.Error("expected false")
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

func TestTimestampCount(t *testing.T) {

	el := ExtentList{
		Extent{Start: t100, End: t200},
		Extent{Start: t600, End: t900},
		Extent{},
		Extent{Start: t1100, End: t1300},
	}

	const expected int64 = 9

	if v := el.TimestampCount(time.Second * 100); v != expected {
		t.Errorf("expected %d got %d", expected, v)
	}

}

func TestSplice(t *testing.T) {

	tests := []struct {
		el, expected               ExtentList
		step, maxRange, spliceStep time.Duration
		maxPoints                  int
	}{
		{ // case 0 - spliceByPoints basic
			el: ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{},
				Extent{Start: t1100, End: t1300}},
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
		{ // case 1 - spliceByTime Fast Fail
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
		{ // case 2 - Splice Fast Fail 01
			el:       ExtentList{},
			expected: ExtentList{},
		},
		{ // case 3 - Splice Fast Fail 02
			el:       nil,
			expected: nil,
		},
		{ // case 4 - spliceByTimeAligned Fast Fail 01
			el: ExtentList{
				Extent{Start: t100, End: t200},
			},
			expected: ExtentList{
				Extent{Start: t100, End: t200},
			},
			step:       time.Second * 100,
			spliceStep: time.Second * 100,
		},
		{ // case 5 - spliceByPoints Fast Fail
			el: ExtentList{
				Extent{Start: t100, End: t200},
			},
			expected: ExtentList{
				Extent{Start: t100, End: t200},
			},
			maxPoints: 2,
		},
		{ // case 6 - spliceByTimeAligned left-side splice only
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
		{ // case 7 - spliceByTimeAligned, left- and right-side splicing
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
		{ // case 8 - spliceByTime basic
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
		{ // case 9 - spliceByTimeAligned, spliceStep < maxRange
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
		{ // case 10 - spliceByTimeAligned, step > spliceStep
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
		{ // case 11 - spliceByTimeAligned, step > maxRange
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
		{ // case 12 - spliceByPoints, step > maxPoints spread
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
		{ // case 12 - spliceByTime, step > maxRange
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

	for i, test := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			out := test.el.Splice(test.step, test.maxRange, test.spliceStep, test.maxPoints)
			if out == nil && test.expected == nil {
				return
			}
			if (test.expected == nil && out != nil) ||
				(out == nil && test.expected != nil) ||
				(!out.Equal(test.expected)) {
				t.Errorf("expected %s\ngot      %s", test.expected, out)
			}
		})
	}
}

func genBenchmarkExtentList(len int, step time.Duration) (el ExtentList, start, end time.Time) {
	end = time.Now()
	start = end.Add(time.Duration(-1*len) * step)
	el = make(ExtentList, len)
	for i := 0; i < len; i++ {
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
		r = haves[i].CalculateDeltas(wants[i], bmTimeStep)
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
