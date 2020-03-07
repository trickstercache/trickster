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

package clickhouse

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/pkg/sort/times"
)

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

func TestMerge(t *testing.T) {
	tests := []struct {
		a, b, merged *ResultsEnvelope
	}{
		// Run 0: Series that adhere to rule
		{
			a: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(10, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10, 0), End: time.Unix(10, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			b: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(5, 0), Value: 1.5},
							{Timestamp: time.Unix(15, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(5, 0), End: time.Unix(5, 0)},
					timeseries.Extent{Start: time.Unix(15, 0), End: time.Unix(15, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			merged: &ResultsEnvelope{
				isCounted:  true,
				isSorted:   true,
				tslist:     times.Times{time.Unix(5, 0), time.Unix(10, 0), time.Unix(15, 0)},
				timestamps: map[time.Time]bool{time.Unix(5, 0): true, time.Unix(10, 0): true, time.Unix(15, 0): true},
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(5, 0), Value: 1.5},
							{Timestamp: time.Unix(10, 0), Value: 1.5},
							{Timestamp: time.Unix(15, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(5, 0), End: time.Unix(15, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
		},
		// Run 1: Empty second series
		{
			a: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(10000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			b: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{},
					},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(5000) * time.Second,
			},
			merged: &ResultsEnvelope{
				isCounted:  true,
				isSorted:   true,
				tslist:     times.Times{time.Unix(10000, 0)},
				timestamps: map[time.Time]bool{time.Unix(10000, 0): true},
				Data: map[string]*DataSet{
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(10000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
		},
		// Run 2: second series has new metric
		{
			a: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(10000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			b: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(15000, 0), End: time.Unix(15000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			merged: &ResultsEnvelope{
				isCounted:  true,
				isSorted:   true,
				tslist:     times.Times{time.Unix(10000, 0), time.Unix(15000, 0)},
				timestamps: map[time.Time]bool{time.Unix(10000, 0): true, time.Unix(15000, 0): true},
				Data: map[string]*DataSet{
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
						},
					},
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(15000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
		},
		// Run 3: merge one metric, one metric unchanged
		{
			a: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
						},
					},
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(10000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			b: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(15000, 0), End: time.Unix(15000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			merged: &ResultsEnvelope{
				isCounted:  true,
				isSorted:   true,
				tslist:     times.Times{time.Unix(10000, 0), time.Unix(15000, 0)},
				timestamps: map[time.Time]bool{time.Unix(10000, 0): true, time.Unix(15000, 0): true},
				Data: map[string]*DataSet{
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
						},
					},
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(15000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
		},
		// Run 4: merge multiple extents
		{
			a: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(15000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			b: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(30000, 0), Value: 1.5},
							{Timestamp: time.Unix(35000, 0), Value: 1.5},
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(30000, 0), Value: 1.5},
							{Timestamp: time.Unix(35000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(30000, 0), End: time.Unix(35000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			merged: &ResultsEnvelope{
				isCounted:  true,
				isSorted:   true,
				tslist:     times.Times{time.Unix(10000, 0), time.Unix(15000, 0), time.Unix(30000, 0), time.Unix(35000, 0)},
				timestamps: map[time.Time]bool{time.Unix(10000, 0): true, time.Unix(15000, 0): true, time.Unix(30000, 0): true, time.Unix(35000, 0): true},
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
							{Timestamp: time.Unix(30000, 0), Value: 1.5},
							{Timestamp: time.Unix(35000, 0), Value: 1.5},
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
							{Timestamp: time.Unix(30000, 0), Value: 1.5},
							{Timestamp: time.Unix(35000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(15000, 0)},
					timeseries.Extent{Start: time.Unix(30000, 0), End: time.Unix(35000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
		},
		//
		//
		// Run 5: merge with some overlapping extents
		{
			a: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(15000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			b: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
							{Timestamp: time.Unix(20000, 0), Value: 1.5},
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
							{Timestamp: time.Unix(20000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(15000, 0), End: time.Unix(20000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			merged: &ResultsEnvelope{
				isCounted:  true,
				isSorted:   true,
				tslist:     times.Times{time.Unix(10000, 0), time.Unix(15000, 0), time.Unix(20000, 0)},
				timestamps: map[time.Time]bool{time.Unix(10000, 0): true, time.Unix(15000, 0): true, time.Unix(20000, 0): true},
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
							{Timestamp: time.Unix(20000, 0), Value: 1.5},
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(10000, 0), Value: 1.5},
							{Timestamp: time.Unix(15000, 0), Value: 1.5},
							{Timestamp: time.Unix(20000, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(20000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
		},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.a.Merge(true, test.b)
			if !reflect.DeepEqual(test.merged, test.a) {
				t.Errorf("mismatch\n  actual=%v\nexpected=%v", test.a, test.merged)
			}
		})
	}
}

func TestCropToRange(t *testing.T) {
	tests := []struct {
		before, after *ResultsEnvelope
		extent        timeseries.Extent
	}{
		// Run 0: Case where the very first element in the matrix has a timestamp matching the extent's end
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1644004600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1644004600, 0), End: time.Unix(1644004600, 0)},
				},
				StepDuration: testStep,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1644004600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1644004600, 0), End: time.Unix(1644004600, 0)},
				},
				StepDuration: testStep,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1644004600, 0),
			},
		},
		// Run 1: Case where we trim nothing
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1544004600, 0), End: time.Unix(1544004600, 0)},
				},
				StepDuration: testStep,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1544004600, 0), End: time.Unix(1544004600, 0)},
				},
				StepDuration: testStep,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1644004600, 0),
			},
		},
		// Run 2: Case where we trim everything (all data is too late)
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1544004600, 0), End: time.Unix(1544004600, 0)},
				},
				StepDuration: testStep,
			},
			after: &ResultsEnvelope{
				Data:         map[string]*DataSet{},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: testStep,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(10, 0),
			},
		},
		// Run 3: Case where we trim everything (all data is too early)
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(100, 0)},
				},
				StepDuration: testStep,
			},
			after: &ResultsEnvelope{
				Data:         map[string]*DataSet{},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: testStep,
			},
			extent: timeseries.Extent{
				Start: time.Unix(10000, 0),
				End:   time.Unix(20000, 0),
			},
		},
		// Run 4: Case where we trim some off the beginning
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"d": {
						Metric: map[string]interface{}{"__name__": "d"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"d": {
						Metric: map[string]interface{}{"__name__": "d"},
						Points: []Point{
							{Timestamp: time.Unix(300, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(300, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(300, 0),
				End:   time.Unix(400, 0),
			},
		},
		// Run 5: Case where we trim some off the ends
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"e": {
						Metric: map[string]interface{}{"__name__": "e"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"e": {
						Metric: map[string]interface{}{"__name__": "e"},
						Points: []Point{
							{Timestamp: time.Unix(200, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(200, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(200, 0),
				End:   time.Unix(200, 0),
			},
		},
		// Run 6: Case where the last datapoint is on the Crop extent
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"f": {
						Metric: map[string]interface{}{"__name__": "f"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"f": {
						Metric: map[string]interface{}{"__name__": "f"},
						Points: []Point{
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(200, 0),
				End:   time.Unix(300, 0),
			},
		},
		// Run 7: Case where we aren't given any datapoints
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"g": {
						Metric: map[string]interface{}{"__name__": "g"},
						Points: []Point{},
					},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &ResultsEnvelope{
				Data:         map[string]*DataSet{},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(200, 0),
				End:   time.Unix(300, 0),
			},
		},

		// Run 9: Case where after cropping, an inner series is empty/removed
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
						},
					},
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(600, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(400, 0), End: time.Unix(600, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(400, 0),
				End:   time.Unix(600, 0),
			},
		},
		// Run 10: Case where after cropping, the front series is empty/removed
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(600, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(400, 0), End: time.Unix(600, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(400, 0),
				End:   time.Unix(600, 0),
			},
		},
		// Run 11: Case where after cropping, the back series is empty/removed
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(100, 0), Value: 1.5},
							{Timestamp: time.Unix(200, 0), Value: 1.5},
							{Timestamp: time.Unix(300, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(600, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(400, 0), Value: 1.5},
							{Timestamp: time.Unix(500, 0), Value: 1.5},
							{Timestamp: time.Unix(600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(400, 0), End: time.Unix(600, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(400, 0),
				End:   time.Unix(600, 0),
			},
		},
		// Run 12: Case where we short circuit since the dataset is already entirely inside the crop range
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{},
					},
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(100, 0),
				End:   time.Unix(600, 0),
			},
		},
		// Run 13: Case where we short circuit since the dataset is empty
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(300, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(300, 0),
				End:   time.Unix(600, 0),
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.before.CropToRange(test.extent)
			if !reflect.DeepEqual(test.before, test.after) {
				t.Errorf("mismatch\nexpected=%v\ngot=%v", test.after, test.before)
			}
		})
	}
}

const testStep = time.Duration(10) * time.Second

func TestCropToSize(t *testing.T) {

	now := time.Now().Truncate(testStep)

	tests := []struct {
		before, after *ResultsEnvelope
		size          int
		bft           time.Time
		extent        timeseries.Extent
	}{
		// case 0: where we already have the number of timestamps we are cropping to
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1444004600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1444004600, 0), End: time.Unix(1444004600, 0)},
				},
				StepDuration: testStep,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1444004600, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1444004600, 0), End: time.Unix(1444004600, 0)},
				},
				StepDuration: testStep,
				timestamps:   map[time.Time]bool{time.Unix(1444004600, 0): true},
				tslist:       times.Times{time.Unix(1444004600, 0)},
				isCounted:    true,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1444004600, 0),
			},
			size: 1,
			bft:  now,
		},

		// case 1
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1444004600, 0), Value: 1.5},
							{Timestamp: time.Unix(1444004610, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1444004600, 0), End: time.Unix(1444004610, 0)},
				},
				StepDuration: testStep,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1444004610, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1444004610, 0), End: time.Unix(1444004610, 0)},
				},
				StepDuration: testStep,
				timestamps:   map[time.Time]bool{time.Unix(1444004610, 0): true},
				tslist:       times.Times{time.Unix(1444004610, 0)},
				isCounted:    true,
				isSorted:     true,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1444004610, 0),
			},
			size: 1,
			bft:  now,
		},

		// case 2 - empty extent list
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{},
					},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: testStep,
			},
			after: &ResultsEnvelope{
				Data:         map[string]*DataSet{},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: testStep,
			},
			extent: timeseries.Extent{},
			size:   1,
			bft:    now,
		},

		// case 3 - backfill tolerance
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1444004610, 0), Value: 1.5},
							{Timestamp: now, Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1444004610, 0), End: now},
				},
				StepDuration: testStep,
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1444004610, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1444004610, 0), End: now.Add(-5 * time.Minute)},
				},
				StepDuration: testStep,
				timestamps:   map[time.Time]bool{time.Unix(1444004610, 0): true},
				tslist:       times.Times{time.Unix(1444004610, 0)},
				isCounted:    true,
				isSorted:     false,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   now,
			},
			size: 2,
			bft:  now.Add(-5 * time.Minute),
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.before.CropToSize(test.size, test.bft, test.extent)

			for i := range test.before.ExtentList {
				test.before.ExtentList[i].LastUsed = time.Time{}
			}

			if !reflect.DeepEqual(test.before, test.after) {
				t.Errorf("mismatch\nexpected=%v\n     got=%v", test.after, test.before)
			}
		})
	}
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

	tests := []struct {
		before *ResultsEnvelope
	}{
		// Run 0
		{
			before: &ResultsEnvelope{
				Meta:        []FieldDefinition{{Name: "1", Type: "string"}},
				Serializers: map[string]func(interface{}){"test": nil},
				tslist:      times.Times{time.Unix(1644001200, 0)},
				timestamps:  map[time.Time]bool{time.Unix(1644001200, 0): true},
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1644001200, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1644001200, 0), End: time.Unix(1644001200, 0)},
				},
				StepDuration: time.Duration(3600) * time.Second,
				SeriesOrder:  []string{"a"},
			},
		},

		// Run 1
		{
			before: &ResultsEnvelope{
				tslist:     times.Times{time.Unix(1644001200, 0), time.Unix(1644004800, 0)},
				timestamps: map[time.Time]bool{time.Unix(1644001200, 0): true, time.Unix(1644004800, 0): true},
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1644001200, 0), Value: 1.5},
						},
					},

					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(1644001200, 0), Value: 1.5},
							{Timestamp: time.Unix(1644004800, 0), Value: 1.5},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1644001200, 0), End: time.Unix(1644004800, 0)},
				},
				StepDuration: time.Duration(3600) * time.Second,
				SeriesOrder:  []string{"a", "b"},
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			after := test.before.Clone()
			if !reflect.DeepEqual(test.before, after) {
				t.Errorf("mismatch\nexpected %v\nactual   %v", test.before, after)
			}
		})
	}

}

