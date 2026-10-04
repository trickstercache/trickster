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

package global

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/stretchr/testify/require"
)

func writeTestResult(t *testing.T, value parser.Value, warnings, infos []string) string {
	t.Helper()
	w := httptest.NewRecorder()
	WriteResult(w, value, warnings, infos)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, contentTypeJSON, w.Header().Get(headers.NameContentType))
	return w.Body.String()
}

func TestWriteResult(t *testing.T) {
	quoted := labels.FromStrings(labels.MetricName, testMetric, testPod, `a"<b>`)
	tests := []struct {
		name  string
		value parser.Value
		want  string
	}{
		{
			name: "matrix",
			value: promql.Matrix{{Metric: quoted, Floats: []promql.FPoint{
				{T: 1500, F: 1}, {T: 2000, F: math.NaN()}, {T: -1005, F: 1e-7}, {T: 3010, F: math.Inf(-1)},
			}}},
			want: `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up",` +
				`"pod":"a\"` + "\\u003cb\\u003e" + `"},"values":[[1.500,"1"],[2,"NaN"],[-1.005,"1e-07"],[3.010,"-Inf"]]}]}}`,
		},
		{
			name:  "vector",
			value: promql.Vector{{Metric: labels.EmptyLabels(), T: 1000, F: 2.5}},
			want:  `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"2.5"]}]}}`,
		},
		{
			name:  "scalar",
			value: promql.Scalar{T: 1000, V: 1e22},
			want:  `{"status":"success","data":{"resultType":"scalar","result":[1,"1e+22"]}}`,
		},
		{
			name:  "string",
			value: promql.String{T: 1000, V: "hi"},
			want:  `{"status":"success","data":{"resultType":"string","result":[1,"hi"]}}`,
		},
		{
			name:  "empty matrix",
			value: promql.Matrix{},
			want:  `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, writeTestResult(t, test.value, nil, nil))
		})
	}
	require.Equal(t, `{"status":"success","data":{"resultType":"vector","result":[]},"warnings":["w"],"infos":["i"]}`,
		writeTestResult(t, promql.Vector{}, []string{"w"}, []string{"i"}))
}

func TestWriteHistograms(t *testing.T) {
	h := &histogram.FloatHistogram{
		Count: 6, Sum: 5, ZeroThreshold: 0.001, ZeroCount: 1,
		PositiveSpans: []histogram.Span{{Offset: 0, Length: 2}}, PositiveBuckets: []float64{2, 0},
		NegativeSpans: []histogram.Span{{Offset: 0, Length: 1}}, NegativeBuckets: []float64{3},
	}
	got := writeTestResult(t, promql.Vector{{Metric: labels.EmptyLabels(), T: 1000, H: h}}, nil, nil)
	require.Equal(t, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"histogram":[1,`+
		`{"count":"6","sum":"5","buckets":[[1,"-1","-0.5","3"],[3,"-0.001","0.001","1"],[0,"0.5","1","2"]]}]}]}}`, got)
	empty := writeTestResult(t, promql.Matrix{{
		Metric:     labels.EmptyLabels(),
		Histograms: []promql.HPoint{{T: 1000, H: &histogram.FloatHistogram{Count: 0, Sum: 0}}},
	}}, nil, nil)
	require.Equal(t, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"histograms":[[1,`+
		`{"count":"0","sum":"0"}]]}]}}`, empty)
	_, err := model.UnmarshalTimeseries([]byte(got), &timeseries.TimeRangeQuery{Statement: testMetric})
	require.NoError(t, err)
}

// The written result decodes as the Prometheus API response Trickster caches and merges.
func TestWriteResultDecodes(t *testing.T) {
	body := writeTestResult(t, promql.Matrix{{
		Metric: podLabels("a"),
		Floats: []promql.FPoint{{T: 60000, F: 1}, {T: 120000, F: 2}},
	}}, []string{"w"}, nil)
	ts, err := model.UnmarshalTimeseries([]byte(body), &timeseries.TimeRangeQuery{Statement: testMetric, Step: time.Minute})
	require.NoError(t, err)
	ds := ts.(*dataset.DataSet)
	require.Equal(t, []string{"w"}, ds.Warnings)
	require.Len(t, ds.Results[0].SeriesList, 1)
	require.Equal(t, 2, ds.Results[0].SeriesList[0].PointCount())
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, http.StatusUnprocessableEntity, ErrorTypeExecution, errors.New(`too "many"`), []string{"w"})
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Equal(t, `{"status":"error","errorType":"execution","error":"too \"many\"","warnings":["w"]}`, w.Body.String())
}

func TestWriteLargeResultIsNotPooled(t *testing.T) {
	floats := make([]promql.FPoint, maxPooledResponse/8)
	m := promql.Matrix{{Metric: labels.EmptyLabels(), Floats: floats}}
	require.Greater(t, len(writeTestResult(t, m, nil, nil)), maxPooledResponse)
	require.LessOrEqual(t, cap(*responseBuffers.Get().(*[]byte)), maxPooledResponse)
}
