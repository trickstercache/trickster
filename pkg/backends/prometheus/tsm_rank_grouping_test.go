/*
 * Copyright 2026 The Trickster Authors
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
	"slices"
	"strconv"
	"testing"
	"time"

	tsmql "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/prometheus/prometheus/model/labels"
	promengine "github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/stretchr/testify/require"
)

const (
	rankGroupingMetricA  = "a"
	rankGroupingMetricB  = "b"
	rankGroupingSelector = `{__name__=~"a|b"}`
	rankGroupingJobLabel = "job"
	rankGroupingJob1     = "j1"
	rankGroupingJob2     = "j2"
	rankGroupingMembers  = 2
)

type rankGroupingSeries struct {
	labels   labels.Labels
	members  []int
	from, to int
}

func rankGroupingFixture(metric, instance, job string, from, to int, members ...int) rankGroupingSeries {
	return rankGroupingSeries{
		labels: labels.FromStrings(labels.MetricName, metric, presenceInstanceLabel, instance,
			rankGroupingJobLabel, job),
		members: members, from: from, to: to,
	}
}

var rankGroupingFixtures = []rankGroupingSeries{
	// both metrics hold every instance and job, so every grouping that ignores the metric name spans both
	rankGroupingFixture(rankGroupingMetricA, presenceInstance1, rankGroupingJob1, 0, 30, 0),
	rankGroupingFixture(rankGroupingMetricA, presenceInstance1, rankGroupingJob2, 0, 30, 0, 1),
	rankGroupingFixture(rankGroupingMetricA, presenceInstance2, rankGroupingJob1, 10, 20, 1),
	rankGroupingFixture(rankGroupingMetricA, presenceInstance2, rankGroupingJob2, 0, 30, 0),
	rankGroupingFixture(rankGroupingMetricB, presenceInstance1, rankGroupingJob1, 0, 30, 1),
	rankGroupingFixture(rankGroupingMetricB, presenceInstance1, rankGroupingJob2, 0, 30, 0, 1),
	rankGroupingFixture(rankGroupingMetricB, presenceInstance2, rankGroupingJob1, 5, 25, 0),
	rankGroupingFixture(rankGroupingMetricB, presenceInstance2, rankGroupingJob2, 0, 30, 1),
}

var rankGroupingGroupings = []string{
	"without (job)", "without ()", "without (__name__)", "without (instance, job)", "by (instance)",
}

var rankGroupingEvals = []presenceEval{
	{at: 4},
	{at: 15},
	{at: 27},
	{start: 0, end: 30, step: 1},
	{start: 2, end: 28, step: 3},
}

func rankGroupingValue(series, minute int) float64 {
	// differs between every fixture series at a minute, and changes which series wins over time
	return float64((series*7+minute*5)%16*10 + series)
}

func rankGroupingStorages(t *testing.T) ([]storage.Queryable, storage.Queryable) {
	t.Helper()
	// each member holds its series over minutes [from, to), and the union holds every series as one TSDB
	members := make([]presenceStore, rankGroupingMembers)
	var union presenceStore
	for s, fixture := range rankGroupingFixtures {
		samples := make([]chunks.Sample, 0, fixture.to-fixture.from)
		for minute := fixture.from; minute < fixture.to; minute++ {
			samples = append(samples, presenceSample{t: presenceMillis(minute), f: rankGroupingValue(s, minute)})
		}
		for _, member := range fixture.members {
			members[member] = append(members[member], storage.NewListSeries(fixture.labels, samples))
		}
		union = append(union, storage.NewListSeries(fixture.labels, samples))
	}
	byLabels := func(a, b storage.Series) int { return labels.Compare(a.Labels(), b.Labels()) }
	queryables := make([]storage.Queryable, len(members))
	for i := range members {
		slices.SortFunc(members[i], byLabels)
		queryables[i] = members[i]
	}
	slices.SortFunc(union, byLabels)
	// limitk visits series in label order in the engine and in label-JSON order in TSM; here they agree
	keys := make([]string, len(union))
	for i, series := range union {
		keys[i] = dataset.Tags(series.Labels().Map()).JSON()
	}
	require.True(t, slices.IsSorted(keys), "fixture visitation orders differ")
	return queryables, union
}

func newRankGroupingEngine() *promengine.Engine {
	// the parser accepts limitk, which Prometheus gates behind its experimental functions
	return promengine.NewEngine(promengine.EngineOpts{
		MaxSamples: 10_000_000,
		Timeout:    time.Minute,
		Parser:     parser.NewParser(tsmql.ParserOptions()),
	})
}

func TestRankGroupingMatchesPrometheus(t *testing.T) {
	eng := newRankGroupingEngine()
	members, union := rankGroupingStorages(t)
	for _, operator := range []string{aggregation.TopK, aggregation.BottomK, aggregation.LimitK} {
		for _, grouping := range rankGroupingGroupings {
			for _, k := range []int{1, 2} {
				query := operator + " " + grouping + " (" + strconv.Itoa(k) + ", " + rankGroupingSelector + ")"
				t.Run(query, func(t *testing.T) {
					for _, ev := range rankGroupingEvals {
						var want presenceResult
						evalPresence(t, eng, union, query, ev, func(v parser.Value) {
							want = enginePresenceResult(t, v)
						})
						got := tricksterPresenceResult(t, eng, members, query, ev)
						require.Equal(t, want.series, got.series, "%s at %+v", query, ev)
					}
				})
			}
		}
	}
}

func TestRankGroupingAcrossMetricNames(t *testing.T) {
	// grouping without the job label puts both metrics' series of an instance in one group
	eng := newRankGroupingEngine()
	members, _ := rankGroupingStorages(t)
	for _, operator := range []string{aggregation.TopK, aggregation.BottomK, aggregation.LimitK} {
		query := operator + " without (job) (1, " + rankGroupingSelector + ")"
		got := tricksterPresenceResult(t, eng, members, query, presenceEval{at: 15})
		require.Len(t, got.series, 2, query)
	}
}