func TestSort(t *testing.T) {
	tests := []struct {
		before, after *ResultsEnvelope
		extent        timeseries.Extent
	}{
		// Case where we trim nothing
		{
			before: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1544004200, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004800, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004000, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004000, 0), Value: 1.5}, // sort should also dupe kill
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004200, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004000, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004800, 0), Value: 1.5},
						},
					},
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(1544004800, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004200, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004000, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
						},
					},
				},
			},
			after: &ResultsEnvelope{
				isSorted:  true,
				isCounted: true,
				tslist:    []time.Time{time.Unix(1544004000, 0), time.Unix(1544004200, 0), time.Unix(1544004600, 0), time.Unix(1544004800, 0)},
				timestamps: map[time.Time]bool{time.Unix(1544004000, 0): true, time.Unix(1544004200, 0): true,
					time.Unix(1544004600, 0): true, time.Unix(1544004800, 0): true},
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "a"},
						Points: []Point{
							{Timestamp: time.Unix(1544004000, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004200, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004800, 0), Value: 1.5},
						},
					},
					"b": {
						Metric: map[string]interface{}{"__name__": "b"},
						Points: []Point{
							{Timestamp: time.Unix(1544004000, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004200, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004800, 0), Value: 1.5},
						},
					},
					"c": {
						Metric: map[string]interface{}{"__name__": "c"},
						Points: []Point{
							{Timestamp: time.Unix(1544004000, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004200, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
							{Timestamp: time.Unix(1544004800, 0), Value: 1.5},
						},
					},
				},
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.before.isSorted = false
			test.before.Sort()
			if !reflect.DeepEqual(test.before, test.after) {
				t.Errorf("mismatch\nexpected=%v\n  actual=%v", test.after, test.before)
			}
			// test isSorted short circuit
			test.before.Sort()
			if !reflect.DeepEqual(test.before, test.after) {
				t.Errorf("mismatch\nexpected=%v\n  actual=%v", test.after, test.before)
			}
		})
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
	re := &ResultsEnvelope{
		Data: map[string]*DataSet{
			"d": {
				Metric: map[string]interface{}{"__name__": "d"},
				Points: []Point{
					{Timestamp: time.Unix(99, 0), Value: 1.5},
					{Timestamp: time.Unix(199, 0), Value: 1.5},
					{Timestamp: time.Unix(299, 0), Value: 1.5},
				},
			},
		},
	}
	if re.SeriesCount() != 1 {
		t.Errorf("expected 1 got %d.", re.SeriesCount())
	}
}

