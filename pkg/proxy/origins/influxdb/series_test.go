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

package influxdb

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/tricksterproxy/trickster/pkg/sort/times"
	"github.com/tricksterproxy/trickster/pkg/timeseries"

	"github.com/influxdata/influxdb/models"
)

const testStep = time.Duration(10) * time.Second

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

func TestUpdateTimestamps(t *testing.T) {

	// test edge condition here (core functionality is tested across this file)
	se := SeriesEnvelope{isCounted: true}
	se.updateTimestamps()
	if se.timestamps != nil {
		t.Errorf("expected nil map, got size %d", len(se.timestamps))
	}

}

func TestClone(t *testing.T) {
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
		timestamps: map[time.Time]bool{{}: true},
	}

	sec := se.Clone().(*SeriesEnvelope)

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
		// case 0
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

		// case 1 empty second series
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

		// case 2, different series in different envelopes, merge into 1
		{
			a: &SeriesEnvelope{
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
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			b: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			merged: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
				timestamps:   map[time.Time]bool{},
				tslist:       times.Times{},
				isSorted:     true,
				isCounted:    true,
			},
		},
		// case 3, more results[] elements in the incoming envelope - lazy merge them
		{
			a: &SeriesEnvelope{
				Results: []Result{
					{
						StatementID: 0,
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
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			b: &SeriesEnvelope{
				Results: []Result{
					{
						StatementID: 0,
						Series: []models.Row{
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
						},
					},
					{
						StatementID: 1,
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
							{
								Name:    "c",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			merged: &SeriesEnvelope{
				Results: []Result{
					{
						StatementID: 0,
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
						},
					},
					{
						StatementID: 1,
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
							{
								Name:    "c",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
				timestamps:   map[time.Time]bool{},
				tslist:       times.Times{},
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

	now := time.Now().Truncate(testStep)
	nowEpochMs := float64(now.Unix() * 1000)

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
			bft:  now,
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
				StepDuration: testStep,
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
				StepDuration: testStep,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{},
					},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: testStep,
			},
			extent: timeseries.Extent{},
			size:   1,
			bft:    now,
		},

		// case 3 - backfill tolerance
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
									{nowEpochMs, 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1444004600, 0), End: now},
				},
				StepDuration: testStep,
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
					timeseries.Extent{Start: time.Unix(1444004600, 0), End: now.Add(-5 * time.Minute)},
				},
				StepDuration: testStep,
				timestamps:   map[time.Time]bool{time.Unix(1444004600, 0): true},
				tslist:       times.Times{time.Unix(1444004600, 0)},
				isCounted:    true,
				isSorted:     false,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   now,
			},
			size: 1,
			bft:  now.Add(-5 * time.Minute),
		},

		// Case 4 - missing "time" column (we accidentally call it timestamp here)
		{
			before: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"timestamp", "units"},
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
				StepDuration: testStep,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "a",
								Columns: []string{"timestamp", "units"},
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
				StepDuration: testStep,
				timestamps:   map[time.Time]bool{},
				tslist:       times.Times{},
				isCounted:    true,
				isSorted:     false,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1444004610, 0),
			},
			size: 1,
			bft:  now,
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
		{ // Run 0 Case where the very first element in the matrix has a timestamp matching the extent's end
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

		{ // Run 1 Case where we trim nothing
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

		{ // Run 2 Case where we trim everything (all data is too old)
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

		{ // Run 3 Case where we trim everything (all data is too early)
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

		{ // Run 4 Case where we trim some off the beginning
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

		{ // Run 5 Case where we trim some off the ends
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

		{ // Run 6 Case where the last datapoint is on the Crop extent
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

		{ // Run 7 Case where we aren't given any datapoints
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

		{ // Run 8 Case where we have more series than points
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

		// Run 9: Case where after cropping, an inner series is empty/removed
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
									{float64(100000), 1.5},
									{float64(200000), 1.5},
									{float64(300000), 1.5},
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(100000), 1.5},
									{float64(200000), 1.5},
									{float64(300000), 1.5},
								},
							},
							{
								Name:    "c",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(100000), 1.5},
									{float64(200000), 1.5},
									{float64(300000), 1.5},
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(600, 0)},
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
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
							{
								Name:    "c",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
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
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(100000), 1.5},
									{float64(200000), 1.5},
									{float64(300000), 1.5},
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
							{
								Name:    "c",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(100000), 1.5},
									{float64(200000), 1.5},
									{float64(300000), 1.5},
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(600, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
							{
								Name:    "c",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
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
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(100000), 1.5},
									{float64(200000), 1.5},
									{float64(300000), 1.5},
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
							{
								Name:    "c",
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
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(600, 0)},
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
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(400000), 1.5},
									{float64(500000), 1.5},
									{float64(600000), 1.5},
								},
							},
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
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
							{
								Name:    "c",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{{Series: []models.Row{}}},
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

		{ // Run 13 Case where we short circuit since the dataset is empty
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
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values:  [][]interface{}{},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &SeriesEnvelope{
				Results: []Result{
					{
						Series: []models.Row{},
					},
				},
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
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.before.CropToRange(test.extent)
			if !reflect.DeepEqual(test.before, test.after) {
				t.Errorf("mismatch\nexpected=%v\ngot     =%v", test.after, test.before)
			}
		})
	}
}

func TestSort(t *testing.T) {

	se := SeriesEnvelope{isSorted: true, isCounted: false}
	se.Sort()
	if se.isCounted {
		t.Errorf("got %t expected %t", se.isCounted, false)
	}

	tests := []struct {
		before, after *SeriesEnvelope
	}{
		// case 0
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
									{float64(15000), 1.5},
									{float64(5000), 1.5},
									{float64(10000), 1.5},
								},
							},
							{
								Name:    "b",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(15000), 1.5},
									{float64(5000), 1.5},
									{float64(10000), 1.5},
								},
							},
						},
					},
					{
						Series: []models.Row{
							{
								Name:    "c",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(15000), 1.5},
									{float64(5000), 1.5},
									{float64(10000), 1.5},
								},
							},
							{
								Name:    "d",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(15000), 1.5},
									{float64(5000), 1.5},
									{float64(10000), 1.5},
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
				isSorted:     false,
				isCounted:    false,
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
									{float64(5000), 1.5},
									{float64(10000), 1.5},
									{float64(15000), 1.5},
								},
							},
							{
								Name:    "b",
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
					{
						Series: []models.Row{
							{
								Name:    "c",
								Columns: []string{"time", "units"},
								Tags:    map[string]string{"tagName1": "tagValue1"},
								Values: [][]interface{}{
									{float64(5000), 1.5},
									{float64(10000), 1.5},
									{float64(15000), 1.5},
								},
							},
							{
								Name:    "d",
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
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.before.Sort()
			if !reflect.DeepEqual(test.before, test.after) {
				t.Errorf("mismatch\nexpected=%v\ngot     =%v", test.after, test.before)
			}
		})
	}

}

func TestSize(t *testing.T) {
	s := &SeriesEnvelope{
		Results: []Result{
			{
				Series: []models.Row{
					{
						Name:    "a",
						Columns: []string{"time", "units"},
						Tags:    map[string]string{"tagName1": "tagValue1"},
						Values:  [][]interface{}{},
					},
					{
						Name:    "b",
						Columns: []string{"time", "units"},
						Tags:    map[string]string{"tagName1": "tagValue1"},
						Values:  [][]interface{}{},
					},
				},
			},
		},
		ExtentList: timeseries.ExtentList{
			timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)},
		},
		StepDuration: time.Duration(100) * time.Second,
	}

	i := s.Size()
	expected := 226

	if i != expected {
		t.Errorf("expected %d got %d", expected, i)
	}

}

func TestTags(t *testing.T) {
	tags := make(tags)
	if tags.String() != "" {
		t.Error("expected empty string")
	}
}
