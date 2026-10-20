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
	"context"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
)

const (
	resetMetric    = "requests"
	resetStepMs    = 15_000
	resetSamples   = 21
	resetEndMs     = 300_000
	resetSeconds   = 300
	resetEvalStep  = 15 * time.Second
	transportInt   = "int chunks"
	transportFloat = "float chunks"
	transportList  = "samples"
)

// resetPoint is a histogram counter sample, its buckets each holding n observations
type resetPoint struct {
	t int64
	n int64
}

func resetHistogram(n int64) *histogram.Histogram {
	return &histogram.Histogram{
		Count: uint64(2 * n), Sum: float64(3 * n), //nolint:gosec // small positive test counts
		PositiveSpans: []histogram.Span{{Offset: 0, Length: 2}}, PositiveBuckets: []int64{n, 0},
	}
}

// resetSeries returns points as a series carried by transport, with the hints it would carry: chunks
// set them from their own previous sample, and samples as a member sends them
func resetSeries(t *testing.T, lset labels.Labels, transport string, points []resetPoint) storage.Series {
	t.Helper()
	switch transport {
	case transportList:
		samples := make([]sample, len(points))
		for i, p := range points {
			fh := resetHistogram(p.n).ToFloat(nil)
			if i > 0 {
				fh.CounterResetHint = histogram.NotCounterReset
			}
			samples[i] = sample{t: p.t, fh: fh}
		}
		return &sampleSeries{lset: lset, samples: samples}
	case transportFloat:
		c := chunkenc.NewFloatHistogramChunk()
		app, err := c.Appender()
		require.NoError(t, err)
		for _, p := range points {
			chk, _, next, err := app.AppendFloatHistogram(nil, 0, p.t, resetHistogram(p.n).ToFloat(nil), true)
			require.NoError(t, err)
			require.Nil(t, chk, "a source's own samples rise, so they fit one chunk")
			app = next
		}
		return &chunkSeries{lset: lset, chunks: []chunkenc.Iterable{c}}
	default:
		c := chunkenc.NewHistogramChunk()
		app, err := c.Appender()
		require.NoError(t, err)
		for _, p := range points {
			chk, _, next, err := app.AppendHistogram(nil, 0, p.t, resetHistogram(p.n), true)
			require.NoError(t, err)
			require.Nil(t, chk, "a source's own samples rise, so they fit one chunk")
			app = next
		}
		return &chunkSeries{lset: lset, chunks: []chunkenc.Iterable{c}}
	}
}

// resetPoints returns count samples every 15s from start, the counter rising by step from base
func resetPoints(start int64, count int, base, step int64) []resetPoint {
	out := make([]resetPoint, count)
	for i := range out {
		out[i] = resetPoint{t: start + int64(i)*resetStepMs, n: base + int64(i)*step}
	}
	return out
}

// canonicalSeries reads what deduplication returns into series whose hints say nothing, so the
// engine finds every reset from the counts alone, as it must across a change of source
func canonicalSeries(t *testing.T, deduped []storage.Series) []storage.Series {
	t.Helper()
	out := make([]storage.Series, len(deduped))
	for i, s := range deduped {
		var samples []sample
		it := s.Iterator(nil)
		for vt := it.Next(); vt != chunkenc.ValNone; vt = it.Next() {
			require.Contains(t, []chunkenc.ValueType{chunkenc.ValHistogram, chunkenc.ValFloatHistogram}, vt)
			at, fh := it.AtFloatHistogram(nil)
			fh = fh.Copy()
			fh.CounterResetHint = histogram.UnknownCounterReset
			samples = append(samples, sample{t: at, fh: fh})
		}
		require.NoError(t, it.Err())
		out[i] = &sampleSeries{lset: s.Labels(), samples: samples}
	}
	return out
}

func evalRange(t *testing.T, q storage.Queryable, query string) promql.Matrix {
	t.Helper()
	engine := promql.NewEngine(promql.EngineOpts{MaxSamples: 1_000_000, Timeout: time.Minute})
	qry, err := engine.NewRangeQuery(context.Background(), q, nil, query, time.Unix(0, 0),
		time.Unix(resetSeconds, 0), resetEvalStep)
	require.NoError(t, err)
	defer qry.Close()
	res := qry.Exec(context.Background())
	require.NoError(t, res.Err)
	m, err := res.Matrix()
	require.NoError(t, err)
	return copyMatrix(m)
}

