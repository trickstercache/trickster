/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package timeseries

import (
	"reflect"
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
var t600 = time.Unix(600, 0)
var t900 = time.Unix(900, 0)
var t1000 = time.Unix(1000, 0)
var t1100 = time.Unix(1100, 0)
var t1200 = time.Unix(1200, 0)
var t1300 = time.Unix(1300, 0)
var t1400 = time.Unix(1400, 0)

func TestString(t *testing.T) {

	tests := []struct {
		el       ExtentList
		expected string
	}{
		{
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
			"100-200;600-900;1100-1300",
		},

		{
			ExtentList{},
			"",
		},

		{
			ExtentList{
				Extent{Start: t100, End: t200},
			},
			"100-200",
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
			el.Copy(),
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 1
			Extent{Start: t100, End: t1300},
			el.Copy(),
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 2
			Extent{Start: t101, End: t1300},
			el.Copy(),
			ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 3
			Extent{Start: t200, End: t1300},
			el.Copy(),
			ExtentList{
				Extent{Start: t200, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 4
			Extent{Start: t201, End: t1300},
			el.Copy(),
			ExtentList{
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 5
			Extent{Start: t99, End: t1200},
			el.Copy(),
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1200},
			},
		},

		{ // Run 6
			Extent{Start: t100, End: t1200},
			el.Copy(),
			ExtentList{
				Extent{Start: t100, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1200},
			},
		},

		{ // Run 7
			Extent{Start: t101, End: t1200},
			el.Copy(),
			ExtentList{
				Extent{Start: t101, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1200},
			},
		},

		{ // Run 8
			Extent{Start: t200, End: t1200},
			el.Copy(),
			ExtentList{
				Extent{Start: t200, End: t200},
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1200},
			},
		},

		{ // Run 9
			Extent{Start: t201, End: t1200},
			el.Copy(),
			ExtentList{
				Extent{Start: t600, End: t900},
				Extent{Start: t1100, End: t1200},
			},
		},

		{ // Run 10
			Extent{Start: t98, End: t98},
			el.Copy(),
			ExtentList{},
		},

		{ // Run 11
			Extent{Start: t98, End: t99},
			el.Copy(),
			ExtentList{},
		},

		{ // Run 12
			Extent{Start: t98, End: t100},
			el.Copy(),
			ExtentList{
				Extent{Start: t100, End: t100},
			},
		},

		{ // Run 13
			Extent{Start: t98, End: t101},
			el.Copy(),
			ExtentList{
				Extent{Start: t100, End: t101},
			},
		},

		{ // Run 14
			Extent{Start: t98, End: t200},
			el.Copy(),
			ExtentList{
				Extent{Start: t100, End: t200},
			},
		},

		{ // Run 15
			Extent{Start: t100, End: t200},
			el.Copy(),
			ExtentList{
				Extent{Start: t100, End: t200},
			},
		},

		{ // Run 16
			Extent{Start: t100, End: t101},
			el.Copy(),
			ExtentList{
				Extent{Start: t100, End: t101},
			},
		},

		{ // Run 17
			Extent{Start: t1000, End: t1300},
			el.Copy(),
			ExtentList{
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 18
			Extent{Start: t1100, End: t1300},
			el.Copy(),
			ExtentList{
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 19
			Extent{Start: t1200, End: t1300},
			el.Copy(),
			ExtentList{
				Extent{Start: t1200, End: t1300},
			},
		},

		{ // Run 20
			Extent{Start: t1300, End: t1300},
			el.Copy(),
			ExtentList{
				Extent{Start: t1300, End: t1300},
			},
		},

		{ // Run 21
			Extent{Start: t1300, End: t1400},
			el.Copy(),
			ExtentList{
				Extent{Start: t1300, End: t1300},
			},
		},

		{ // Run 22
			Extent{Start: t1200, End: t1400},
			el.Copy(),
			ExtentList{
				Extent{Start: t1200, End: t1300},
			},
		},

		{ // Run 23
			Extent{Start: t1000, End: t1400},
			el.Copy(),
			ExtentList{
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 24
			Extent{Start: t900, End: t1400},
			el.Copy(),
			ExtentList{
				Extent{Start: t900, End: t900},
				Extent{Start: t1100, End: t1300},
			},
		},

		{ // Run 25
			Extent{Start: t98, End: t1400},
			el.Copy(),
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
			result := test.seed.Copy().Crop(test.cropRange)
			if !reflect.DeepEqual(test.expected, result) {
				t.Errorf("mismatch in Crop: expected=%s got=%s", test.expected, result)
			}
		})
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
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {

			result := ExtentList(test.uncompressed).Compress(time.Duration(30) * time.Second)

			if !reflect.DeepEqual(result, test.compressed) {
				t.Errorf("mismatch in Compress: expected=%s got=%s", test.compressed, result)
			}
		})
	}
}
