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

package prometheus

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Comcast/trickster/pkg/sort/times"

	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/prometheus/common/model"
)

const (
	nonexistantOrigin          = "http://nonexistent-origin:54321"
	exampleQuery               = "/api/v1/query?query=up&time=2015-07-01T20:11:15.781Z"
	exampleRangeQuery          = "/api/v1/query_range?query=up&start=2015-07-01T20:10:30.781Z&end=2015-07-01T20:11:00.781Z&step=15"
	exampleRangeQueryStatement = "up"
	exampleRangeQueryStart     = "2015-07-01T20:10:30.781Z"
	exampleRangeQueryEnd       = "2015-07-01T20:11:00.781Z"
	exampleRangeQueryStep      = "15"

	// this example should have 2 data points later than those in exampleRangeResponse
	exampleResponse = `{
   "status" : "success",
   "data" : {
      "resultType" : "vector",
      "result" : [
         {
            "metric" : {
               "__name__" : "up",
               "job" : "prometheus",
               "instance" : "localhost:9090"
            },
            "value": [ 1435781475.781, "1" ]
         },
         {
            "metric" : {
               "__name__" : "up",
               "job" : "node",
               "instance" : "localhost:9091"
            },
            "value" : [ 1435781475.781, "0" ]
         }
      ]
   }
}`

	// this example should have 6 data points
	// NOTE: Times in this response should end with '.781' not '.000'. Had
	//       to truncate due to how extents are measured in TricksterHandler.
	exampleRangeResponse = `{
   "status" : "success",
   "data" : {
      "resultType" : "matrix",
      "result" : [
         {
            "metric" : {
               "__name__" : "up",
               "job" : "prometheus",
               "instance" : "localhost:9090"
            },
            "values" : [
               [ 1435781430.000, "1" ],
               [ 1435781445.000, "1" ],
               [ 1435781460.000, "1" ]
            ]
         },
         {
            "metric" : {
               "__name__" : "up",
               "job" : "node",
               "instance" : "localhost:9091"
            },
            "values" : [
               [ 1435781430.000, "0" ],
               [ 1435781445.000, "0" ],
               [ 1435781460.000, "1" ]
            ]
         }
      ]
   }
}`
)

func TestSetStep(t *testing.T) {
	me := MatrixEnvelope{}
	const step = time.Duration(300) * time.Minute
	me.SetStep(step)
	if me.StepDuration != step {
		t.Errorf(`expected "%s". got "%s"`, step, me.StepDuration)
	}
}

func TestStep(t *testing.T) {
	me := MatrixEnvelope{}
	const step = time.Duration(300) * time.Minute
	me.SetStep(step)
	if me.Step() != step {
		t.Errorf(`expected "%s". got "%s"`, step, me.Step())
	}
}

