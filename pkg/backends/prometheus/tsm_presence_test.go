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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	responsemerge "github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	promengine "github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const (
	presenceMinute = int64(time.Minute / time.Millisecond)
	// fixture minutes count from this minute-aligned time in 2023
	presenceBaseMinute = 28333334

	presenceMetric        = "x"
	presenceOtherMetric   = "y"
	presenceUnmatched     = "z"
	presencePodLabel      = "pod"
	presenceNSLabel       = "ns"
	presenceInstanceLabel = "instance"
	presenceBackupLabel   = "isBackup"
	presenceBackupValue   = "true"
	presencePodA          = "a"
	presencePodB          = "b"
	presencePodC          = "c"
	presencePodD          = "d"
	presenceNS1           = "n1"
	presenceNS2           = "n2"
	presenceInstance1     = "i1"
	presenceInstance2     = "i2"
	presenceInstance3     = "i3"

	presenceInner        = "count by (pod) (x)"
	presenceQuery        = "count(" + presenceInner + ")"
	presenceGroupedInner = "count by (ns, pod) (x)"
	presenceGroupedQuery = "count by (ns) (" + presenceGroupedInner + ")"
	presenceSortedQuery  = "sort_desc(" + presenceGroupedQuery + ")"
)

func presenceMillis(minute int) int64 {
	return (presenceBaseMinute + int64(minute)) * presenceMinute
}

// presenceSpan is the one-minute samples a member holds for a series, from minute from up to minute to
type presenceSpan struct{ member, from, to int }

type presenceFixtureSeries struct {
	labels labels.Labels
	spans  []presenceSpan
	nan    bool
	hist   bool
}

type presenceTopology struct {
	name    string
	members int
	series  []presenceFixtureSeries
}

type presenceEval struct {
	at, start, end, step int // minutes; a zero step is an instant query at minute at
}

type presencePoint struct {
	ms    int64
	value float64
}

// presenceResult is a query's points by label set, and the values of an instant vector in its order
type presenceResult struct {
	series map[string][]presencePoint
	order  []float64
}

// presencePod returns a fixture series of metric for pod, with further label pairs
func presencePod(spans []presenceSpan, metric, pod string, kv ...string) presenceFixtureSeries {
	return presenceFixtureSeries{
		labels: labels.FromStrings(append([]string{labels.MetricName, metric, presencePodLabel, pod}, kv...)...),
		spans:  spans,
	}
}

func presenceNaN(series presenceFixtureSeries) presenceFixtureSeries {
	series.nan = true
	return series
}

func presenceHist(series presenceFixtureSeries) presenceFixtureSeries {
	series.hist = true
	return series
}

func onMembers(from, to int, members ...int) []presenceSpan {
	spans := make([]presenceSpan, len(members))
	for i, member := range members {
		spans[i] = presenceSpan{member: member, from: from, to: to}
	}
	return spans
}

