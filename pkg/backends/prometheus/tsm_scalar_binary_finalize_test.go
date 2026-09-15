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

package prometheus

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

type scalarFinalizeValueOperations struct {
	calls int
}

func (*scalarFinalizeValueOperations) MergeValues(any, any, merge.Strategy) (any, bool) {
	return nil, false
}

func (*scalarFinalizeValueOperations) DivideValue(any, float64) (any, bool) {
	return nil, false
}

func (*scalarFinalizeValueOperations) PairingHash(header *dataset.SeriesHeader,
	query string,
) dataset.Hash {
	return header.CalculateHashWithQueryStatement(query)
}

func (*scalarFinalizeValueOperations) FinalizeMerge(*dataset.DataSet, merge.Strategy) {}

func (o *scalarFinalizeValueOperations) ApplyScalarBinary(any, string,
	float64, bool,
) (any, bool) {
	o.calls++
	return `{"count":"4","sum":"8"}`, true
}

func TestPlanTSMMergeScalarBinaryWrapperContents(t *testing.T) {
	const (
		query = "sum(rate(requests[5m])) * 100"
		inner = "sum(rate(requests[5m]))"
	)
	r, _ := http.NewRequest(http.MethodGet,
		"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
	plan := mustTSMMergePlan(t, r, query)
	values, _, _ := params.GetRequestValues(plan.Variants[0].Request)
	if values.Get(promQueryParam) != inner ||
		plan.Variants[0].MergeStrategy != int(merge.StrategySum) ||
		!plan.Finalizer.Enabled || plan.Finalizer.Query != query ||
		plan.UnsupportedWarning != "" || plan.AllowSingleMemberBypass {
		t.Fatalf("scalar binary plan: %#v", plan)
	}
}

func TestFinalizeTSMMergeScalarBinaryWrapper(t *testing.T) {
	t.Run("applies nested arithmetic and drops the metric name", func(t *testing.T) {
		series := rankSeriesWithTags("up", dataset.Tags{"__name__": "up", "job": "api"},
			"2", 100, "3", 200)
		ds := rankDataSet(series)

		(&Client{}).FinalizeTSMMerge("sum by (job) (up) * 100 + 2", ds)

		if got := seriesPointValues(ds)[""]; !equalStrings(got, []string{"202", "302"}) {
			t.Fatalf("values got %v", got)
		}
		if series.Header.Name != "" || series.Header.Tags.JSON() != `{"job":"api"}` ||
			series.Header.QueryStatement != "sum by (job) (up) * 100 + 2" {
			t.Fatalf("header got %#v", series.Header)
		}
	})

	t.Run("filters comparison failures", func(t *testing.T) {
		series := rankSeriesWithTags("", dataset.Tags{"job": "api"},
			"-1", 100, "2", 200)
		ds := rankDataSet(series)

		(&Client{}).FinalizeTSMMerge("sum by (job) (up) > 0", ds)

		if got := seriesPointValues(ds)[""]; !equalStrings(got, []string{"2"}) {
			t.Fatalf("values got %v", got)
		}
	})

	t.Run("returns booleans for bool comparisons", func(t *testing.T) {
		series := rankSeriesWithTags("", dataset.Tags{"job": "api"},
			"-1", 100, "2", 200)
		ds := rankDataSet(series)

		(&Client{}).FinalizeTSMMerge("sum by (job) (up) > bool 0", ds)

		if got := seriesPointValues(ds)[""]; !equalStrings(got, []string{"0", "1"}) {
			t.Fatalf("values got %v", got)
		}
	})

	t.Run("applies unary negation", func(t *testing.T) {
		series := rankSeries("up", "2", 100)
		ds := rankDataSet(series)

		(&Client{}).FinalizeTSMMerge("-sum(up)", ds)

		if got := seriesPointValues(ds)[""]; !equalStrings(got, []string{"-2"}) {
			t.Fatalf("values got %v", got)
		}
	})

	t.Run("uses each point evaluation time", func(t *testing.T) {
		series := rankSeriesWithTags("", dataset.Tags{}, "1726000000.123", 1)
		series.Points[0].Epoch = epoch.FromMilliSecs(1726000015123)
		ds := rankDataSet(series)

		(&Client{}).FinalizeTSMMerge("time() - max(timestamp(up))", ds)

		if got := seriesPointValues(ds)[""]; !equalStrings(got, []string{"15"}) {
			t.Fatalf("values got %v", got)
		}
	})

	t.Run("finalizes rank before scalar arithmetic", func(t *testing.T) {
		ds := rankDataSet(
			rankSeriesWithTags("", dataset.Tags{"pod": "a"}, "1", 100),
			rankSeriesWithTags("", dataset.Tags{"pod": "b"}, "3", 100),
		)

		(&Client{}).FinalizeTSMMerge("topk(1, sum by (pod) (up)) * 8", ds)

		if got := seriesPointValues(ds)[""]; !equalStrings(got, []string{"24"}) {
			t.Fatalf("values got %v", got)
		}
	})

	t.Run("applies histogram arithmetic", func(t *testing.T) {
		series := rankSeries("histogram", `{"count":"2","sum":"4"}`, 100)
		series.Header.ValueFieldsList = timeseries.FieldDefinitions{{Name: histogramFieldName}}
		operations := &scalarFinalizeValueOperations{}
		ds := rankDataSet(series)
		ds.ValueOperations = operations

		(&Client{}).FinalizeTSMMerge("sum(up) * 2", ds)

		if got := seriesPointValues(ds)[""]; !equalStrings(got,
			[]string{`{"count":"4","sum":"8"}`}) {
			t.Fatalf("values got %v", got)
		}
		if operations.calls != 1 {
			t.Fatalf("histogram operations called %d times", operations.calls)
		}
	})

	t.Run("drops a histogram without compatible operations", func(t *testing.T) {
		series := rankSeries("histogram", `{"count":"2","sum":"4"}`, 100)
		series.Header.ValueFieldsList = timeseries.FieldDefinitions{{Name: histogramFieldName}}
		ds := rankDataSet(series)

		(&Client{}).FinalizeTSMMerge("sum(up) * 2", ds)

		if got := ds.Results[0].SeriesList; len(got) != 0 {
			t.Fatalf("series got %v", got)
		}
	})

	t.Run("removes a fully filtered series", func(t *testing.T) {
		ds := rankDataSet(rankSeriesWithTags("", dataset.Tags{"job": "api"}, "-1", 100))

		(&Client{}).FinalizeTSMMerge("sum by (job) (up) > 0", ds)

		if got := ds.Results[0].SeriesList; len(got) != 0 {
			t.Fatalf("series got %v", got)
		}
	})

	t.Run("drops malformed points", func(t *testing.T) {
		series := rankSeries("up", "2", 400)
		series.Points = append(dataset.Points{
			{},
			{Values: []any{1}},
			{Values: []any{"invalid"}},
		}, series.Points...)
		ds := rankDataSet(nil, series)
		ds.Results = append(dataset.Results{nil}, ds.Results...)

		(&Client{}).FinalizeTSMMerge("sum(up) * 2", ds)

		if len(series.Points) != 1 || series.Points[0].Values[0] != "4" {
			t.Fatalf("points got %v", series.Points)
		}
	})
}
