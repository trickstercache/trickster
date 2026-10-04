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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/stretchr/testify/require"
)

const (
	testReplicaLabel = "replica"
	testBackupLabel  = "isBackup"
	testRate         = "rate"
)

func listSeries(lset labels.Labels, points ...testPoint) storage.Series {
	samples := make([]chunks.Sample, len(points))
	for i, p := range points {
		if p.h {
			samples[i] = sample{t: p.t, fh: testHistogram().ToFloat(nil)}
			continue
		}
		samples[i] = sample{t: p.t, f: p.f}
	}
	return storage.NewListSeries(lset, samples)
}

func podLabels(pod string, kv ...string) labels.Labels {
	return labels.FromStrings(append([]string{labels.MetricName, testMetric, testPod, pod}, kv...)...)
}

func readDedup(t *testing.T, members [][]storage.Series, replicas []string, dedup, function string) []storage.Series {
	t.Helper()
	return Deduplicate(members, replicas, dedup, function)
}

// interleaved returns two replicas scraped 5s apart every 15s, as an HA pair is
func interleaved() (storage.Series, storage.Series) {
	a := listSeries(podLabels("a", testReplicaLabel, "1"),
		testPoint{t: 0, f: 1}, testPoint{t: 15000, f: 2}, testPoint{t: 30000, f: 3},
		testPoint{t: 90000, f: 4}, testPoint{t: 105000, f: 5})
	b := listSeries(podLabels("a", testReplicaLabel, "2"),
		testPoint{t: 5000, f: 10}, testPoint{t: 20000, f: 11}, testPoint{t: 35000, f: 12}, testPoint{t: 50000, f: 13},
		testPoint{t: 65000, f: 14}, testPoint{t: 80000, f: 15}, testPoint{t: 95000, f: 16}, testPoint{t: 110000, f: 17})
	return a, b
}

func TestDeduplicateGroupsByCanonicalLabels(t *testing.T) {
	members := [][]storage.Series{
		{
			listSeries(podLabels("b", testReplicaLabel, "1"), testPoint{t: 1, f: 1}),
			listSeries(podLabels("a", testReplicaLabel, "1"), testPoint{t: 1, f: 1}),
		},
		{
			listSeries(podLabels("a", testReplicaLabel, "2"), testPoint{t: 2, f: 2}),
			listSeries(podLabels("c"), testPoint{t: 1, f: 1}),
		},
	}
	out := readDedup(t, members, []string{testReplicaLabel}, merge.TSMDeduplicationPenalty, "")
	require.Len(t, out, 3)
	require.Equal(t, podLabels("a"), out[0].Labels())
	require.Equal(t, podLabels("b"), out[1].Labels())
	require.Equal(t, podLabels("c"), out[2].Labels())
	require.Len(t, out[0].(*dedupSeries).group.replicas, 2)
}

func TestDeduplicatePenalty(t *testing.T) {
	a, b := interleaved()
	out := readDedup(t, [][]storage.Series{{a}, {b}}, []string{testReplicaLabel}, merge.TSMDeduplicationPenalty, "")
	require.Len(t, out, 1)
	// replica 1 is followed until its gap, then replica 2, skipping its samples within the penalty
	require.Equal(t, []testPoint{
		{t: 0, f: 1},
		{t: 15000, f: 2},
		{t: 30000, f: 3},
		{t: 65000, f: 14},
		{t: 80000, f: 15},
		{t: 95000, f: 16},
		{t: 110000, f: 17},
	}, readPoints(t, out[0]))
}

func TestDeduplicatePenaltyCounterAdjust(t *testing.T) {
	a := listSeries(podLabels("a", testReplicaLabel, "1"),
		testPoint{t: 0, f: 10}, testPoint{t: 15000, f: 20}, testPoint{t: 30000, f: 30})
	b := listSeries(podLabels("a", testReplicaLabel, "2"),
		testPoint{t: 5000, f: 1}, testPoint{t: 20000, f: 2}, testPoint{t: 35000, f: 3}, testPoint{t: 50000, f: 4},
		testPoint{t: 65000, f: 5}, testPoint{t: 80000, f: 6})
	members := [][]storage.Series{{a}, {b}}
	counter := readDedup(t, members, []string{testReplicaLabel}, merge.TSMDeduplicationPenalty, testRate)
	require.Equal(t, []testPoint{
		{t: 0, f: 10},
		{t: 15000, f: 20},
		{t: 30000, f: 30},
		{t: 65000, f: 30},
		{t: 80000, f: 31},
	}, readPoints(t, counter[0]))
	gauge := readDedup(t, members, []string{testReplicaLabel}, merge.TSMDeduplicationPenalty, "")
	require.Equal(t, []testPoint{
		{t: 0, f: 10},
		{t: 15000, f: 20},
		{t: 30000, f: 30},
		{t: 65000, f: 5},
		{t: 80000, f: 6},
	}, readPoints(t, gauge[0]))
}