var presenceTopologies = []presenceTopology{
	{name: "reported partial overlap", members: 2, series: []presenceFixtureSeries{
		presencePod(onMembers(0, 30, 0), presenceMetric, presencePodA,
			presenceNSLabel, presenceNS1, presenceInstanceLabel, presenceInstance1),
		presencePod(onMembers(0, 30, 0, 1), presenceMetric, presencePodB,
			presenceNSLabel, presenceNS1, presenceInstanceLabel, presenceInstance1),
		presencePod(onMembers(0, 30, 1), presenceMetric, presencePodC,
			presenceNSLabel, presenceNS2, presenceInstanceLabel, presenceInstance2),
	}},
	{name: "full replicas", members: 2, series: []presenceFixtureSeries{
		presencePod(onMembers(0, 30, 0, 1), presenceMetric, presencePodA, presenceNSLabel, presenceNS1),
		presencePod(onMembers(0, 30, 0, 1), presenceMetric, presencePodB, presenceNSLabel, presenceNS2),
	}},
	{name: "disjoint", members: 2, series: []presenceFixtureSeries{
		presencePod(onMembers(0, 30, 0), presenceMetric, presencePodA, presenceNSLabel, presenceNS1),
		presencePod(onMembers(0, 30, 0), presenceMetric, presencePodB, presenceNSLabel, presenceNS1),
		presencePod(onMembers(0, 30, 1), presenceMetric, presencePodC, presenceNSLabel, presenceNS2),
		presencePod(onMembers(0, 30, 1), presenceMetric, presencePodD, presenceNSLabel, presenceNS2),
	}},
	{name: "copies within one member", members: 2, series: []presenceFixtureSeries{
		presencePod(onMembers(0, 30, 0), presenceMetric, presencePodA, presenceInstanceLabel, presenceInstance1),
		presencePod(onMembers(0, 30, 0), presenceMetric, presencePodA, presenceInstanceLabel, presenceInstance2),
		presencePod(onMembers(0, 30, 1), presenceMetric, presencePodA, presenceInstanceLabel, presenceInstance3),
		presencePod(onMembers(0, 30, 1), presenceMetric, presencePodB, presenceInstanceLabel, presenceInstance1),
	}},
	{name: "presence changes over time", members: 2, series: []presenceFixtureSeries{
		presencePod([]presenceSpan{{0, 0, 10}, {1, 20, 30}}, presenceMetric, presencePodA,
			presenceNSLabel, presenceNS1),
		presencePod(onMembers(5, 25, 0), presenceMetric, presencePodB, presenceNSLabel, presenceNS1),
		presencePod(onMembers(12, 18, 1), presenceMetric, presencePodC, presenceNSLabel, presenceNS2),
		presencePod([]presenceSpan{{0, 0, 15}, {1, 10, 30}}, presenceMetric, presencePodD,
			presenceNSLabel, presenceNS2),
	}},
	{name: "backup copies", members: 2, series: []presenceFixtureSeries{
		presencePod(onMembers(0, 30, 0), presenceMetric, presencePodA),
		presencePod(onMembers(0, 30, 0), presenceMetric, presencePodB),
		presencePod(onMembers(0, 30, 0), presenceMetric, presencePodC, presenceBackupLabel, presenceBackupValue),
		presencePod(onMembers(0, 30, 1), presenceMetric, presencePodC),
		presencePod(onMembers(0, 30, 1), presenceMetric, presencePodB, presenceBackupLabel, presenceBackupValue),
	}},
	{name: "nan and native histograms", members: 2, series: []presenceFixtureSeries{
		presenceNaN(presencePod(onMembers(0, 30, 0), presenceMetric, presencePodA)),
		presenceHist(presencePod(onMembers(0, 30, 0, 1), presenceMetric, presencePodB)),
		presenceHist(presencePod(onMembers(0, 20, 1), presenceMetric, presencePodC)),
		presencePod(onMembers(10, 30, 0, 1), presenceMetric, presencePodD),
	}},
	{name: "metric names", members: 2, series: []presenceFixtureSeries{
		presencePod(onMembers(0, 30, 0), presenceMetric, presencePodA),
		presencePod(onMembers(0, 30, 1), presenceOtherMetric, presencePodA),
		presencePod(onMembers(0, 30, 0, 1), presenceOtherMetric, presencePodB),
	}},
	{name: "member without matches", members: 2, series: []presenceFixtureSeries{
		presencePod(onMembers(0, 30, 0), presenceMetric, presencePodA, presenceNSLabel, presenceNS1),
		presencePod(onMembers(0, 30, 0), presenceUnmatched, presencePodB, presenceNSLabel, presenceNS1),
	}},
	{name: "three members", members: 3, series: []presenceFixtureSeries{
		presencePod([]presenceSpan{{0, 0, 12}, {1, 8, 20}, {2, 18, 30}}, presenceMetric, presencePodA,
			presenceNSLabel, presenceNS1),
		presencePod(onMembers(0, 30, 2), presenceMetric, presencePodB, presenceNSLabel, presenceNS1),
		presencePod(onMembers(5, 15, 0, 1, 2), presenceMetric, presencePodC, presenceNSLabel, presenceNS2),
	}},
	{name: "no series", members: 2},
}

var presenceQueries = []string{
	presenceQuery,
	"count(group by (pod) (x))",
	"group(count by (pod) (x))",
	"group(group by (pod) (x))",
	presenceGroupedQuery,
	"count without (pod) (count by (ns, pod) (x))",
	"count(count without (instance) (x))",
	"count(count(x))",
	"count(count by (pod, isBackup) (x))",
	`count(count by (__name__) ({__name__=~"x|y"}))`,
	"count(count by (pod) (x offset 5m))",
	"count(count by (pod) (x @ " + strconv.FormatInt(presenceMillis(10)/1000, 10) + "))",
	presenceSortedQuery,
	"sort(" + presenceGroupedQuery + ")",
	presenceQuery + " > 1",
	presenceQuery + " * 2",
}

