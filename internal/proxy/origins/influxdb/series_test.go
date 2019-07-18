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

package influxdb

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Comcast/trickster/pkg/sort/times"

	"github.com/Comcast/trickster/internal/timeseries"

	"github.com/influxdata/influxdb/models"
)

func TestSetExtents(t *testing.T) {
	se := &SeriesEnvelope{}
	ex := timeseries.ExtentList{timeseries.Extent{Start: time.Time{}, End: time.Time{}}}
	se.SetExtents(ex)
	if len(se.ExtentList) != 1 {
		t.Errorf(`expected 1. got %d`, len(se.ExtentList))
	}
}

func TestExtents(t *testing.T) {
	se := &SeriesEnvelope{}
	ex := timeseries.ExtentList{timeseries.Extent{Start: time.Time{}, End: time.Time{}}}
	se.SetExtents(ex)
	e := se.Extents()
	if len(e) != 1 {
		t.Errorf(`expected 1. got %d`, len(se.ExtentList))
	}
}

func TestCopy(t *testing.T) {
	se := &SeriesEnvelope{
		Results: []Result{
			{
				Series: []models.Row{
					{
						Name:    "a",
						Columns: []string{"time", "units"},
						Tags:    map[string]string{"tagName1": "tagValue1"},
						Values: [][]interface{}{
							{float64(1000), 1.5},
							{float64(5000), 1.5},
							{float64(10000), 1.5},
						},
					},
					{
						Name:    "b",
						Columns: []string{"time", "units"},
						Tags:    map[string]string{"tagName2": "tagValue2"},
						Values: [][]interface{}{
							{float64(1000), 2.5},
							{float64(5000), 2.1},
							{float64(10000), 2.4},
						},
					},
				},
			},
		},
	}

	sec := se.Copy().(*SeriesEnvelope)

	if len(sec.Results) != 1 {
		t.Errorf(`expected 1. got %d`, len(sec.Results))
		return
	}

	if len(sec.Results[0].Series) != 2 {
		t.Errorf(`expected 2. got %d`, len(sec.Results[0].Series))
		return
	}

	if len(sec.Results[0].Series[0].Values) != 3 {
		t.Errorf(`expected 3. got %d`, len(sec.Results[0].Series[0].Values))
		return
	}

	if len(sec.Results[0].Series[1].Values) != 3 {
		t.Errorf(`expected 3. got %d`, len(sec.Results[0].Series[1].Values))
		return
	}

}

func TestSetStep(t *testing.T) {
	se := SeriesEnvelope{}
	const step = time.Duration(300) * time.Minute
	se.SetStep(step)
	if se.StepDuration != step {
		t.Errorf(`expected "%s". got "%s"`, step, se.StepDuration)
	}
}

func TestStep(t *testing.T) {
	se := SeriesEnvelope{}
	const step = time.Duration(300) * time.Minute
	se.SetStep(step)
	if se.Step() != step {
		t.Errorf(`expected "%s". got "%s"`, step, se.Step())
	}
}

func TestSeriesCount(t *testing.T) {
	se := &SeriesEnvelope{
		Results: []Result{
			{
				Series: []models.Row{
					{
						Name:    "a",
						Columns: []string{"time", "units"},
						Tags:    map[string]string{"tagName1": "tagValue1"},
						Values: [][]interface{}{
							{float64(10000), 1.5},
						},
					},
				},
			},
		},
	}
	if se.SeriesCount() != 1 {
		t.Errorf("expected 1 got %d.", se.SeriesCount())
	}
}

func TestValueCount(t *testing.T) {
	se := &SeriesEnvelope{
		Results: []Result{
			{
				Series: []models.Row{
					{
						Name:    "a",
						Columns: []string{"time", "units"},
						Tags:    map[string]string{"tagName1": "tagValue1"},
						Values: [][]interface{}{
							{float64(1000), 1.5},
							{float64(5000), 1.5},
							{float64(10000), 1.5},
						},
					},
				},
			},
		},
	}
	if se.ValueCount() != 3 {
		t.Errorf("expected 3 got %d.", se.ValueCount())
	}
}