func TestDeduplicateChain(t *testing.T) {
	a := listSeries(podLabels("a", testReplicaLabel, "1"), testPoint{t: 0, f: 1}, testPoint{t: 15000, f: 2})
	b := listSeries(podLabels("a", testReplicaLabel, "2"), testPoint{t: 5000, f: 10}, testPoint{t: 15000, f: 20})
	out := readDedup(t, [][]storage.Series{{b}, {a}}, []string{testReplicaLabel}, merge.TSMDeduplicationChain, "")
	// replica 2 belongs to the first member, so it wins the shared timestamp
	require.Equal(t, []testPoint{{t: 0, f: 1}, {t: 5000, f: 10}, {t: 15000, f: 20}}, readPoints(t, out[0]))
}

func TestDeduplicateIdenticalCopies(t *testing.T) {
	first := listSeries(podLabels("a"), testPoint{t: 0, f: 1}, testPoint{t: 20000, f: 3})
	second := listSeries(podLabels("a"), testPoint{t: 0, f: 100}, testPoint{t: 10000, f: 2}, testPoint{t: 30000, f: 4})
	out := readDedup(t, [][]storage.Series{{first}, {second}}, nil, merge.TSMDeduplicationPenalty, "")
	require.Len(t, out, 1)
	require.Equal(t, []testPoint{{t: 0, f: 1}, {t: 10000, f: 2}, {t: 20000, f: 3}, {t: 30000, f: 4}},
		readPoints(t, out[0]))
}

func TestDeduplicateReplicaOrderWithinMember(t *testing.T) {
	primary := listSeries(podLabels("a"), testPoint{t: 0, f: 1})
	backup := listSeries(podLabels("a", testBackupLabel, "true"), testPoint{t: 0, f: 2})
	out := readDedup(t, [][]storage.Series{{backup, primary}}, []string{testBackupLabel},
		merge.TSMDeduplicationChain, "")
	require.Equal(t, podLabels("a"), out[0].Labels())
	require.Equal(t, []testPoint{{t: 0, f: 1}}, readPoints(t, out[0]), "the copy without the replica label wins")
}

func TestDeduplicateThreeReplicas(t *testing.T) {
	a := listSeries(podLabels("a", testReplicaLabel, "1"), testPoint{t: 0, f: 1}, testPoint{t: 15000, f: 2})
	b := listSeries(podLabels("a", testReplicaLabel, "2"), testPoint{t: 30000, f: 3})
	c := listSeries(podLabels("a", testReplicaLabel, "3"), testPoint{t: 75000, f: 4}, testPoint{t: 90000, f: 5})
	out := readDedup(t, [][]storage.Series{{a}, {b}, {c}}, []string{testReplicaLabel},
		merge.TSMDeduplicationPenalty, testRate)
	require.Equal(t, []testPoint{{t: 0, f: 1}, {t: 15000, f: 2}, {t: 75000, f: 4}, {t: 90000, f: 5}},
		readPoints(t, out[0]))
}

func TestDeduplicateHistograms(t *testing.T) {
	a := listSeries(podLabels("a", testReplicaLabel, "1"), testPoint{t: 0, h: true}, testPoint{t: 15000, h: true})
	b := listSeries(podLabels("a", testReplicaLabel, "2"), testPoint{t: 5000, h: true}, testPoint{t: 50000, h: true})
	out := readDedup(t, [][]storage.Series{{a}, {b}}, []string{testReplicaLabel}, merge.TSMDeduplicationPenalty, testRate)
	require.Equal(t, []testPoint{{t: 0, h: true}, {t: 15000, h: true}, {t: 50000, h: true}}, readPoints(t, out[0]))
	chain := readDedup(t, [][]storage.Series{{a}, {b}}, []string{testReplicaLabel}, merge.TSMDeduplicationChain, "")
	for _, series := range []storage.Series{out[0], chain[0]} {
		it := series.Iterator(nil)
		require.Equal(t, chunkenc.ValFloatHistogram, it.Next())
		_, fh := it.AtFloatHistogram(nil)
		require.InDelta(t, 4.0, fh.Count, 0)
		_, h := it.AtHistogram(nil)
		require.Nil(t, h)
		require.Zero(t, it.AtST())
	}
}