var presenceEvals = []presenceEval{
	{at: 0},
	{at: 4},
	{at: 9},
	{at: 13},
	{at: 16},
	{at: 22},
	{at: 29},
	{at: 33},
	{at: 40},
	{start: 0, end: 40, step: 1},
	{start: 3, end: 37, step: 2},
}

type presenceSample struct {
	t int64
	f float64
	h *histogram.Histogram
}

func (s presenceSample) T() int64                      { return s.t }
func (s presenceSample) ST() int64                     { return 0 }
func (s presenceSample) F() float64                    { return s.f }
func (s presenceSample) H() *histogram.Histogram       { return s.h }
func (s presenceSample) FH() *histogram.FloatHistogram { return nil }

func (s presenceSample) Type() chunkenc.ValueType {
	if s.h != nil {
		return chunkenc.ValHistogram
	}
	return chunkenc.ValFloat
}

func (s presenceSample) Copy() chunks.Sample {
	if s.h != nil {
		s.h = s.h.Copy()
	}
	return s
}

type presenceSeriesSet struct {
	series []storage.Series
	next   int
}

func (s *presenceSeriesSet) Next() bool                        { s.next++; return s.next <= len(s.series) }
func (s *presenceSeriesSet) At() storage.Series                { return s.series[s.next-1] }
func (s *presenceSeriesSet) Err() error                        { return nil }
func (s *presenceSeriesSet) Warnings() annotations.Annotations { return nil }

// presenceStore is an in-memory TSDB of series sorted by their labels
type presenceStore []storage.Series

func (s presenceStore) Querier(int64, int64) (storage.Querier, error) {
	return &storage.MockQuerier{SelectMockFunction: func(_ bool, _ *storage.SelectHints,
		matchers ...*labels.Matcher,
	) storage.SeriesSet {
		set := &presenceSeriesSet{}
		for _, series := range s {
			lset := series.Labels()
			if !slices.ContainsFunc(matchers, func(m *labels.Matcher) bool { return !m.Matches(lset.Get(m.Name)) }) {
				set.series = append(set.series, series)
			}
		}
		return set
	}}, nil
}

func presenceHistogram() *histogram.Histogram {
	return &histogram.Histogram{
		Count: 4, Sum: 5, Schema: 0,
		PositiveSpans:   []histogram.Span{{Offset: 0, Length: 2}},
		PositiveBuckets: []int64{2, 0},
	}
}

func presenceListSeries(series presenceFixtureSeries, minutes []int) storage.Series {
	samples := make([]chunks.Sample, len(minutes))
	for i, minute := range minutes {
		sample := presenceSample{t: presenceMillis(minute), f: 1}
		switch {
		case series.hist:
			sample.h = presenceHistogram()
		case series.nan:
			sample.f = math.NaN()
		}
		samples[i] = sample
	}
	return storage.NewListSeries(series.labels, samples)
}

// presenceStorages loads each member's samples, and the union of every member's samples as one TSDB
func presenceStorages(t *testing.T, topology presenceTopology) ([]storage.Queryable, storage.Queryable) {
	t.Helper()
	members := make([]presenceStore, topology.members)
	var union presenceStore
	for _, series := range topology.series {
		var all []int
		for _, span := range series.spans {
			var minutes []int
			for minute := span.from; minute < span.to; minute++ {
				minutes = append(minutes, minute)
			}
			members[span.member] = append(members[span.member], presenceListSeries(series, minutes))
			all = append(all, minutes...)
		}
		slices.Sort(all)
		union = append(union, presenceListSeries(series, slices.Compact(all)))
	}
	byLabels := func(a, b storage.Series) int { return labels.Compare(a.Labels(), b.Labels()) }
	queryables := make([]storage.Queryable, len(members))
	for i := range members {
		// a member holds a series once, as its spans of one series never overlap in these fixtures
		slices.SortFunc(members[i], byLabels)
		queryables[i] = members[i]
	}
	slices.SortFunc(union, byLabels)
	return queryables, union
}

func newPresenceEngine() *promengine.Engine {
	return promengine.NewEngine(promengine.EngineOpts{
		MaxSamples:           10_000_000,
		Timeout:              time.Minute,
		EnableAtModifier:     true,
		EnableNegativeOffset: true,
	})
}

