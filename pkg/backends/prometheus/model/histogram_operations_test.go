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

package model

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/stretchr/testify/require"
)

func TestPrometheusHistogramOperationsMergeExplicitBuckets(t *testing.T) {
	left := `{"count":"10","sum":"20","buckets":[` +
		`[0,"0","1","4"],[0,"1","2","6"]]}`
	right := `{"count":"8","sum":"14","buckets":[[0,"0","2","8"]]}`

	value, handled := prometheusValueOperations.MergeValues(left, right, merge.StrategySum)
	require.True(t, handled)
	histogram, err := parseNormalizedHistogram(value)
	require.NoError(t, err)
	require.Equal(t, 18.0, histogram.count)
	require.Equal(t, 34.0, histogram.sum)
	require.Len(t, histogram.buckets, 1)
	require.Equal(t, 0.0, histogram.buckets[0].lower)
	require.Equal(t, 2.0, histogram.buckets[0].upper)
	require.Equal(t, 18.0, histogram.buckets[0].count)
}

func TestPrometheusHistogramOperationsDatasetMergeUpdatesSize(t *testing.T) {
	leftValue := `{"count":"1","sum":"1","buckets":[[0,"0","1","1"]]}`
	rightValue := `{"count":"2","sum":"3","buckets":[[0,"0","1","2"]]}`
	newDataSet := func(value string) *dataset.DataSet {
		return &dataset.DataSet{
			ValueOperations: prometheusValueOperations,
			Results: dataset.Results{{SeriesList: dataset.SeriesList{{
				Header: dataset.SeriesHeader{
					Tags:            dataset.Tags{"service": "api"},
					QueryStatement:  "sum by (service) (requests)",
					ValueFieldsList: timeseries.FieldDefinitions{{Name: fieldNameHistogram}},
				},
				Points: dataset.Points{{
					Epoch:  epoch.Epoch(1),
					Size:   len(value) + 32,
					Values: []any{value},
				}},
			}}}},
		}
	}
	left := newDataSet(leftValue)
	left.MergeWithStrategy(true, int(merge.StrategySum), newDataSet(rightValue))

	series := left.Results[0].SeriesList[0]
	value := series.Points[0].Values[0].(string)
	require.Equal(t, len(value)+32, series.Points[0].Size)
	require.Equal(t, series.Points.Size(), series.PointSize)
}

func TestPrometheusHistogramOperationsMergeSpanBuckets(t *testing.T) {
	left := `{"count":"10","sum":"20","schema":0,"zero_threshold":0.1,` +
		`"zero_count":"1","positive_spans":[{"offset":0,"length":2}],` +
		`"positive_deltas":[2,3]}`
	right := `{"count":"8","sum":"14","schema":0,"zero_threshold":0.1,` +
		`"zero_count":"2","positive_spans":[{"offset":1,"length":1}],` +
		`"positive_deltas":[4]}`

	value, handled := prometheusValueOperations.MergeValues(left, right, merge.StrategySum)
	require.True(t, handled)
	histogram, err := parseNormalizedHistogram(value)
	require.NoError(t, err)
	require.Equal(t, 18.0, histogram.count)
	require.Equal(t, 34.0, histogram.sum)
	require.Len(t, histogram.buckets, 3)
	require.Equal(t, 3.0, histogram.buckets[0].count)
	require.Equal(t, 2.0, histogram.buckets[1].count)
	require.Equal(t, 9.0, histogram.buckets[2].count)
}

func TestHistogramBucketBoundPreservesInfinityBucket(t *testing.T) {
	for _, test := range []struct {
		schema        int
		lastFinite    int32
		firstInfinite int32
	}{
		{schema: 0, lastFinite: 1024, firstInfinite: 1025},
		{schema: -1, lastFinite: 512, firstInfinite: 513},
	} {
		finite, err := histogramBucketBound(test.lastFinite, test.schema, nil)
		require.NoError(t, err)
		require.Equal(t, math.MaxFloat64, finite)
		infinite, err := histogramBucketBound(test.firstInfinite, test.schema, nil)
		require.NoError(t, err)
		require.True(t, math.IsInf(infinite, 1))
	}
}

func TestPrometheusHistogramOperationsDivide(t *testing.T) {
	value := `{"count":"18","sum":"34","buckets":[[0,"0","2","18"]]}`

	divided, handled := prometheusValueOperations.DivideValue(value, 2)
	require.True(t, handled)
	histogram, err := parseNormalizedHistogram(divided)
	require.NoError(t, err)
	require.Equal(t, 9.0, histogram.count)
	require.Equal(t, 17.0, histogram.sum)
	require.Equal(t, 9.0, histogram.buckets[0].count)
}