func TestMerge(t *testing.T) {
	tests := []struct {
		a, b, merged *SeriesEnvelope
	}{
		// Series that adhere to rule
		{
			a: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(15000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(15, 0), End: time.Unix(15, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			b: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(5000), 1.5},
									{float64(10000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(5, 0), End: time.Unix(10, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			merged: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(5000), 1.5},
									{float64(10000), 1.5},
									{float64(15000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(5, 0), End: time.Unix(15, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
				timestamps:   map[time.Time]bool{time.Unix(5, 0): true, time.Unix(10, 0): true, time.Unix(15, 0): true},
				tslist:       times.Times{time.Unix(5, 0), time.Unix(10, 0), time.Unix(15, 0)},
				isSorted:     true,
				isCounted:    true,
			},
		},

		// empty second series
		{
			a: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(10000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10, 0), End: time.Unix(10, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			b: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10, 0), End: time.Unix(10, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			merged: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(10000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10, 0), End: time.Unix(10, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
				timestamps:   map[time.Time]bool{time.Unix(10, 0): true},
				tslist:       times.Times{time.Unix(10, 0)},
				isSorted:     true,
				isCounted:    true,
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.a.Merge(true, test.b)
			if !reflect.DeepEqual(test.merged, test.a) {
				t.Fatalf("Mismatch\nactual=%v\nexpect=%v", test.a, test.merged)
			}
		})
	}
}

func TestCropToSize(t *testing.T) {
	tests := []struct {
		before, after *SeriesEnvelope
		size          int
		bft           time.Time
		extent        timeseries.Extent
	}{
		// case 0: where we already have the number of timestamps we are cropping to
		{
			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(1444004600000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1444004600, 0), End: time.Unix(1444004600, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(1444004600000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1444004600, 0), End: time.Unix(1444004600, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
				timestamps:   map[time.Time]bool{time.Unix(1444004600, 0): true},
				tslist:       times.Times{time.Unix(1444004600, 0)},
				isCounted:    true,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1444004600, 0),
			},
			size: 1,
			bft:  time.Now(),
		},

		// case 1
		{

			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(1444004600000), 1.5},
									{float64(1444004610000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1444004600, 0), End: time.Unix(1444004610, 0)},
				},
				StepDuration: time.Duration(10) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(1444004610000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1444004610, 0), End: time.Unix(1444004610, 0)},
				},
				StepDuration: time.Duration(10) * time.Second,
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
			bft:  time.Now(),
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

func TestCropToRange(t *testing.T) {
	tests := []struct {
		before, after *SeriesEnvelope
		extent        timeseries.Extent
	}{
		// Case where the very first element in the matrix has a timestamp matching the extent's end
		{ // Run 0
			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(1544004600000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1544004600, 0), End: time.Unix(1544004600, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(1544004600000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1544004600, 0), End: time.Unix(1544004600, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1544004600, 0),
			},
		},
		// Case where we trim nothing
		{ // Run 1
			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(1544004600000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1544004600, 0), End: time.Unix(1544004600, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(1544004600000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1544004600, 0), End: time.Unix(1544004600, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1644004600, 0),
			},
		},

		// Case where we trim everything (all data is too old)
		{ // Run 2
			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(1544004600000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1544004600, 0), End: time.Unix(1544004600, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{},
					},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(5) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(10, 0),
			},
		},

		// Case where we trim everything (all data is too early)
		{ // Run 3
			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(100000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(100, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{},
					},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(5) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(10000, 0),
				End:   time.Unix(20000, 0),
			},
		},

		// Case where we trim some off the beginning
		{ // Run 4
			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(100000), 1.5},
									{float64(200000), 1.5},
									{float64(300000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(300000), 1.5},
								},
							},
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
				End:   time.Unix(300, 0),
			},
		},

		// Case where we trim some off the ends
		{ // Run 5
			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(100000), 1.5},
									{float64(200000), 1.5},
									{float64(300000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(200000), 1.5},
								},
							},
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

		// Case where the last datapoint is on the Crop extent
		{ // Run 6
			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(100000), 1.5},
									{float64(200000), 1.5},
									{float64(300000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(200000), 1.5},
									{float64(300000), 1.5},
								},
							},
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

		// Case where we aren't given any datapoints
		{ // Run 7
			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
						},
					},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{},
					},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(100, 0),
				End:   time.Unix(300, 0),
			},
		},

		// Case where we have more series than points
		{ // Run 8
			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(400000), 1.5},
								},
							},
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(400000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(400, 0), End: time.Unix(400, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{},
					},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(100, 0),
				End:   time.Unix(300, 0),
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.before.CropToRange(test.extent)
			if !reflect.DeepEqual(test.before, test.after) {
				t.Errorf("mismatch got=%v expected=%v", test.before, test.after)
			}
		})
	}
}

// func TestSort(t *testing.T) {
// 	tests := []struct {
// 		before, after *SeriesEnvelope
// 		extent        timeseries.Extent
// 	}{
// 		// Case where we trim nothing
// 		{
// 			before: &SeriesEnvelope{
// 				Data: MatrixData{
// 					ResultType: "matrix",
// 					Result: models.Matrix{
// 						&models.SampleStream{
// 							Metric: models.Metric{"__name__": "a"},
// 							Values: []models.SamplePair{
// 								models.SamplePair{Timestamp: 1544004200000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004600000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004800000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004000000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004000000, Value: 1.5}, // sort should also dupe kill
// 							},
// 						},
// 						&models.SampleStream{
// 							Metric: models.Metric{"__name__": "b"},
// 							Values: []models.SamplePair{
// 								models.SamplePair{Timestamp: 1544004600000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004200000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004000000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004800000, Value: 1.5},
// 							},
// 						},
// 						&models.SampleStream{
// 							Metric: models.Metric{"__name__": "c"},
// 							Values: []models.SamplePair{
// 								models.SamplePair{Timestamp: 1544004800000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004200000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004000000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004600000, Value: 1.5},
// 							},
// 						},
// 					},
// 				},
// 			},
// 			after: &SeriesEnvelope{
// 				Data: MatrixData{
// 					ResultType: "matrix",
// 					Result: models.Matrix{
// 						&models.SampleStream{
// 							Metric: models.Metric{"__name__": "a"},
// 							Values: []models.SamplePair{
// 								models.SamplePair{Timestamp: 1544004000000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004200000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004600000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004800000, Value: 1.5},
// 							},
// 						},
// 						&models.SampleStream{
// 							Metric: models.Metric{"__name__": "b"},
// 							Values: []models.SamplePair{
// 								models.SamplePair{Timestamp: 1544004000000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004200000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004600000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004800000, Value: 1.5},
// 							},
// 						},
// 						&models.SampleStream{
// 							Metric: models.Metric{"__name__": "c"},
// 							Values: []models.SamplePair{
// 								models.SamplePair{Timestamp: 1544004000000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004200000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004600000, Value: 1.5},
// 								models.SamplePair{Timestamp: 1544004800000, Value: 1.5},
// 							},
// 						},
// 					},
// 				},
// 			},
// 		},
// 	}

// 	for i, test := range tests {
// 		t.Run(strconv.Itoa(i), func(t *testing.T) {
// 			test.before.Sort()
// 			if !reflect.DeepEqual(test.before, test.after) {
// 				t.Fatalf("mismatch\nexpected=%v\nactual=%v", test.after, test.before)
// 			}
// 		})
// 	}
// }