func TestMerge(t *testing.T) {
	tests := []struct {
		a, b, merged *MatrixEnvelope
	}{
		// Run 0: Series that adhere to rule
		{
			a: &MatrixEnvelope{
				Status: rvSuccess,
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 10000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10, 0), End: time.Unix(10, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			b: &MatrixEnvelope{
				Status: rvSuccess,
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 5000, Value: 1.5},
								model.SamplePair{Timestamp: 15000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(5, 0), End: time.Unix(5, 0)},
					timeseries.Extent{Start: time.Unix(15, 0), End: time.Unix(15, 0)},
				},
				StepDuration: time.Duration(5) * time.Second,
			},
			merged: &MatrixEnvelope{
				isCounted:  true,
				isSorted:   true,
				tslist:     times.Times{time.Unix(5, 0), time.Unix(10, 0), time.Unix(15, 0)},
				timestamps: map[time.Time]bool{time.Unix(5, 0): true, time.Unix(10, 0): true, time.Unix(15, 0): true},
				Status:     rvSuccess,
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 5000, Value: 1.5},
								model.SamplePair{Timestamp: 10000, Value: 1.5},
								model.SamplePair{Timestamp: 15000, Value: 1.5},
							},
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
			a: &MatrixEnvelope{
				Status: rvSuccess,
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 10000000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(10000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			b: &MatrixEnvelope{
				Status: rvSuccess,
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{},
						},
					},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(5000) * time.Second,
			},
			merged: &MatrixEnvelope{
				isCounted:  true,
				isSorted:   true,
				tslist:     times.Times{time.Unix(10000, 0)},
				timestamps: map[time.Time]bool{time.Unix(10000, 0): true},
				Status:     rvSuccess,
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 10000000, Value: 1.5},
							},
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
			a: &MatrixEnvelope{
				Status: rvSuccess,
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 10000000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(10000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			b: &MatrixEnvelope{
				Status: rvSuccess,
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "c"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 15000000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(15000, 0), End: time.Unix(15000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
			merged: &MatrixEnvelope{
				isCounted:  true,
				isSorted:   true,
				tslist:     times.Times{time.Unix(10000, 0), time.Unix(15000, 0)},
				timestamps: map[time.Time]bool{time.Unix(10000, 0): true, time.Unix(15000, 0): true},
				Status:     rvSuccess,
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 10000000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "c"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 15000000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(10000, 0), End: time.Unix(15000, 0)},
				},
				StepDuration: time.Duration(5000) * time.Second,
			},
		},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.a.Merge(true, test.b)
			if !reflect.DeepEqual(test.merged, test.a) {
				t.Errorf("mismatch\nactual=%v\nexpected=%v", test.a, test.merged)
			}
		})
	}
}
func TestCropToRange(t *testing.T) {
	tests := []struct {
		before, after *MatrixEnvelope
		extent        timeseries.Extent
	}{
		// Run 0: Case where the very first element in the matrix has a timestamp matching the extent's end
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1644004600000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1644004600, 0), End: time.Unix(1644004600, 0)},
				},
				StepDuration: time.Duration(10) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1644004600000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1644004600, 0), End: time.Unix(1644004600, 0)},
				},
				StepDuration: time.Duration(10) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1644004600, 0),
			},
		},
		// Run 1: Case where we trim nothing
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1544004600000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1544004600, 0), End: time.Unix(1544004600, 0)},
				},
				StepDuration: time.Duration(10) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1544004600000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1544004600, 0), End: time.Unix(1544004600, 0)},
				},
				StepDuration: time.Duration(10) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1644004600, 0),
			},
		},
		// Run 2: Case where we trim everything (all data is too late)
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1544004600000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1544004600, 0), End: time.Unix(1544004600, 0)},
				},
				StepDuration: time.Duration(10) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result:     model.Matrix{},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(10) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(10, 0),
			},
		},
		// Run 3: Case where we trim everything (all data is too early)
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "c"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(100, 0)},
				},
				StepDuration: time.Duration(10) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result:     model.Matrix{},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(10) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(10000, 0),
				End:   time.Unix(20000, 0),
			},
		},
		// Run 4: Case where we trim some off the beginning
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "d"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "d"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 300000, Value: 1.5},
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
				End:   time.Unix(400, 0),
			},
		},
		// Run 5: Case where we trim some off the ends
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "e"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "e"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 200000, Value: 1.5},
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
		// Run 6: Case where the last datapoint is on the Crop extent
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "f"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(300, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "f"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
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
		// Run 7: Case where we aren't given any datapoints
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "g"},
							Values: []model.SamplePair{},
						},
					},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result:     model.Matrix{},
				},
				ExtentList:   timeseries.ExtentList{},
				StepDuration: time.Duration(100) * time.Second,
			},
			extent: timeseries.Extent{
				Start: time.Unix(200, 0),
				End:   time.Unix(300, 0),
			},
		},

		// Run 8: Case where we have more series than points
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "h"},
							Values: []model.SamplePair{model.SamplePair{Timestamp: 100000, Value: 1.5}},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "h"},
							Values: []model.SamplePair{model.SamplePair{Timestamp: 100000, Value: 1.5}},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(100, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result:     model.Matrix{},
				},
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
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "c"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(600, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "c"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
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
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "c"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(600, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "c"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
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
		// Run 10: Case where after cropping, the back series is empty/removed
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "c"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 100000, Value: 1.5},
								model.SamplePair{Timestamp: 200000, Value: 1.5},
								model.SamplePair{Timestamp: 300000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(600, 0)},
				},
				StepDuration: time.Duration(100) * time.Second,
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 400000, Value: 1.5},
								model.SamplePair{Timestamp: 500000, Value: 1.5},
								model.SamplePair{Timestamp: 600000, Value: 1.5},
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

func TestCopy(t *testing.T) {

	tests := []struct {
		before *MatrixEnvelope
	}{
		// Run 0
		{
			before: &MatrixEnvelope{
				tslist:     times.Times{time.Unix(1644001200, 0)},
				timestamps: map[time.Time]bool{time.Unix(1644001200, 0): true},
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1644001200000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1644001200, 0), End: time.Unix(1644001200, 0)},
				},
				StepDuration: time.Duration(3600) * time.Second,
			},
		},

		// Run 1
		{
			before: &MatrixEnvelope{
				tslist:     times.Times{time.Unix(1644001200, 0), time.Unix(1644004800, 0)},
				timestamps: map[time.Time]bool{time.Unix(1644001200, 0): true, time.Unix(1644004800, 0): true},
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1644001200000, Value: 1.5},
							},
						},

						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1644001200000, Value: 1.5},
								model.SamplePair{Timestamp: 1644004800000, Value: 1.5},
							},
						},
					},
				},
				ExtentList: timeseries.ExtentList{
					timeseries.Extent{Start: time.Unix(1644001200, 0), End: time.Unix(1644004800, 0)},
				},
				StepDuration: time.Duration(3600) * time.Second,
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			after := test.before.Copy()
			if !reflect.DeepEqual(test.before, after) {
				t.Errorf("mismatch\nexpected %v\ngot      %v", test.before, after)
			}
		})
	}

}