func TestPrometheusHistogramOperationsMergeOrderIndependent(t *testing.T) {
	histograms := []string{
		`{"count":"3","sum":"4","buckets":[` +
			`[0,"0","1","1"],[0,"1","2","2"]]}`,
		`{"count":"3","sum":"5","buckets":[[0,"0","2","3"]]}`,
		`{"count":"4","sum":"12","buckets":[[0,"2","4","4"]]}`,
	}
	for _, order := range [][3]int{
		{0, 1, 2},
		{0, 2, 1},
		{1, 0, 2},
		{1, 2, 0},
		{2, 0, 1},
		{2, 1, 0},
	} {
		value := any(histograms[order[0]])
		for _, index := range order[1:] {
			var handled bool
			value, handled = prometheusValueOperations.MergeValues(
				value, histograms[index], merge.StrategySum,
			)
			require.True(t, handled, "order %v", order)
		}
		histogram, err := parseNormalizedHistogram(value)
		require.NoError(t, err)
		require.Equal(t, 10.0, histogram.count, "order %v", order)
		require.Equal(t, 21.0, histogram.sum, "order %v", order)
		require.Len(t, histogram.buckets, 2, "order %v", order)
		require.Equal(t, 6.0, histogram.buckets[0].count, "order %v", order)
		require.Equal(t, 4.0, histogram.buckets[1].count, "order %v", order)
	}
}

func TestPrometheusHistogramOperationsDropMixedSamples(t *testing.T) {
	header := dataset.SeriesHeader{
		Name:           "requests",
		Tags:           dataset.Tags{"service": "api"},
		QueryStatement: "sum by (service) (requests)",
	}
	floatHeader := header.Clone()
	floatHeader.ValueFieldsList = timeseries.FieldDefinitions{{Name: "value"}}
	histogramHeader := header.Clone()
	histogramHeader.ValueFieldsList = timeseries.FieldDefinitions{{Name: fieldNameHistogram}}
	ds := &dataset.DataSet{
		ValueOperations: prometheusValueOperations,
		Results: dataset.Results{{SeriesList: dataset.SeriesList{
			{
				Header: floatHeader,
				Points: dataset.Points{
					{Epoch: epoch.Epoch(1), Values: []any{"1"}},
					{Epoch: epoch.Epoch(2), Values: []any{"2"}},
				},
			},
			{
				Header: histogramHeader,
				Points: dataset.Points{
					{Epoch: epoch.Epoch(2), Values: []any{`{"count":"1","sum":"2"}`}},
					{Epoch: epoch.Epoch(3), Values: []any{`{"count":"1","sum":"3"}`}},
				},
			},
		}}},
	}

	ds.FinalizeValueMerge(int(merge.StrategySum))

	require.Len(t, ds.Results[0].SeriesList, 2)
	require.Equal(t, epoch.Epoch(1), ds.Results[0].SeriesList[0].Points[0].Epoch)
	require.Equal(t, epoch.Epoch(3), ds.Results[0].SeriesList[1].Points[0].Epoch)
	require.Equal(t, []string{mixedFloatHistogramWarning}, ds.Warnings)

	ds.FinalizeValueMerge(int(merge.StrategySum))
	require.Equal(t, []string{mixedFloatHistogramWarning}, ds.Warnings)
}

func TestPrometheusHistogramOperationsRejectsInvalidInputs(t *testing.T) {
	ops := prometheusHistogramOperations{}

	_, handled := ops.MergeValues(`{"count":"1","sum":"1"}`, `{"count":"1","sum":"1"}`, merge.StrategyDedup)
	require.False(t, handled)
	_, handled = ops.MergeValues(1, `{"count":"1","sum":"1"}`, merge.StrategySum)
	require.False(t, handled)
	_, handled = ops.MergeValues(`{"count":"1","sum":"1"}`, `{`, merge.StrategySum)
	require.False(t, handled)

	_, handled = ops.DivideValue(`{"count":"1","sum":"1"}`, 0)
	require.False(t, handled)
	_, handled = ops.DivideValue(nil, 2)
	require.False(t, handled)

	ops.FinalizeMerge(&dataset.DataSet{}, merge.StrategyDedup)
}

