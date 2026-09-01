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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func testTRQ(plan *QueryPlan) *timeseries.TimeRangeQuery {
	return &timeseries.TimeRangeQuery{
		Statement:   `{"dataSource":"wiki","intervals":["__interval__"]}`,
		Step:        time.Minute,
		StepNS:      time.Minute.Nanoseconds(),
		ParsedQuery: plan,
	}
}

func decodeJSON(t *testing.T, input []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var output any
	if err := decoder.Decode(&output); err != nil {
		t.Fatal(err)
	}
	return output
}

func requireJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	if !reflect.DeepEqual(decodeJSON(t, got), decodeJSON(t, want)) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func requireRoundTrip(t *testing.T, body []byte, plan *QueryPlan, wantSeries int) {
	t.Helper()
	trq := testTRQ(plan)
	ts, err := UnmarshalTimeseries(body, trq)
	if err != nil {
		t.Fatal(err)
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok || len(ds.Results) != 1 || len(ds.Results[0].SeriesList) != wantSeries {
		t.Fatalf("unexpected dataset: %#v", ts)
	}
	options := &timeseries.RequestOptions{ProviderRequest: plan}
	wire, err := MarshalTimeseries(ds, options, 200)
	if err != nil {
		t.Fatal(err)
	}
	requireJSONEqual(t, wire, body)

	modeler := NewModeler()
	cacheBody, err := modeler.CacheMarshaler(ds, options, 200)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := modeler.CacheUnmarshaler(cacheBody, trq)
	if err != nil {
		t.Fatal(err)
	}
	wire, err = MarshalTimeseries(cached, options, 200)
	if err != nil {
		t.Fatal(err)
	}
	requireJSONEqual(t, wire, body)
}

func TestTimeseriesRoundTrip(t *testing.T) {
	body := []byte(`[
		{"timestamp":"2024-01-01T00:00:00.000Z","result":{"count":1,"ratio":1.5,"flag":true,"nested":{"x":2}}},
		{"timestamp":"2024-01-01T00:01:00.000Z","result":{"count":2,"ratio":2.5,"flag":false,"nested":{"x":3}}}
	]`)
	plan := NewQueryPlan(queryTimeseries, nil, []string{"count", "ratio"}, false, nil, nil)
	requireRoundTrip(t, body, plan, 1)
}

func TestGroupByRoundTripPreservesNativeDimensionTypes(t *testing.T) {
	body := []byte(`[
		{"version":"v1","timestamp":"2024-01-01T00:00:00.000Z","event":{"country":"US","device":7,"count":2}},
		{"version":"v1","timestamp":"2024-01-01T00:00:00.000Z","event":{"country":"CA","device":null,"count":1}},
		{"version":"v1","timestamp":"2024-01-01T00:01:00.000Z","event":{"country":"US","device":7,"count":3}}
	]`)
	plan := NewQueryPlan(queryGroupBy, []string{"country", "device"}, []string{"count"}, false, nil, nil)
	requireRoundTrip(t, body, plan, 2)
}

func TestTopNRoundTripPreservesPerBucketRank(t *testing.T) {
	body := []byte(`[
		{"timestamp":"2024-01-01T00:00:00.000Z","result":[{"page":"A","count":5},{"page":"B","count":4}]},
		{"timestamp":"2024-01-01T00:01:00.000Z","result":[{"page":"B","count":7},{"page":"A","count":6}]}
	]`)
	plan := NewQueryPlan(queryTopN, []string{"page"}, []string{"count"}, false, nil, nil)
	requireRoundTrip(t, body, plan, 2)
}

func TestRankedResponsesRoundTripAcrossMergedExtents(t *testing.T) {
	tests := []struct {
		name   string
		plan   *QueryPlan
		first  string
		second string
	}{
		{
			name: "groupBy",
			plan: NewQueryPlan(queryGroupBy, []string{"country"}, []string{"count"}, false, nil, nil),
			first: `[{"version":"v1","timestamp":"2024-01-01T00:00:00.000Z","event":{"country":"US","count":2}},` +
				`{"version":"v1","timestamp":"2024-01-01T00:00:00.000Z","event":{"country":"CA","count":1}}]`,
			second: `[{"version":"v1","timestamp":"2024-01-01T00:01:00.000Z","event":{"country":"CA","count":3}},` +
				`{"version":"v1","timestamp":"2024-01-01T00:01:00.000Z","event":{"country":"US","count":2}}]`,
		},
		{
			name: "topN",
			plan: NewQueryPlan(queryTopN, []string{"page"}, []string{"count"}, false, nil, nil),
			first: `[{"timestamp":"2024-01-01T00:00:00.000Z","result":[{"page":"A","count":5},` +
				`{"page":"B","count":4}]}]`,
			second: `[{"timestamp":"2024-01-01T00:01:00.000Z","result":[{"page":"B","count":7},` +
				`{"page":"A","count":6}]}]`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, err := UnmarshalTimeseries([]byte(test.first), testTRQ(test.plan))
			if err != nil {
				t.Fatal(err)
			}
			right, err := UnmarshalTimeseries([]byte(test.second), testTRQ(test.plan))
			if err != nil {
				t.Fatal(err)
			}
			left.Merge(true, right)
			body, err := MarshalTimeseries(left,
				&timeseries.RequestOptions{ProviderRequest: test.plan}, 200)
			if err != nil {
				t.Fatal(err)
			}
			want := []byte(strings.TrimSuffix(test.first, "]") + "," + strings.TrimPrefix(test.second, "["))
			requireJSONEqual(t, body, want)
		})
	}
}

