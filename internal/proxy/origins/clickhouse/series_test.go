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

package clickhouse

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/pkg/sort/times"
)

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

func TestExtremes(t *testing.T) {
	re := &ResultsEnvelope{
		Data: map[string]*DataSet{
			"1": &DataSet{
				Points: []Point{
					Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(10000, 0), Value: 1.5},
				},
			},
		},
	}
	e := re.Extents()
	if len(e) != 1 {
		t.Errorf(`expected 1. got %d`, len(re.ExtentList))
	}
}

func TestCopy(t *testing.T) {
	re := &ResultsEnvelope{
		Meta: []FieldDefinition{
			FieldDefinition{Name: "testMeta", Type: "testType"},
		},
		Data: map[string]*DataSet{
			"1": &DataSet{
				Metric: map[string]interface{}{
					"label1": 20,
					"label2": "red",
				},
				Points: []Point{
					Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(10000, 0), Value: 1.5},
				},
			},
		},
	}

	rec := re.Copy().(*ResultsEnvelope)

	if len(rec.Meta) != 1 {
		t.Errorf(`expected 1. got %d`, len(rec.Meta))
		return
	}

	if len(rec.Data) != 1 {
		t.Errorf(`expected 1. got %d`, len(rec.Data))
		return
	}

	if len(rec.Data["1"].Points) != 3 {
		t.Errorf(`expected 3. got %d`, len(rec.Data["1"].Points))
		return
	}

	if len(rec.Data["1"].Metric) != 2 {
		t.Errorf(`expected 2. got %d`, len(rec.Data["1"].Metric))
		return
	}
}

func TestSetStep(t *testing.T) {
	re := ResultsEnvelope{}
	const step = time.Duration(300) * time.Minute
	re.SetStep(step)
	if re.StepDuration != step {
		t.Errorf(`expected "%s". got "%s"`, step, re.StepDuration)
	}
}

func TestStep(t *testing.T) {
	re := ResultsEnvelope{}
	const step = time.Duration(300) * time.Minute
	re.SetStep(step)
	if re.Step() != step {
		t.Errorf(`expected "%s". got "%s"`, step, re.Step())
	}
}

func TestSeriesCount(t *testing.T) {
	re := &ResultsEnvelope{
		Data: map[string]*DataSet{
			"1": &DataSet{
				Points: []Point{
					Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(10000, 0), Value: 1.5},
				},
			},
			"2": &DataSet{
				Points: []Point{
					Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(10000, 0), Value: 1.5},
				},
			},
			"3": &DataSet{
				Points: []Point{
					Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(10000, 0), Value: 1.5},
				},
			},
		},
	}
	if re.SeriesCount() != 3 {
		t.Errorf("expected 3 got %d.", re.SeriesCount())
	}
}

func TestValueCount(t *testing.T) {
	re := &ResultsEnvelope{
		Data: map[string]*DataSet{
			"1": &DataSet{
				Points: []Point{
					Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(10000, 0), Value: 1.5},
				},
			},
			"2": &DataSet{
				Points: []Point{
					Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
					Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
				},
			},
		},
	}
	if re.ValueCount() != 5 {
		t.Errorf("expected 5 got %d.", re.ValueCount())
	}
}

func TestMerge(t *testing.T) {
	tests := []struct {
		a, b, merged *ResultsEnvelope
	}{
		// Series that adhere to rule
		{
			a: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"1": &DataSet{
						Points: []Point{
							Point{Timestamp: time.Unix(10, 0), Value: 1.5},
						},
					},
				},
			},
			b: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"1": &DataSet{
						Points: []Point{
							Point{Timestamp: time.Unix(1, 0), Value: 1.5},
							Point{Timestamp: time.Unix(5, 0), Value: 1.5},
						},
					},
				},
			},
			merged: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"1": &DataSet{
						Points: []Point{
							Point{Timestamp: time.Unix(1, 0), Value: 1.5},
							Point{Timestamp: time.Unix(5, 0), Value: 1.5},
							Point{Timestamp: time.Unix(10, 0), Value: 1.5},
						},
					},
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
			a: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"1": &DataSet{
						Points: []Point{
							Point{Timestamp: time.Unix(10, 0), Value: 1.5},
						},
					},
				},
			},
			b: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"1": &DataSet{
						Points: []Point{},
					},
				},
			},
			merged: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"1": &DataSet{
						Points: []Point{
							Point{Timestamp: time.Unix(10, 0), Value: 1.5},
						},
					},
				},
				isSorted: true,
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.a.Merge(true, test.b)
			if !reflect.DeepEqual(test.merged, test.a) {
				t.Errorf("Mismatch\nactual=%v\nexpect=%v", test.a, test.merged)
			}
		})
	}
}