// evalPresence runs query on q, handing the result to read before the engine releases it
func evalPresence(t *testing.T, eng *promengine.Engine, q storage.Queryable, query string,
	ev presenceEval, read func(parser.Value),
) {
	t.Helper()
	ctx := context.Background()
	var qry promengine.Query
	var err error
	if ev.step == 0 {
		qry, err = eng.NewInstantQuery(ctx, q, nil, query, time.UnixMilli(presenceMillis(ev.at)))
	} else {
		qry, err = eng.NewRangeQuery(ctx, q, nil, query, time.UnixMilli(presenceMillis(ev.start)),
			time.UnixMilli(presenceMillis(ev.end)), time.Duration(ev.step)*time.Minute)
	}
	require.NoError(t, err, query)
	defer qry.Close()
	res := qry.Exec(ctx)
	require.NoError(t, res.Err, query)
	read(res.Value)
}

func enginePresenceResult(t *testing.T, value parser.Value) presenceResult {
	t.Helper()
	out := presenceResult{series: make(map[string][]presencePoint)}
	switch v := value.(type) {
	case promengine.Vector:
		for _, sample := range v {
			require.Nil(t, sample.H, "a count is never a histogram")
			out.series[sample.Metric.String()] = []presencePoint{{ms: sample.T, value: sample.F}}
			out.order = append(out.order, sample.F)
		}
	case promengine.Matrix:
		for _, series := range v {
			points := make([]presencePoint, len(series.Floats))
			for i, p := range series.Floats {
				points[i] = presencePoint{ms: p.T, value: p.F}
			}
			out.series[series.Metric.String()] = points
		}
	default:
		t.Fatalf("unexpected result type %T", value)
	}
	return out
}

type presenceAPISample struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value,omitempty"`
	Values [][]any           `json:"values,omitempty"`
}

type presenceAPIResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string              `json:"resultType"`
		Result     []presenceAPISample `json:"result"`
	} `json:"data"`
}

func presenceAPIPoint(ms int64, value float64) []any {
	return []any{float64(ms) / 1000, strconv.FormatFloat(value, 'f', -1, 64)}
}

// presenceAPIBody encodes an engine result as the Prometheus HTTP API would return it
func presenceAPIBody(t *testing.T, value parser.Value) []byte {
	t.Helper()
	var resp presenceAPIResponse
	resp.Status = "success"
	resp.Data.Result = make([]presenceAPISample, 0)
	switch v := value.(type) {
	case promengine.Vector:
		resp.Data.ResultType = "vector"
		for _, sample := range v {
			resp.Data.Result = append(resp.Data.Result, presenceAPISample{
				Metric: sample.Metric.Map(), Value: presenceAPIPoint(sample.T, sample.F),
			})
		}
	case promengine.Matrix:
		resp.Data.ResultType = "matrix"
		for _, series := range v {
			values := make([][]any, len(series.Floats))
			for i, p := range series.Floats {
				values[i] = presenceAPIPoint(p.T, p.F)
			}
			resp.Data.Result = append(resp.Data.Result, presenceAPISample{
				Metric: series.Metric.Map(), Values: values,
			})
		}
	default:
		t.Fatalf("unexpected member result type %T", value)
	}
	body, err := json.Marshal(resp)
	require.NoError(t, err)
	return body
}

func datasetPresenceResult(t *testing.T, ds *dataset.DataSet) presenceResult {
	t.Helper()
	out := presenceResult{series: make(map[string][]presencePoint)}
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil || series.PointCount() == 0 {
				continue
			}
			pts := dspoints.Of(series)
			points := make([]presencePoint, len(pts))
			for i, p := range pts {
				value, err := strconv.ParseFloat(p.Values[0].(string), 64)
				require.NoError(t, err)
				points[i] = presencePoint{ms: int64(p.Epoch) / int64(time.Millisecond), value: value}
			}
			out.series[labels.FromMap(series.Header.Tags).String()] = points
			if ds.Step() == 0 {
				out.order = append(out.order, points[0].value)
			}
		}
	}
	return out
}

func presencePlanRequest(query string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet,
		"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
	return r
}