func TestDeduplicationInvalidatesResetHintsAcrossSources(t *testing.T) {
	lset := func(kv ...string) labels.Labels {
		return labels.FromStrings(append([]string{labels.MetricName, resetMetric, testPod, "a"}, kv...)...)
	}
	// the first source stops at 150s and the lower second carries on, so moving to it reads as a reset;
	// interleaved, every switch to the second reads as one
	first := resetPoints(0, 11, 100, 10)
	second := resetPoints(5_000, resetSamples, 10, 10)
	topologies := []struct {
		name     string
		replicas []string
		labels   [2]labels.Labels
	}{
		{
			name: "replicas", replicas: []string{testReplicaLabel},
			labels: [2]labels.Labels{lset(testReplicaLabel, "1"), lset(testReplicaLabel, "2")},
		},
		{name: "copies", labels: [2]labels.Labels{lset(), lset()}},
	}
	queries := []string{
		"rate(" + resetMetric + "[1m])", "increase(" + resetMetric + "[1m])",
		"irate(" + resetMetric + "[1m])", "resets(" + resetMetric + "[1m])",
	}
	for _, transport := range []string{transportInt, transportFloat, transportList} {
		for _, topology := range topologies {
			for _, dedup := range []string{merge.TSMDeduplicationPenalty, merge.TSMDeduplicationChain} {
				t.Run(transport+"/"+topology.name+"/"+dedup, func(t *testing.T) {
					members := [][]storage.Series{
						{resetSeries(t, topology.labels[0], transport, first)},
						{resetSeries(t, topology.labels[1], transport, second)},
					}
					sources := make([]Member, len(members))
					for i, list := range members {
						sources[i] = Member{Name: string(rune('a' + i)), Source: &MemorySource{Series: list}}
					}
					global := NewQueryable(sources, topology.replicas, dedup, false, nil)
					canonical := NewQueryable([]Member{{Name: "canonical", Source: &MemorySource{
						Series: canonicalSeries(t, Deduplicate(members, topology.replicas, dedup, "")),
					}}}, nil, dedup, false, nil)
					for _, query := range queries {
						require.Equal(t, evalRange(t, canonical, query), evalRange(t, global, query), query)
					}
					if transport == transportList {
						// a sample a member sent is never changed in place, as other iterations read it
						it := members[1][0].Iterator(nil)
						require.Equal(t, chunkenc.ValFloatHistogram, it.Next())
						require.Equal(t, chunkenc.ValFloatHistogram, it.Next())
						_, fh := it.AtFloatHistogram(nil)
						require.Equal(t, histogram.NotCounterReset, fh.CounterResetHint)
					}
				})
			}
		}
	}
}

func TestDeduplicationKeepsResetHintsWithinOneSource(t *testing.T) {
	// a gauge stays a gauge, and a source's own consecutive samples keep their hints
	g := resetHistogram(5).ToFloat(nil)
	g.CounterResetHint = histogram.GaugeType
	require.Same(t, g, withUnknownFloatReset(g, nil))
	unknown := resetHistogram(5).ToFloat(nil)
	require.Same(t, unknown, withUnknownFloatReset(unknown, nil))
	hinted := resetHistogram(5).ToFloat(nil)
	hinted.CounterResetHint = histogram.CounterReset
	buf := &histogram.FloatHistogram{}
	require.Same(t, buf, withUnknownFloatReset(hinted, buf))
	require.Equal(t, histogram.UnknownCounterReset, buf.CounterResetHint)
	require.Equal(t, histogram.CounterReset, hinted.CounterResetHint, "the source's histogram is untouched")
	copied := withUnknownFloatReset(hinted, nil)
	require.NotSame(t, hinted, copied)
	require.Equal(t, histogram.UnknownCounterReset, copied.CounterResetHint)

	ih := resetHistogram(5)
	ih.CounterResetHint = histogram.NotCounterReset
	require.Equal(t, histogram.UnknownCounterReset, withUnknownReset(ih, nil).CounterResetHint)
	require.Equal(t, histogram.NotCounterReset, ih.CounterResetHint)
	ibuf := &histogram.Histogram{}
	require.Same(t, ibuf, withUnknownReset(ih, ibuf))
	ig := resetHistogram(5)
	ig.CounterResetHint = histogram.GaugeType
	require.Same(t, ig, withUnknownReset(ig, nil))

	lset := labels.FromStrings(labels.MetricName, resetMetric)
	one := resetSeries(t, lset, transportList, resetPoints(0, 3, 1, 1))
	for _, dedup := range []string{merge.TSMDeduplicationPenalty, merge.TSMDeduplicationChain} {
		deduped := Deduplicate([][]storage.Series{{one}, {one}}, nil, dedup, "")
		it := deduped[0].Iterator(nil)
		var hints []histogram.CounterResetHint
		for it.Next() != chunkenc.ValNone {
			_, fh := it.AtFloatHistogram(nil)
			hints = append(hints, fh.CounterResetHint)
		}
		require.Equal(t, []histogram.CounterResetHint{
			histogram.UnknownCounterReset, histogram.NotCounterReset, histogram.NotCounterReset,
		}, hints, dedup)
	}
}
