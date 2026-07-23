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
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
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
