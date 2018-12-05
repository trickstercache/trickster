package main

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/prometheus/common/model"
)

func TestPrometheusMatrixEnvelope_CropToRange(t *testing.T) {
	tests := []struct {
		before, after PrometheusMatrixEnvelope
		start, end    int64
	}{
		// Case where we trim nothing
		{
			before: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{1544004600, 1.5},
							},
						},
					},
				},
			},
			after: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{1544004600, 1.5},
							},
						},
					},
				},
			},
			start: 0,
			end:   1644004600,
		},
		// Case where we trim everything (all data is too late)
		{
			before: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{1544004600, 1.5},
							},
						},
					},
				},
			},
			after: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result:     model.Matrix{},
				},
			},
			start: 0,
			end:   10,
		},
		// Case where we trim everything (all data is too early)
		{
			before: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{100, 1.5},
							},
						},
					},
				},
			},
			after: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result:     model.Matrix{},
				},
			},
			start: 10000,
			end:   20000,
		},
		// Case where we trim some off the beginning
		{
			before: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{99, 1.5},
								model.SamplePair{199, 1.5},
								model.SamplePair{299, 1.5},
							},
						},
					},
				},
			},
			after: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{299, 1.5},
							},
						},
					},
				},
			},
			start: 200,
			end:   300,
		},
		// Case where we trim some off the end
		{
			before: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{99, 1.5},
								model.SamplePair{199, 1.5},
								model.SamplePair{299, 1.5},
							},
						},
					},
				},
			},
			after: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{
								model.SamplePair{199, 1.5},
							},
						},
					},
				},
			},
			start: 100,
			end:   200,
		},

		// Case where we aren't given any datapoints
		{
			before: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result: model.Matrix{
						&model.SampleStream{
							Metric: model.Metric{"__name__": "a"},
							Values: []model.SamplePair{},
						},
					},
				},
			},
			after: PrometheusMatrixEnvelope{
				Data: PrometheusMatrixData{
					ResultType: "matrix",
					Result:     model.Matrix{},
				},
			},
			start: 200,
			end:   300,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			test.before.cropToRange(test.start, test.end)
			if !reflect.DeepEqual(test.before, test.after) {
				t.Fatalf("mismatch\nexpected=%v\nactual=%v", test.after, test.before)
			}
		})
	}
}