func TestCrop(t *testing.T) {
	tests := []struct {
		before, after *ResultsEnvelope
		extent        timeseries.Extent
	}{
		// Case where the very first element in the matrix has a timestamp matching the extent's end
		{
			before: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
						},
					},
				},
			},
			after: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
						},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1544004600, 0),
			},
		},
		// Case where the very first element in the matrix has a timestamp matching the extent's end
		{
			before: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
						},
					},
				},
			},
			after: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
						},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1644004600, 0),
			},
		},

		// Case where we trim nothing
		{
			before: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
						},
					},
				},
			},
			after: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(1544004600, 0), Value: 1.5},
						},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1644004600, 0),
			},
		},

		// Case where we trim everything (all data is too early)
		{
			before: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
						},
					},
				},
			},
			after: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(10000, 0),
				End:   time.Unix(20000, 0),
			},
		},

		// Case where we trim some off the beginning
		{
			before: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(99, 0), Value: 1.5},
							Point{Timestamp: time.Unix(199, 0), Value: 1.5},
							Point{Timestamp: time.Unix(299, 0), Value: 1.5},
						},
					},
				},
			},
			after: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(299, 0), Value: 1.5},
						},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(200, 0),
				End:   time.Unix(300, 0),
			},
		},

		// Case where we trim some off the ends
		{
			before: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(99, 0), Value: 1.5},
							Point{Timestamp: time.Unix(199, 0), Value: 1.5},
							Point{Timestamp: time.Unix(299, 0), Value: 1.5},
						},
					},
				},
			},
			after: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(199, 0), Value: 1.5},
						},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(100, 0),
				End:   time.Unix(200, 0),
			},
		},

		// Case where the last datapoint is on the Crop extent
		{
			before: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(99, 0), Value: 1.5},
							Point{Timestamp: time.Unix(199, 0), Value: 1.5},
							Point{Timestamp: time.Unix(299, 0), Value: 1.5},
						},
					},
				},
			},
			after: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{
							Point{Timestamp: time.Unix(199, 0), Value: 1.5},
							Point{Timestamp: time.Unix(299, 0), Value: 1.5},
						},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(100, 0),
				End:   time.Unix(299, 0),
			},
		},

		// Case where we aren't given any datapoints
		{
			before: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{},
					},
				},
			},
			after: &ResultsEnvelope{
				Meta: []FieldDefinition{
					FieldDefinition{Name: "testMeta", Type: "testType"},
				},
				Data: map[string]*DataSet{
					"1": &DataSet{
						Metric: map[string]interface{}{
							"label1": 20,
							"label2": "red",
						},
						Points: []Point{},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(100, 0),
				End:   time.Unix(299, 0),
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.before.CropToRange(test.extent)
			if !reflect.DeepEqual(test.before, test.after) {
				t.Errorf("mismatch\nexpected=%v\nactual=%v", test.after, test.before)
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
					"1": &DataSet{
						Points: []Point{
							Point{Timestamp: time.Unix(2000, 0), Value: 1.5},
							Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
							Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
						},
					},
					"2": &DataSet{
						Points: []Point{
							Point{Timestamp: time.Unix(10, 0), Value: 1.5},
							Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
							Point{Timestamp: time.Unix(100, 0), Value: 1.5},
						},
					},
					"3": &DataSet{
						Points: []Point{
							Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
							Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
							Point{Timestamp: time.Unix(10000, 0), Value: 1.5},
						},
					},
				},
			},
			after: &ResultsEnvelope{
				Data: map[string]*DataSet{
					"1": &DataSet{
						Points: []Point{
							Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
							Point{Timestamp: time.Unix(2000, 0), Value: 1.5},
							Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
						},
					},
					"2": &DataSet{
						Points: []Point{
							Point{Timestamp: time.Unix(10, 0), Value: 1.5},
							Point{Timestamp: time.Unix(100, 0), Value: 1.5},
							Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
						},
					},
					"3": &DataSet{
						Points: []Point{
							Point{Timestamp: time.Unix(1000, 0), Value: 1.5},
							Point{Timestamp: time.Unix(5000, 0), Value: 1.5},
							Point{Timestamp: time.Unix(10000, 0), Value: 1.5},
						},
					},
				},
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.before.Sort()
			if !reflect.DeepEqual(test.before, test.after) {
				t.Errorf("mismatch\nexpected=%v\nactual=%v", test.after, test.before)
			}
		})
	}
}