// tricksterPresenceResult plans query as TSM does, evaluates each member's fanout query on that member,
// merges the members' API responses as TSM merges them, and finalizes the merged result
func tricksterPresenceResult(t *testing.T, eng *promengine.Engine, members []storage.Queryable,
	query string, ev presenceEval,
) presenceResult {
	t.Helper()
	plan := mustTSMMergePlan(t, presencePlanRequest(query), query)
	require.Len(t, plan.Variants, 1, query)
	require.Equal(t, int(merge.StrategyDedup), plan.Variants[0].MergeStrategy, query)
	require.True(t, plan.Finalizer.Enabled, query)
	require.Empty(t, plan.UnsupportedWarning, query)
	values, _, _ := params.GetRequestValues(plan.Variants[0].Request)
	fanout := values.Get(promQueryParam)

	accumulator := responsemerge.NewAccumulator()
	mergeFunc := responsemerge.TimeseriesMergeFuncWithStrategy(nil, plan.Variants[0].MergeStrategy)
	trq := &timeseries.TimeRangeQuery{Statement: fanout, Step: time.Duration(ev.step) * time.Minute}
	for i, member := range members {
		var body []byte
		evalPresence(t, eng, member, fanout, ev, func(v parser.Value) { body = presenceAPIBody(t, v) })
		ts, err := model.UnmarshalTimeseries(body, trq.Clone())
		require.NoError(t, err)
		require.NoError(t, mergeFunc(accumulator, ts, i))
	}
	ds, ok := accumulator.GetTSData().(*dataset.DataSet)
	require.True(t, ok)
	(&Client{}).FinalizeTSMMerge(plan.Finalizer.Query, ds)
	return datasetPresenceResult(t, ds)
}

func TestPresenceAggregationMatchesPrometheus(t *testing.T) {
	eng := newPresenceEngine()
	for _, topology := range presenceTopologies {
		t.Run(topology.name, func(t *testing.T) {
			members, union := presenceStorages(t, topology)
			for _, query := range presenceQueries {
				for _, ev := range presenceEvals {
					var want presenceResult
					evalPresence(t, eng, union, query, ev, func(v parser.Value) {
						want = enginePresenceResult(t, v)
					})
					got := tricksterPresenceResult(t, eng, members, query, ev)
					const at = "%s at %+v"
					require.Equal(t, want.series, got.series, at, query, ev)
					if strings.HasPrefix(query, "sort") {
						require.Equal(t, want.order, got.order, at, query, ev)
					}
				}
			}
		})
	}
}

// The reported topology counts each pod once, where summing regional counts counted pod b twice.
func TestPresenceAggregationReportedCount(t *testing.T) {
	eng := newPresenceEngine()
	members, _ := presenceStorages(t, presenceTopologies[0])
	got := tricksterPresenceResult(t, eng, members, presenceQuery, presenceEval{at: 10})
	require.Equal(t, map[string][]presencePoint{"{}": {{ms: presenceMillis(10), value: 3}}}, got.series)
}

func TestPlanTSMMergePresenceContents(t *testing.T) {
	r := presencePlanRequest(presenceSortedQuery)
	plan := mustTSMMergePlan(t, r, presenceSortedQuery)
	values, _, _ := params.GetRequestValues(plan.Variants[0].Request)
	require.Equal(t, presenceGroupedInner, values.Get(promQueryParam))
	require.NotSame(t, r, plan.Variants[0].Request)
	require.Equal(t, merge.TSMFinalizerSpec{Enabled: true, Query: presenceSortedQuery}, plan.Finalizer)
	require.True(t, plan.StripInjectedLabels)
	require.False(t, plan.AllowSingleMemberBypass)
	require.Equal(t, merge.TSMCompletenessResponseAuthority, plan.Completeness)
}