func TestParseNormalizedHistogramEdgeCases(t *testing.T) {
	_, err := parseNormalizedHistogram(123)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"x","sum":"1"}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"x"}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","buckets":[[0,"0","1"]]}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","buckets":[[9,"0","1","1"]]}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","buckets":[[0,"x","1","1"]]}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","buckets":[[0,"0","x","1"]]}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","buckets":[[0,"0","1","x"]]}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","buckets":[[0,"2","1","1"]]}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","zero_count":"x"}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","schema":0,` +
		`"positive_spans":[{"offset":0,"length":1}],"positive_deltas":[1,2]}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","schema":0,` +
		`"positive_spans":[{"offset":0,"length":1}],"positive_counts":["1","2"]}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","schema":99,` +
		`"positive_spans":[{"offset":0,"length":1}],"positive_deltas":[1]}`)
	require.Error(t, err)

	hist, err := parseNormalizedHistogram(`{"count":"3","sum":"4","schema":0,"zero_threshold":0.5,` +
		`"zero_count":"","positive_spans":[{"offset":0,"length":1}],"positive_counts":["2"],` +
		`"negative_spans":[{"offset":0,"length":1}],"negative_deltas":[1]}`)
	require.NoError(t, err)
	require.Equal(t, 3.0, hist.count)
	require.GreaterOrEqual(t, len(hist.buckets), 2)

	// Clamp span bucket edges that fall inside the zero threshold.
	hist, err = parseNormalizedHistogram(`{"count":"2","sum":"3","schema":0,"zero_threshold":2,` +
		`"positive_spans":[{"offset":0,"length":1}],"positive_deltas":[1],` +
		`"negative_spans":[{"offset":0,"length":1}],"negative_deltas":[1]}`)
	require.NoError(t, err)
	for _, bucket := range hist.buckets {
		if bucket.lower > 0 {
			require.GreaterOrEqual(t, bucket.lower, 2.0)
		}
		if bucket.upper < 0 {
			require.LessOrEqual(t, bucket.upper, -2.0)
		}
	}

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","schema":0,` +
		`"positive_spans":[{"offset":0,"length":1}],"positive_counts":["x"]}`)
	require.Error(t, err)

	_, err = parseNormalizedHistogram(`{"count":"1","sum":"1","schema":-53,"custom_values":[1],` +
		`"positive_spans":[{"offset":5,"length":1}],"positive_deltas":[1]}`)
	require.Error(t, err)

	// Inclusive boundary modes and zero-count bucket omission.
	value, err := marshalNormalizedHistogram(normalizedHistogram{
		count: 1,
		sum:   1,
		buckets: []normalizedHistogramBucket{
			{lower: 0, upper: 1, count: 0, upperInclusive: true},
			{lower: 1, upper: 2, count: 1, lowerInclusive: true},
			{lower: 2, upper: 3, count: 1, lowerInclusive: true, upperInclusive: true},
			{lower: 3, upper: 4, count: 1},
		},
	})
	require.NoError(t, err)
	require.Contains(t, value, `[1,`)
	require.Contains(t, value, `[3,`)
	require.Contains(t, value, `[2,`)
	require.NotContains(t, value, `"0","1","0"`)
}

func TestHistogramNumberHelpers(t *testing.T) {
	v, err := parseHistogramNumber("", true)
	require.NoError(t, err)
	require.Equal(t, 0.0, v)

	_, err = parseHistogramNumber("", false)
	require.Error(t, err)

	for _, in := range []any{"1.5", float64(2), float32(3), 4, int32(5), int64(6), json.Number("7")} {
		got, err := histogramNumber(in)
		require.NoError(t, err, "%T", in)
		require.NotZero(t, got, "%T", in)
	}
	_, err = histogramNumber(true)
	require.Error(t, err)

	require.Equal(t, "0.000001", formatHistogramNumber(1e-6))
	require.Contains(t, formatHistogramNumber(1e-7), "e")
	require.Contains(t, formatHistogramNumber(1e21), "e")
}

func TestHistogramBucketBoundCustomSchema(t *testing.T) {
	negInf, err := histogramBucketBound(-1, -53, []float64{1, 2, 4})
	require.NoError(t, err)
	require.True(t, math.IsInf(negInf, -1))

	got, err := histogramBucketBound(1, -53, []float64{1, 2, 4})
	require.NoError(t, err)
	require.Equal(t, 2.0, got)

	posInf, err := histogramBucketBound(3, -53, []float64{1, 2, 4})
	require.NoError(t, err)
	require.True(t, math.IsInf(posInf, 1))

	_, err = histogramBucketBound(-2, -53, []float64{1, 2, 4})
	require.Error(t, err)
	_, err = histogramBucketBound(4, -53, []float64{1, 2, 4})
	require.Error(t, err)

	bucket, err := spanHistogramBucket(0, 2, true, -53, []float64{1, 2, 4})
	require.NoError(t, err)
	require.True(t, bucket.lowerInclusive)
	require.True(t, bucket.upperInclusive)

	bucket, err = spanHistogramBucket(1, 3, false, 0, nil)
	require.NoError(t, err)
	require.True(t, bucket.lowerInclusive)
	require.False(t, bucket.upperInclusive)
	require.Less(t, bucket.lower, 0.0)
}

func TestCoarsenHistogramBucketsSmall(t *testing.T) {
	require.Empty(t, coarsenHistogramBuckets(nil, nil))
	one := []normalizedHistogramBucket{{lower: 0, upper: 1, count: 1}}
	require.Equal(t, one, coarsenHistogramBuckets(one, nil))
}
