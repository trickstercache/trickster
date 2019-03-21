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

	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/prometheus/common/model"
)

func TestSetStep(t *testing.T) {
	me := MatrixEnvelope{}
	const step = time.Duration(300) * time.Minute
	me.SetStep(step)
	if me.StepDuration != step {
		t.Errorf(`wanted "%s". got "%s"`, step, me.StepDuration)
	}
}

func TestStep(t *testing.T) {
	me := MatrixEnvelope{}
	const step = time.Duration(300) * time.Minute
	me.SetStep(step)
	if me.Step() != step {
		t.Errorf(`wanted "%s". got "%s"`, step, me.Step())
	}
}

func TestMerge(t *testing.T) {

}

func TestCrop(t *testing.T) {
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
								model.SamplePair{Timestamp: 1544004600000, Value: 1.5},
							},
						},
					},
				},
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
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(1644004600, 0),
			},
		},
		// Case where we trim everything (all data is too late)
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
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "b"},
							Values: []model.SamplePair{},
						}},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(0, 0),
				End:   time.Unix(10, 0),
			},
		},
		// Case where we trim everything (all data is too early)
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
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "c"},
							Values: []model.SamplePair{},
						},
					}},
			},
			extent: timeseries.Extent{
				Start: time.Unix(10000, 0),
				End:   time.Unix(20000, 0),
			},
		},
		// Case where we trim some off the beginning
		{
			before: &MatrixEnvelope{
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
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "d"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 299000, Value: 1.5},
							},
						},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(200, 0),
				End:   time.Unix(300, 0),
			},
		},
		// Case where we trim some off the end
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "e"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 99000, Value: 1.5},
								model.SamplePair{Timestamp: 199000, Value: 1.5},
								model.SamplePair{Timestamp: 299000, Value: 1.5},
							},
						},
					},
				},
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "e"},
							Values: []model.SamplePair{
								model.SamplePair{Timestamp: 199000, Value: 1.5},
							},
						},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(100, 0),
				End:   time.Unix(200, 0),
			},
		},

		// Case where we aren't given any datapoints
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "f"},
							Values: []model.SamplePair{},
						},
					},
				},
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "f"},
							Values: []model.SamplePair{},
						},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(200, 0),
				End:   time.Unix(300, 0),
			},
		},

		// Case where we have more series than points
		{
			before: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "g"},
							Values: []model.SamplePair{model.SamplePair{Timestamp: 99000, Value: 1.5}},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "g"},
							Values: []model.SamplePair{model.SamplePair{Timestamp: 99000, Value: 1.5}},
						},
					},
				},
			},
			after: &MatrixEnvelope{
				Data: MatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "g"},
							Values: []model.SamplePair{},
						},
						&model.SampleStream{
							Metric: model.Metric{"__name__": "g"},
							Values: []model.SamplePair{},
						},
					},
				},
			},
			extent: timeseries.Extent{
				Start: time.Unix(200, 0),
				End:   time.Unix(300, 0),
			},
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			result := test.before.Crop(test.extent).(*MatrixEnvelope)
			if !reflect.DeepEqual(result, test.after) {
				t.Fatalf("mismatch\nexpected=%v\nactual=%v", test.after, result)
			}
		})
	}
}