func TestDedupIteratorsSeek(t *testing.T) {
	a, b := interleaved()
	for _, dedup := range []string{merge.TSMDeduplicationPenalty, merge.TSMDeduplicationChain} {
		t.Run(dedup, func(t *testing.T) {
			series := readDedup(t, [][]storage.Series{{a}, {b}}, []string{testReplicaLabel}, dedup, "")[0]
			all := readPoints(t, series)
			for i, want := range all {
				it := series.Iterator(nil)
				require.NotEqual(t, chunkenc.ValNone, it.Seek(want.t-1), "%s seek to %d", dedup, want.t)
				at, v := it.At()
				require.Equal(t, want, testPoint{t: at, f: v})
				// seeking backwards keeps the current sample
				require.NotEqual(t, chunkenc.ValNone, it.Seek(0))
				require.Equal(t, want.t, it.AtT())
				if i == len(all)-1 {
					require.Equal(t, chunkenc.ValNone, it.Next())
					require.Equal(t, chunkenc.ValNone, it.Next())
					require.Equal(t, chunkenc.ValNone, it.Seek(want.t+1))
				}
			}
			it := series.Iterator(nil)
			require.Equal(t, chunkenc.ValNone, it.Seek(all[len(all)-1].t+1))
			require.NoError(t, it.Err())
		})
	}
}

type errIterator struct {
	chunkenc.Iterator
	err error
}

func (e errIterator) Err() error { return e.err }

type errSeries struct {
	storage.Series
	err error
}

func (e errSeries) Iterator(chunkenc.Iterator) chunkenc.Iterator {
	return errIterator{Iterator: e.Series.Iterator(nil), err: e.err}
}

func TestDedupIteratorsReportErrors(t *testing.T) {
	boom := errors.New("boom")
	a, b := interleaved()
	for _, dedup := range []string{merge.TSMDeduplicationPenalty, merge.TSMDeduplicationChain} {
		for _, members := range [][][]storage.Series{
			{{errSeries{Series: a, err: boom}}, {b}},
			{{a}, {errSeries{Series: b, err: boom}}},
		} {
			series := readDedup(t, members, []string{testReplicaLabel}, dedup, "")[0]
			require.ErrorIs(t, series.Iterator(nil).Err(), boom, dedup)
		}
	}
}

func TestCounterAdjustSkipsHistograms(t *testing.T) {
	it := &counterAdjustIterator{Iterator: listSeries(podLabels("a"), testPoint{t: 0, h: true}).Iterator(nil)}
	require.Equal(t, chunkenc.ValFloatHistogram, it.Next())
	it.adjustAtValue(100)
	require.Zero(t, it.adjust)
	nested := newPenaltyIterator(noAdjustIterator{Iterator: storage.NewListSeries(labels.EmptyLabels(), nil).Iterator(nil)},
		&counterAdjustIterator{Iterator: listSeries(podLabels("a"), testPoint{t: 0, f: 1}).Iterator(nil)})
	require.Equal(t, chunkenc.ValFloat, nested.Next())
	nested.adjustAtValue(5)
	_, v := nested.At()
	require.InDelta(t, 5.0, v, 0)
	var fh *histogram.FloatHistogram
	_, fh = nested.AtFloatHistogram(fh)
	require.Nil(t, fh)
}

func TestDeduplicateChainKeepsInterleavedSamples(t *testing.T) {
	a, b := interleaved()
	out := readDedup(t, [][]storage.Series{{a}, {b}}, []string{testReplicaLabel}, merge.TSMDeduplicationChain, "")
	require.Len(t, readPoints(t, out[0]), len(readPoints(t, a))+len(readPoints(t, b)))
}

func BenchmarkDeduplicate(b *testing.B) {
	const (
		seriesCount = 2000
		samples     = 240
	)
	members := make([][]storage.Series, 2)
	for m := range members {
		for s := range seriesCount {
			points := make([]testPoint, samples)
			for i := range points {
				points[i] = testPoint{t: int64(i*15000 + m*5000), f: float64(i)}
			}
			members[m] = append(members[m], listSeries(podLabels(string(rune('a'+s%26))+string(rune('a'+s/26%26))+
				string(rune('a'+s/676)), testReplicaLabel, string(rune('1'+m))), points...))
		}
	}
	for _, dedup := range []string{merge.TSMDeduplicationPenalty, merge.TSMDeduplicationChain} {
		b.Run(dedup, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, series := range Deduplicate(members, []string{testReplicaLabel}, dedup, testRate) {
					it := series.Iterator(nil)
					for it.Next() != chunkenc.ValNone {
					}
				}
			}
		})
	}
}