func TestSort(t *testing.T) {
	tests := []struct {
		before, after *MatrixEnvelope
		extent        timeseries.Extent
	}{
		// Case where we trim nothing
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1544004200000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004600000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004800000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004000000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004000000, Value: 1.5}, // sort should also dupe kill
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1544004600000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004200000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004000000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004800000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "c"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1544004800000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004200000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004000000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004600000, Value: 1.5},
							},
						},
					},
				},
			},
			after: &MatrixEnvelope{
				isSorted:  true,
				isCounted: true,
				tslist:    []time.Time{time.Unix(1544004000, 0), time.Unix(1544004200, 0), time.Unix(1544004600, 0), time.Unix(1544004800, 0)},
				timestamps: map[time.Time]bool{time.Unix(1544004000, 0): true, time.Unix(1544004200, 0): true,
					time.Unix(1544004600, 0): true, time.Unix(1544004800, 0): true},
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1544004000000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004200000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004600000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004800000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1544004000000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004200000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004600000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004800000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "c"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 1544004000000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004200000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004600000, Value: 1.5},
								model.SamplePair{Timestamp: 1544004800000, Value: 1.5},
							},
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
				t.Errorf("mismatch\nexpected=%v\nactual=%v", test.after, test.before)
			}
			// test isSorted short circuit
			test.before.Sort()
			if !reflect.DeepEqual(test.before, test.after) {
				t.Errorf("mismatch\nexpected=%v\nactual=%v", test.after, test.before)
			}
		})
	}
}

func TestSetExtents(t *testing.T) {
	me := &MatrixEnvelope{}
	ex := timeseries.ExtentList{timeseries.Extent{Start: time.Time{}, End: time.Time{}}}
	me.SetExtents(ex)
	if len(me.ExtentList) != 1 {
		t.Errorf(`expected 1. got %d`, len(me.ExtentList))
	}
}

func TestExtents(t *testing.T) {
	me := &MatrixEnvelope{}
	ex := timeseries.ExtentList{timeseries.Extent{Start: time.Time{}, End: time.Time{}}}
	me.SetExtents(ex)
	e := me.Extents()
	if len(e) != 1 {
		t.Errorf(`expected 1. got %d`, len(me.ExtentList))
	}
}

func TestSeriesCount(t *testing.T) {
	me := &MatrixEnvelope{
		Data: MatrixData{
			ResultType: "matrix",
			Result: model.Matrix{
				&model.SampleStream{
					Metric: model.Metric{"__name__": "d"},
					Values: []model.SamplePair{
						model.SamplePair{Timestamp: 99000, Value: 1.5},
						model.SamplePair{Timestamp: 199000, Value: 1.5},
						model.SamplePair{Timestamp: 299000, Value: 1.5},
					},
				},
			},
		},
	}
	if me.SeriesCount() != 1 {
		t.Errorf("expected 1 got %d.", me.SeriesCount())
	}
}

func TestValueCount(t *testing.T) {
	me := &MatrixEnvelope{
		Data: MatrixData{
			ResultType: "matrix",
			Result: model.Matrix{
				&model.SampleStream{
					Metric: model.Metric{"__name__": "d"},
					Values: []model.SamplePair{
						model.SamplePair{Timestamp: 99000, Value: 1.5},
						model.SamplePair{Timestamp: 199000, Value: 1.5},
						model.SamplePair{Timestamp: 299000, Value: 1.5},
					},
				},
			},
		},
	}
	if me.ValueCount() != 3 {
		t.Errorf("expected 3 got %d.", me.ValueCount())
	}
}

func TestTimestampCount(t *testing.T) {

	tests := []struct {
		ts       *MatrixEnvelope
		expected int
	}{
		{
			ts: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "d"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 99000, Value: 1.5},
								model.SamplePair{Timestamp: 199000, Value: 1.5},
								model.SamplePair{Timestamp: 299000, Value: 1.5},
							},
						},
					},
				},
			},
			expected: 3,
		},

		{
			ts: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "d"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 99000, Value: 1.5},
								model.SamplePair{Timestamp: 199000, Value: 1.5},
							},
						},
					},
				},
			},
			expected: 2,
		},

		{
			ts: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "d"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 99000, Value: 1.5},
								model.SamplePair{Timestamp: 199000, Value: 1.5},
							},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "e"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 99000, Value: 1.5},
								model.SamplePair{Timestamp: 299000, Value: 1.5},
							},
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