func TestValueCount(t *testing.T) {
	re := &ResultsEnvelope{
		Data: map[string]*DataSet{
			"d": {
				Metric: map[string]interface{}{"__name__": "d"},
				Points: []Point{
					{Timestamp: time.Unix(99, 0), Value: 1.5},
					{Timestamp: time.Unix(199, 0), Value: 1.5},
					{Timestamp: time.Unix(299, 0), Value: 1.5},
				},
			},
		},
	}
	if re.ValueCount() != 3 {
		t.Errorf("expected 3 got %d.", re.ValueCount())
	}
}

func TestTimestampCount(t *testing.T) {

	tests := []struct {
		ts       *ResultsEnvelope
		expected int
	}{
		{
			ts: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"d": {
						Metric: map[string]interface{}{"__name__": "d"},
						Points: []Point{
							{Timestamp: time.Unix(99, 0), Value: 1.5},
							{Timestamp: time.Unix(199, 0), Value: 1.5},
							{Timestamp: time.Unix(299, 0), Value: 1.5},
						},
					},
				},
			},
			expected: 3,
		},

		{
			ts: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"d": {
						Metric: map[string]interface{}{"__name__": "d"},
						Points: []Point{
							{Timestamp: time.Unix(99, 0), Value: 1.5},
							{Timestamp: time.Unix(199, 0), Value: 1.5},
						},
					},
				},
			},
			expected: 2,
		},

		{
			ts: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"a": {
						Metric: map[string]interface{}{"__name__": "d"},
						Points: []Point{
							{Timestamp: time.Unix(99, 0), Value: 1.5},
							{Timestamp: time.Unix(199, 0), Value: 1.5},
						},
					},
					"e": {
						Metric: map[string]interface{}{"__name__": "e"},
						Points: []Point{
							{Timestamp: time.Unix(99, 0), Value: 1.5},
							{Timestamp: time.Unix(299, 0), Value: 1.5},
						},
					},
				},
			},
			expected: 3,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			tc := test.ts.TimestampCount()
			if tc != test.expected {
				t.Errorf("expected %d got %d.", test.expected, tc)
			}
		})
	}
}

