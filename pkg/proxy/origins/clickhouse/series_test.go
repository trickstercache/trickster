/*
 * Copyright 2020 Comcast Cable Communications Management, LLC
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

package clickhouse

import (
	"github.com/tricksterproxy/trickster/pkg/timeseries"
	"reflect"
	"testing"
	"time"
)

const testStep = time.Duration(10) * time.Second

var before, after *ResultsEnvelope
var extent timeseries.Extent

func TestSetStep(t *testing.T) {
	re := ResultsEnvelope{}
	const step = time.Duration(300) * time.Minute
	re.SetStep(step)
	if re.StepDuration != step {
		t.Errorf(`expected "%s". got "%s"`, testStep, re.StepDuration)
	}
}

func TestStep(t *testing.T) {
	re := ResultsEnvelope{}
	const step = time.Duration(300) * time.Minute
	re.SetStep(step)
	if re.Step() != step {
		t.Errorf(`expected "%s". got "%s"`, testStep, re.Step())
	}
}

func testRe() *ResultsEnvelope {
	return newRe().addMeta("t", "UInt64", "v", "Float")
}

func testEx(start int64, end int64) timeseries.Extent {
	return timeseries.Extent{Start: time.Unix(start, 0), End: time.Unix(end, 0)}
}

// Multi-series tests omitted for single series ClickHouse
func TestMerge(t *testing.T) {
	var a, b, merged *ResultsEnvelope
	test := func(run string) {
		t.Run(run, func(t *testing.T) {
			merged.Sort()
			a.Merge(true, b)
			if !reflect.DeepEqual(merged, a) {
				t.Errorf("mismatch\n  actual=%v\nexpected=%v", a, merged)
			}
		})
	}

	a = testRe().addPoint(10, 1.5).addExtent(10).setStep("5s")
	b = testRe().addPoint(5, 1.5).addPoint(15, 1.5).addExtent(5).addExtent(15).setStep("5s")
	merged = testRe().addPoint(5, 1.5).addPoint(10, 1.5).addPoint(15, 1.5).addExtents(5, 15).setStep("5s")
	test("Series that adhere to rule")

	a = testRe().addPoint(1000, 1.5).addExtent(10000).setStep("5000s")
	b = testRe().setStep("5000s")
	merged = testRe().addPoint(1000, 1.5).addExtent(10000).setStep("5000s")
	test("Empty second series")

	a = testRe().addPoint(10000, 1.5).addPoint(15000, 1.5).addExtents(10000, 15000).setStep("5000s")
	b = testRe().addPoint(30000, 1.5).addPoint(35000, 1.5).addExtents(30000, 35000).setStep("5000s")
	merged = testRe().addPoint(10000, 1.5).addPoint(15000, 1.5).addPoint(30000, 1.5).addPoint(35000, 1.5).
		addExtents(10000, 15000).addExtents(30000, 35000).setStep("5000s")
	test("Merge multiple extends")

	a = testRe().addPoint(10000, 1.5).addPoint(15000, 1.5).addExtents(10000, 15000).setStep("5000s")
	b = testRe().addPoint(15000, 1.5).addPoint(20000, 1.5).addExtents(15000, 20000).setStep("5000s")
	merged = testRe().addPoint(10000, 1.5).addPoint(15000, 1.5).addPoint(20000, 1.5).
		addExtents(10000, 20000).setStep("5000s")
	test("Merge with some overlapping extents")
}

// Multi-series tests omitted for single series ClickHouse
func TestCropToRange(t *testing.T) {
	test := func(run string) {
		t.Run(run, func(t *testing.T) {
			before.CropToRange(extent)
			if !reflect.DeepEqual(before, after) {
				t.Errorf("mismatch\nexpected=%v\ngot=%v", after, before)
			}
		})
	}

	before = testRe().addPoint(1644004600, 1.5).addExtent(1644004600).setStep("10s")
	after = testRe().addPoint(1644004600, 1.5).addExtent(1644004600).setStep("10s")
	extent = testEx(0, 1644004600)
	test("First element as timestamp matching extent end")

	before = testRe().addPoint(1544004600, 1.5).addExtent(1544004600).setStep("10s")
	after = testRe().addPoint(1544004600, 1.5).addExtent(1544004600).setStep("10s")
	extent = testEx(0, 1644004600)
	test("Trim nothing when series within crop range")

	before = testRe().addPoint(1544004600, 1.5).addExtent(1544004600).setStep("10s")
	after = testRe().setStep("10s")
	extent = testEx(0, 10)
	test("Trim everything when all data is too late")

	before = testRe().addPoint(100, 1.5).addExtent(100).setStep("10s")
	after = testRe().setStep("10s")
	extent = testEx(10000, 20000)
	test("Trim everything when all data is too early")

	before = testRe().addPoint(100, 1.5).addPoint(200, 1.5).addPoint(300, 1.5).addExtents(100, 300).setStep("100s")
	after = testRe().addPoint(300, 1.5).addExtent(300).setStep("100s")
	extent = testEx(300, 400)
	test("Trim some off the beginning")

	before = testRe().addPoint(100, 1.5).addPoint(200, 1.5).addPoint(300, 1.5).addExtents(100, 300).setStep("100s")
	after = testRe().addPoint(200, 1.5).addExtent(200).setStep("100s")
	extent = testEx(200, 200)
	test("Trim some off of both ends")

	before = testRe().addPoint(100, 1.5).addPoint(200, 1.5).addPoint(300, 1.5).addExtents(100, 300).setStep("100s")
	after = testRe().addPoint(200, 1.5).addPoint(300, 1.5).addExtents(200, 300).setStep("100s")
	extent = testEx(200, 300)
	test("last datapoint is on the Crop extent")

	before = testRe().setStep("100s")
	after = testRe().setStep("100s")
	extent = testEx(200, 300)
	test("no data in series provided")
}

func TestCropToSize(t *testing.T) {
	var size int
	var bft time.Time
	now := time.Now().Truncate(testStep)
	nowSec := now.Unix()
	test := func(run string) {
		t.Run(run, func(t *testing.T) {
			before.CropToSize(size, bft, extent)
			for i := range before.ExtentList {
				before.ExtentList[i].LastUsed = time.Time{}
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("mismatch\nexpected=%v\n     got=%v", after, before)
			}
		})
	}

	before = testRe().addPoint(1444004600, 1.5).addExtent(1444004600).setStep("10s")
	after = testRe().addPoint(1444004600, 1.5).addExtent(1444004600).setStep("10s")
	after.updateTimestamps()
	extent = testEx(0, 1444004600)
	size = 1
	bft = now
	test("Size already less than crop")

	before = testRe().addPoint(1444004600, 1.5).addPoint(1444004610, 1.5).addExtents(1444004600, 1444004610).setStep("10s")
	after = testRe().addPoint(1444004610, 1.5).addExtent(1444004610).setStep("10s")
	after.Sort()
	extent = testEx(0, 1444004610)
	size = 1
	bft = now
	test("Crop least recently used")

	before = testRe().setStep("10s")
	after = testRe().setStep("10s")
	extent = testEx(0, nowSec)
	size = 2
	bft = now
	test("Empty extent list")

	before = testRe().addPoint(1444004610, 1.5).addPoint(int(nowSec), 1.5).addExtents(1444004610, nowSec)
	after = testRe().addPoint(1444004610, 1.5).addExtents(1444004610, nowSec-300)
	after.updateTimestamps()
	size = 2
	extent = testEx(0, nowSec)
	bft = now.Add(-5 * time.Minute)
	test("Backfill tolerance")
}

func TestUpdateTimestamps(t *testing.T) {
	// test edge condition here (core functionality is tested across this file)
	re := ResultsEnvelope{isCounted: true}
	re.updateTimestamps()
	if re.timestamps != nil {
		t.Errorf("expected nil map, got size %d", len(re.timestamps))
	}
}

func TestClone(t *testing.T) {
	before = testRe().addPoint(1644001200, 1.5).addPoint(1644004800, 77.4).addExtents(1644001200, 1644004800).setStep("3600s")
	before.Sort()
	after := before.Clone()
	after.Sort()
	if !reflect.DeepEqual(before, after) {
		t.Errorf("mismatch\nexpected %v\nactual   %v", before, after)
	}
}

func TestSort(t *testing.T) {
	before = testRe().addPoint(1544004200, 1.5).addPoint(1544004600, 1.5).addPoint(1544004800, 1.5).
		addPoint(1544004000, 1.5).addPoint(1544004000, 1.5)
	after = testRe().addPoint(1544004000, 1.5).addPoint(1544004200, 1.5).addPoint(1544004600, 1.5).addPoint(1544004800, 1.5)
	after.isCounted = true
	after.isSorted = true
	after.tsList = []time.Time{time.Unix(1544004000, 0),
		time.Unix(1544004200, 0), time.Unix(1544004600, 0), time.Unix(1544004800, 0)}
	after.timestamps = map[time.Time]bool{time.Unix(1544004000, 0): true, time.Unix(1544004200, 0): true,
		time.Unix(1544004600, 0): true, time.Unix(1544004800, 0): true}

	before.isSorted = false
	before.Sort()
	if !reflect.DeepEqual(before, after) {
		t.Errorf("mismatch\nexpected=%v\n  actual=%v", after, before)
	}
	// test isSorted short circuit
	before.Sort()
	if !reflect.DeepEqual(before, after) {
		t.Errorf("mismatch\nexpected=%v\n  actual=%v", after, before)
	}
}

func TestSetExtents(t *testing.T) {
	re := &ResultsEnvelope{}
	ex := timeseries.ExtentList{timeseries.Extent{Start: time.Time{}, End: time.Time{}}}
	re.SetExtents(ex)
	if len(re.ExtentList) != 1 {
		t.Errorf(`expected 1. got %d`, len(re.ExtentList))
	}
}

func TestExtents(t *testing.T) {
	re := &ResultsEnvelope{}
	ex := timeseries.ExtentList{timeseries.Extent{Start: time.Time{}, End: time.Time{}}}
	re.SetExtents(ex)
	e := re.Extents()
	if len(e) != 1 {
		t.Errorf(`expected 1. got %d`, len(re.ExtentList))
	}
}

func TestSeriesCount(t *testing.T) {
	re := testRe().addPoint(99, 1.5).addPoint(199, 1.5).addPoint(299, 1.5)
	if re.SeriesCount() != 1 {
		t.Errorf("expected 1 got %d.", re.SeriesCount())
	}
}

func TestValueCount(t *testing.T) {
	re := testRe().addPoint(99, 1.5).addPoint(199, 1.5).addPoint(299, 1.5)
	if re.ValueCount() != 3 {
		t.Errorf("expected 3 got %d.", re.ValueCount())
	}
}

func TestTimestampCount(t *testing.T) {
	before = testRe().addPoint(99, 1.5).addPoint(199, 1.5).addPoint(299, 1.5)
	if before.TimestampCount() != 3 {
		t.Errorf("expected 3 got %d.", before.TimestampCount())
	}
	before = testRe().addPoint(99, 1.5).addPoint(199, 1.5)
	if before.TimestampCount() != 2 {
		t.Errorf("expected 2 got %d.", before.TimestampCount())
	}
}

func TestSize(t *testing.T) {
	re := testRe().addPoint(5, 1.5).addPoint(10, 1.5).addPoint(15, 1.5).addExtents(5, 15).setStep("5s")
	re.Sort()
	i := re.Size()
	const expected = 173
	if i != expected {
		t.Errorf("expected %d got %d", expected, i)
	}
}