func TestDescendingRoundTrip(t *testing.T) {
	body := []byte(`[
		{"timestamp":"2024-01-01T00:01:00.000Z","result":{"count":2}},
		{"timestamp":"2024-01-01T00:00:00.000Z","result":{"count":1}}
	]`)
	plan := NewQueryPlan(queryTimeseries, nil, []string{"count"}, true, nil, nil)
	requireRoundTrip(t, body, plan, 1)
}

func TestQueryPlanIsImmutable(t *testing.T) {
	dimensions := []string{"country"}
	values := []string{"count"}
	start := []byte(`{"intervals":`)
	end := []byte(`,"queryType":"timeseries"}`)
	plan := NewQueryPlan(queryTimeseries, dimensions, values, false, start, end)
	dimensions[0], values[0], start[0], end[0] = "changed", "changed", '[', ']'
	gotDimensions, gotValues := plan.Dimensions(), plan.ValueFields()
	gotDimensions[0], gotValues[0] = "mutated", "mutated"
	if plan.Dimensions()[0] != "country" || plan.ValueFields()[0] != "count" {
		t.Fatal("query plan slices are mutable")
	}
	body, err := plan.RenderInterval("2024-01-01/2024-01-02")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"intervals":["2024-01-01/2024-01-02"],"queryType":"timeseries"}` {
		t.Fatalf("rendered body = %s", body)
	}
	if _, err := (*QueryPlan)(nil).RenderInterval("x"); !errors.Is(err, timeseries.ErrInvalidBody) {
		t.Fatalf("nil plan error = %v", err)
	}
}

func TestModelErrors(t *testing.T) {
	plan := NewQueryPlan(queryTimeseries, nil, nil, false, nil, nil)
	tests := []struct {
		name string
		body string
		trq  *timeseries.TimeRangeQuery
	}{
		{"nil query", `[]`, nil},
		{"missing plan", `[]`, &timeseries.TimeRangeQuery{}},
		{"invalid JSON", `{`, testTRQ(plan)},
		{"trailing JSON", `[] {}`, testTRQ(plan)},
		{"missing timestamp", `[{"result":{}}]`, testTRQ(plan)},
		{"bad timestamp", `[{"timestamp":"bad","result":{}}]`, testTRQ(plan)},
		{"wrong result", `[{"timestamp":"2024-01-01T00:00:00Z","result":[]}]`, testTRQ(plan)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := UnmarshalTimeseries([]byte(test.body), test.trq); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := MarshalTimeseries(&dataset.DataSet{}, nil, 200); err == nil {
		t.Fatal("expected missing-plan marshal error")
	}
	if _, err := MarshalTimeseries(nil, nil, 200); err == nil {
		t.Fatal("expected invalid-timeseries marshal error")
	}
}

func TestEmptyAndFractionalTimeseriesRoundTrip(t *testing.T) {
	plan := NewQueryPlan(queryTimeseries, nil, []string{"count"}, false, nil, nil)
	requireRoundTrip(t, []byte(`[]`), plan, 0)
	requireRoundTrip(t, []byte(`[{"timestamp":"2024-01-01T00:00:00.123456789Z","result":{"count":18446744073709551615}}]`), plan, 1)
}

func TestNativeShapeErrors(t *testing.T) {
	tests := []struct {
		name, queryType, body string
	}{
		{"unknown query", "unknown", `[]`},
		{"groupBy event", queryGroupBy, `[{"timestamp":"2024-01-01T00:00:00Z","event":[]}]`},
		{"topN result", queryTopN, `[{"timestamp":"2024-01-01T00:00:00Z","result":{}}]`},
		{"topN row", queryTopN, `[{"timestamp":"2024-01-01T00:00:00Z","result":[1]}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := NewQueryPlan(test.queryType, []string{"dimension"}, nil, false, nil, nil)
			if _, err := UnmarshalTimeseries([]byte(test.body), testTRQ(plan)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestMarshalUsesDataSetPlanAndRejectsUnknownPlan(t *testing.T) {
	plan := NewQueryPlan(queryTimeseries, nil, []string{"count"}, false, nil, nil)
	ts, err := UnmarshalTimeseries([]byte(`[{"timestamp":"2024-01-01T00:00:00Z","result":{"count":1}}]`), testTRQ(plan))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarshalTimeseries(ts, nil, 200); err != nil {
		t.Fatal(err)
	}
	unknown := NewQueryPlan("unknown", nil, nil, false, nil, nil)
	if _, err := MarshalTimeseries(ts, &timeseries.RequestOptions{ProviderRequest: unknown}, 200); err == nil {
		t.Fatal("expected unknown-plan error")
	}
	if err := MarshalTimeseriesWriter(ts, nil, 200, failingWriter{}); err == nil {
		t.Fatal("expected writer error")
	}
}

type failingWriter struct{}

func (failingWriter) Write(_ []byte) (int, error) { return 0, io.ErrClosedPipe }

func TestModelHelpers(t *testing.T) {
	var nilPlan *QueryPlan
	if nilPlan.QueryType() != "" || nilPlan.Dimensions() != nil ||
		nilPlan.ValueFields() != nil || nilPlan.Descending() {
		t.Fatal("nil query plan getters returned values")
	}
	if got := formatTimestamp(0); got != "1970-01-01T00:00:00.000Z" {
		t.Fatalf("millisecond timestamp = %q", got)
	}
	if got := formatTimestamp(123456789); got != "1970-01-01T00:00:00.123456789Z" {
		t.Fatalf("nanosecond timestamp = %q", got)
	}
	for _, test := range []struct {
		value any
		want  int64
	}{
		{int(1), 1}, {int64(2), 2}, {uint64(3), 3}, {float64(4), 4}, {"bad", 0},
	} {
		if got := numericRank(test.value); got != test.want {
			t.Errorf("numericRank(%T(%v)) = %d", test.value, test.value, got)
		}
	}
	normalized := normalizeJSONValue([]any{
		json.Number("18446744073709551615"), json.Number("1.25"),
		json.Number("not-a-number"), map[string]any{"n": json.Number("1")},
	})
	values := normalized.([]any)
	if _, ok := values[0].(uint64); !ok {
		t.Fatalf("uint normalization type = %T", values[0])
	}
	if _, ok := values[1].(float64); !ok || values[2] != "not-a-number" {
		t.Fatalf("number normalization = %#v", values)
	}
	if fieldDataType(uint64(1)) != timeseries.Uint64 ||
		fieldDataType(struct{}{}) != timeseries.Unknown {
		t.Fatal("unexpected field data type")
	}
	if got := tagString(make(chan int)); !strings.HasPrefix(got, "0x") {
		t.Fatalf("fallback tag string = %q", got)
	}
	if stringsCompare("b", "a") != 1 || stringsCompare("a", "a") != 0 {
		t.Fatal("unexpected string comparison")
	}
}

func TestRenderedPointsToleratesSparseDataSet(t *testing.T) {
	plan := NewQueryPlan(queryTimeseries, nil, nil, false, nil, nil)
	ds := &dataset.DataSet{
		TimeRangeQuery: testTRQ(plan),
		Results: dataset.Results{
			nil,
			{SeriesList: dataset.SeriesList{
				nil,
				{Header: dataset.SeriesHeader{
					Tags: dataset.Tags{"tag": "x"},
					ValueFieldsList: timeseries.FieldDefinitions{
						{Name: "missing", Role: timeseries.RoleValue},
					},
				}, Points: dataset.Points{{Epoch: 0}}},
			}},
		},
	}
	body, err := MarshalTimeseries(ds, nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	requireJSONEqual(t, body, []byte(`[{"timestamp":"1970-01-01T00:00:00.000Z","result":{"missing":null}}]`))
}