func TestMergeSeriesOrder(t *testing.T) {

	re := ResultsEnvelope{}
	so1 := []string{"a", "e"}
	re.mergeSeriesOrder(so1)
	if !reflect.DeepEqual(re.SeriesOrder, so1) {
		t.Errorf("expected [%s] got [%s]", strings.Join(so1, ","), strings.Join(re.SeriesOrder, ","))
	}
	re.Data = map[string]*DataSet{"a": nil, "e": nil}

	so2 := []string{"d", "e"}
	ex2 := []string{"a", "d", "e"}
	re.mergeSeriesOrder(so2)
	if !reflect.DeepEqual(re.SeriesOrder, ex2) {
		t.Errorf("expected [%s] got [%s]", strings.Join(ex2, ","), strings.Join(re.SeriesOrder, ","))
	}
	re.Data = map[string]*DataSet{"a": nil, "d": nil, "e": nil}

	so3 := []string{"b", "c", "e"}
	ex3 := []string{"a", "d", "b", "c", "e"}
	re.mergeSeriesOrder(so3)
	if !reflect.DeepEqual(re.SeriesOrder, ex3) {
		t.Errorf("expected [%s] got [%s]", strings.Join(ex3, ","), strings.Join(re.SeriesOrder, ","))
	}
	re.Data = map[string]*DataSet{"a": nil, "d": nil, "b": nil, "c": nil, "e": nil}

	so4 := []string{"f"}
	ex4 := []string{"a", "d", "b", "c", "e", "f"}
	re.mergeSeriesOrder(so4)
	if !reflect.DeepEqual(re.SeriesOrder, ex4) {
		t.Errorf("expected [%s] got [%s]", strings.Join(ex4, ","), strings.Join(re.SeriesOrder, ","))
	}

}

func TestSize(t *testing.T) {
	r := &ResultsEnvelope{
		isCounted:  true,
		isSorted:   true,
		tslist:     times.Times{time.Unix(5, 0), time.Unix(10, 0), time.Unix(15, 0)},
		timestamps: map[time.Time]bool{time.Unix(5, 0): true, time.Unix(10, 0): true, time.Unix(15, 0): true},
		Data: map[string]*DataSet{
			"a": {
				Metric: map[string]interface{}{"__name__": "a"},
				Points: []Point{
					{Timestamp: time.Unix(5, 0), Value: 1.5},
					{Timestamp: time.Unix(10, 0), Value: 1.5},
					{Timestamp: time.Unix(15, 0), Value: 1.5},
				},
			},
		},
		Meta:        []FieldDefinition{{Name: "test", Type: "Test"}},
		SeriesOrder: []string{"test"},
		ExtentList: timeseries.ExtentList{
			timeseries.Extent{Start: time.Unix(5, 0), End: time.Unix(15, 0)},
		},
		StepDuration: time.Duration(5) * time.Second,
	}
	i := r.Size()
	const expected = 146
	if i != expected {
		t.Errorf("expected %d got %d", expected, i)
	}
}