func TestPlanTSMMergePresencePOST(t *testing.T) {
	const (
		step     = "15"
		origBody = "query=count%28count+by+%28pod%29+%28x%29%29&start=1000&end=2000&step=" + step
	)
	r, _ := http.NewRequest(http.MethodPost, "http://example.com/api/v1/query_range",
		io.NopCloser(bytes.NewBufferString(origBody)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
	r = request.SetResources(r, &request.Resources{RequestBody: []byte(origBody)})

	plan := mustTSMMergePlan(t, r, presenceQuery)
	rewritten := plan.Variants[0].Request
	body, err := io.ReadAll(rewritten.Body)
	require.NoError(t, err)
	bodyValues, err := url.ParseQuery(string(body))
	require.NoError(t, err)
	require.Equal(t, presenceInner, bodyValues.Get(promQueryParam))
	require.Equal(t, step, bodyValues.Get("step"))
	cachedValues, err := url.ParseQuery(string(request.GetResources(rewritten).RequestBody))
	require.NoError(t, err)
	require.Equal(t, presenceInner, cachedValues.Get(promQueryParam))
}

func presenceRow(at int64, value float64) dataset.Point {
	return dataset.Point{Epoch: epoch.Epoch(at), Values: []any{strconv.FormatFloat(value, 'f', -1, 64)}}
}

func presenceInnerSeries(statement string, tags dataset.Tags, pts ...dataset.Point) *dataset.Series {
	return dataset.NewSeries(dataset.SeriesHeader{
		Tags: tags, QueryStatement: statement,
		ValueFieldsList: timeseries.FieldDefinitions{{Name: "value", DataType: timeseries.String}},
	}, pts)
}

func presencePodTags(pod string) dataset.Tags {
	return dataset.Tags{presencePodLabel: pod}
}

func TestFinalizePresenceAggregation(t *testing.T) {
	t.Run("identities held twice count once", func(t *testing.T) {
		ds := &dataset.DataSet{
			TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: presenceInner, Step: time.Minute},
			Results: dataset.Results{{SeriesList: dataset.SeriesList{
				presenceInnerSeries(presenceInner, presencePodTags(presencePodA), presenceRow(1, 2), presenceRow(2, 1)),
				presenceInnerSeries(presenceInner, presencePodTags(presencePodA), presenceRow(2, 5), presenceRow(3, 1)),
				presenceInnerSeries(presenceInner, presencePodTags(presencePodB), presenceRow(2, 1)),
				presenceInnerSeries(presenceInner, presencePodTags(presencePodC)),
				nil,
			}}},
		}
		(&Client{}).FinalizeTSMMerge(presenceQuery, ds)
		require.Len(t, ds.Results[0].SeriesList, 1)
		out := ds.Results[0].SeriesList[0]
		require.Empty(t, out.Header.Tags)
		require.Equal(t, presenceQuery, out.Header.QueryStatement)
		require.Equal(t, dataset.Points{presenceRow(1, 1), presenceRow(2, 2), presenceRow(3, 1)}, dspoints.Of(out))
	})
	t.Run("group yields one", func(t *testing.T) {
		ds := &dataset.DataSet{
			TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: presenceInner},
			Results: dataset.Results{{SeriesList: dataset.SeriesList{
				presenceInnerSeries(presenceInner, presencePodTags(presencePodA), presenceRow(1, 3)),
				presenceInnerSeries(presenceInner, presencePodTags(presencePodB), presenceRow(1, 4)),
			}}},
		}
		(&Client{}).FinalizeTSMMerge("group("+presenceInner+")", ds)
		require.Equal(t, dataset.Points{presenceRow(1, 1)}, dspoints.Of(ds.Results[0].SeriesList[0]))
	})
	t.Run("other inputs are left alone", func(t *testing.T) {
		final := presenceInnerSeries(presenceQuery, dataset.Tags{}, presenceRow(1, 3))
		ds := &dataset.DataSet{
			TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: presenceQuery},
			Results:        dataset.Results{{SeriesList: dataset.SeriesList{final}}},
		}
		(&Client{}).FinalizeTSMMerge(presenceQuery, ds)
		require.Equal(t, dataset.SeriesList{final}, ds.Results[0].SeriesList)
	})
	t.Run("empty input stays empty", func(t *testing.T) {
		ds := &dataset.DataSet{
			TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: presenceInner},
			Results:        dataset.Results{{}, nil},
		}
		(&Client{}).FinalizeTSMMerge(presenceQuery, ds)
		require.Empty(t, ds.Results[0].SeriesList)
	})
}

func BenchmarkFinalizePresence(b *testing.B) {
	const (
		identities = 5000
		steps      = 120
	)
	var list dataset.SeriesList
	for member := range 2 {
		// the members share half of their pods, as overlapping regions do
		for i := member * identities / 2; i < member*identities/2+identities; i++ {
			pts := make(dataset.Points, steps)
			for at := range pts {
				pts[at] = presenceRow(int64(at)*int64(time.Minute), 1)
			}
			list = append(list, presenceInnerSeries(presenceGroupedInner, dataset.Tags{
				presenceNSLabel: "n" + strconv.Itoa(i%20), presencePodLabel: "p" + strconv.Itoa(i),
			}, pts...))
		}
	}
	base := &dataset.DataSet{
		TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: presenceGroupedInner, Step: time.Minute},
		Results:        dataset.Results{{SeriesList: list}},
	}
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		ds := base.Clone().(*dataset.DataSet)
		b.StartTimer()
		(&Client{}).FinalizeTSMMerge(presenceGroupedQuery, ds)
	}
}
