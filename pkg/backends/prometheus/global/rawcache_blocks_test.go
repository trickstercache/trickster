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
	"bytes"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
)

const (
	compactionSeries  = 20
	histogramMetric   = "hist"
	changingMetric    = "changing"
	compactionResetAt = 70 * rawTestMinute
)

// damagedChunkSource returns a member whose one chunk, in the first block, has a whole header but a body cut
// short until repair is called, and a count of its reads
func damagedChunkSource(t *testing.T, transport string, good prompb.Chunk) (Source, func(), func() int) {
	t.Helper()
	damaged := good
	damaged.Data = good.Data[:2+(len(good.Data)-2)/2]
	var repaired atomic.Bool
	switch transport {
	case "storeapi":
		frame := func(c prompb.Chunk) [][]byte {
			return [][]byte{encodeTestSeries([]string{labels.MetricName, testMetric, testPod, "a"},
				spanned(encodeTestChunk(storeChunkXOR, c.Data, true), c.MinTimeMs, c.MaxTimeMs))}
		}
		store := &testStore{frames: frame(damaged)}
		source := store.serve(t)
		source.ID = rawTestIdentity
		return source, func() {
				store.mu.Lock()
				defer store.mu.Unlock()
				store.frames = frame(good)
			}, func() int {
				store.mu.Lock()
				defer store.mu.Unlock()
				return len(store.requests)
			}
	default:
		var reads atomic.Int64
		rr := newTestRemoteRead(t, func(w http.ResponseWriter, r *http.Request) {
			_ = decodeTestRequest(t, r)
			reads.Add(1)
			chunk := damaged
			if repaired.Load() {
				chunk = good
			}
			var body bytes.Buffer
			writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{chunk}})
			w.Header().Set(headers.NameContentType, contentTypeStreamed+"; proto=prometheus.ChunkedReadResponse")
			_, _ = w.Write(body.Bytes())
		})
		rr.ID = rawTestIdentity
		return rr, func() { repaired.Store(true) }, func() int { return int(reads.Load()) }
	}
}

func TestRawCacheKeepsNoUnreadableChunk(t *testing.T) {
	points := make([]testPoint, rawTestChunk)
	for i := range points {
		points[i] = testPoint{t: rawTestHour + int64(i)*rawTestStep, f: float64(i)}
	}
	good := xorChunk(t, points...)
	const mint, maxt = 0, 2*rawTestHour - 1
	for _, transport := range []string{"remote read", "storeapi"} {
		for name, newCache := range rawTestCaches(t) {
			t.Run(transport+"/"+name, func(t *testing.T) {
				source, repair, reads := damagedChunkSource(t, transport, good)
				rc := newRawTestCache(newCache(), RawCacheOptions{})
				// the read fails rather than keeping a chunk that can't be read, here and through the queryable
				_, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), source, mint, maxt)
				require.Error(t, err)
				q := NewQueryable([]Member{{Name: rawTestMember, Source: source}}, nil, merge.TSMDeduplicationChain, true,
					nil).WithRawCache(rc.RawCache)
				qr, err := q.Querier(mint, maxt)
				require.NoError(t, err)
				set := qr.Select(teamRequest(rawTestTeamA), true, &storage.SelectHints{Start: mint, End: maxt}, testMetricMatcher)
				require.False(t, set.Next())
				require.NoError(t, set.Err())
				require.NotEmpty(t, set.Warnings(), "the member's failure is a partial response's warning")
				rc.Wait()
				// once the member is repaired, the next read reaches it, as nothing was kept in the meantime
				repair()
				series, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), source, mint, maxt)
				require.NoError(t, err)
				require.Len(t, series, 1)
				require.Equal(t, points, readPoints(t, series[0]))
				require.Equal(t, 3, reads())
			})
		}
	}
}

// histChunked encodes a native histogram counter as a member does, cutting chunks at 120 samples and resets
func histChunked(t testing.TB, lset labels.Labels, ts, counts []int64) *chunkSeries {
	t.Helper()
	cs := &chunkSeries{lset: lset}
	var c chunkenc.Chunk
	var app chunkenc.Appender
	var first, last int64
	flush := func() {
		if c != nil && c.NumSamples() > 0 {
			cs.chunks, cs.spans = append(cs.chunks, c), append(cs.spans, span{mint: first, maxt: last})
		}
	}
	for i, at := range ts {
		if c == nil || c.NumSamples() >= rawTestChunk {
			flush()
			c = chunkenc.NewHistogramChunk()
			var err error
			app, err = c.Appender()
			require.NoError(t, err)
			first = at
		}
		cut, recoded, next, err := app.AppendHistogram(nil, 0, at, resetHistogram(counts[i]), false)
		require.NoError(t, err)
		if cut != nil {
			if !recoded {
				flush()
				first = at
			}
			c = cut
		}
		app, last = next, at
	}
	flush()
	return cs
}

// compactionSource holds counters, a native histogram counter that resets once, and a series of floats that
// becomes one of histograms, all scraped every 15s for three hours
func compactionSource(t testing.TB) *chunkedSource {
	t.Helper()
	src := &chunkedSource{ID: rawTestIdentity}
	end := 3 * rawTestHour
	for i := range compactionSeries {
		var points []testPoint
		for ts := int64(i%15) * 1000; ts <= end; ts += rawTestStep {
			points = append(points, testPoint{t: ts, f: float64(ts / rawTestStep)})
		}
		src.series = append(src.series, chunked(t, podLabels(strconv.Itoa(100+i)), points))
	}
	var ts, counts, histTS, histCounts []int64
	var floats []testPoint
	for at := int64(0); at <= end; at += rawTestStep {
		n := at / rawTestStep
		if at >= compactionResetAt {
			n -= compactionResetAt/rawTestStep - 1
		}
		ts, counts = append(ts, at), append(counts, n+1)
		if at < rawTestHour {
			floats = append(floats, testPoint{t: at, f: float64(n)})
		} else {
			histTS, histCounts = append(histTS, at), append(histCounts, n+1)
		}
	}
	src.series = append(src.series, histChunked(t, labels.FromStrings(labels.MetricName, histogramMetric), ts, counts))
	changing := chunked(t, labels.FromStrings(labels.MetricName, changingMetric), floats)
	tail := histChunked(t, changing.lset, histTS, histCounts)
	changing.chunks, changing.spans = append(changing.chunks, tail.chunks...), append(changing.spans, tail.spans...)
	src.series = append(src.series, changing)
	return src
}

func TestRawCacheCompactsRefreshedBlocks(t *testing.T) {
	volatile := rawTestVolatile.Milliseconds()
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			src := compactionSource(t)
			rc := newRawTestCache(newCache(), RawCacheOptions{})
			all := []*labels.Matcher{labels.MustNewMatcher(labels.MatchRegexp, labels.MetricName, ".+")}
			read := func(mint, maxt int64) {
				t.Helper()
				_, _, err := rc.Select(teamRequest(rawTestTeamA), Member{Name: rawTestMember, Source: src}, mint, maxt,
					nil, all)
				require.NoError(t, err)
				rc.Wait()
			}
			key, ok := rc.key(teamRequest(rawTestTeamA), src, all)
			require.True(t, ok)
			// a dashboard refreshing every 15s until the first block is older than the volatile window
			for now := volatile + rawTestStep; now <= rawBlockMs+volatile+rawTestStep; now += rawTestStep {
				rc.setNow(now)
				read(0, now)
				reads := src.takeReads()
				require.Len(t, reads, 1)
				if now > volatile+rawTestStep {
					require.Equal(t, now-volatile-rawTestStep+1, reads[0][0], "each refresh reads only its newest samples")
				}
				// meanwhile, a refreshed series holds full chunks, then a few small ones
				if entry, ok := rc.engine.Retrieve(blockKey(key, 0)); ok && now%rawTestHour == 0 {
					for _, s := range entry.Payload.series {
						samples := 0
						for _, c := range s.chunks {
							samples += c.chk.NumSamples()
						}
						require.LessOrEqual(t, len(s.chunks), (samples+rawChunkSamples-1)/rawChunkSamples+rawTailChunks+2,
							s.lset.String())
					}
				}
			}
			entry, ok := rc.engine.Retrieve(blockKey(key, 0))
			require.True(t, ok)
			require.Equal(t, timeseries.ExtentList{extent(0, rawBlockMs-1)}, entry.Extents)
			members := make([]storage.Series, len(src.series))
			for i, s := range src.series {
				members[i] = s
			}
			cold, err := normalize(members, 0, rawBlockMs-1)
			require.NoError(t, err)
			require.Len(t, entry.Payload.series, len(cold))
			for i, s := range entry.Payload.series {
				// refreshes leave chunks of full size, as one read of the whole block does
				require.LessOrEqual(t, len(s.chunks), len(cold[i].chunks)+1, s.lset.String())
			}
			refreshed, err := rawBlockCodec{}.Marshal(entry.Payload)
			require.NoError(t, err)
			whole, err := rawBlockCodec{}.Marshal(&rawBlock{series: cold})
			require.NoError(t, err)
			require.Less(t, len(refreshed), len(whole)*5/4)

			// and their samples answer as the member's own do, for either deduplication, from the cache alone
			rc.takeStatuses()
			const start = rawTestHour + 10*rawTestMinute
			for _, dedup := range []string{merge.TSMDeduplicationPenalty, merge.TSMDeduplicationChain} {
				members := []Member{{Name: rawTestMember, Source: src}}
				cached := NewQueryable(members, nil, dedup, false, nil).WithRawCache(rc.RawCache)
				direct := NewQueryable(members, nil, dedup, false, nil)
				for _, query := range []string{
					"rate(up[5m])", "up", "rate(" + histogramMetric + "[5m])", "histogram_count(increase(" +
						histogramMetric + "[10m]))", changingMetric, "resets(" + histogramMetric + "[1h])",
				} {
					want := evalRangeQuery(t, direct, query, start, rawBlockMs-1)
					require.NotEmpty(t, want, query)
					require.Equal(t, want, evalRangeQuery(t, cached, query, start, rawBlockMs-1), "%s %s", dedup, query)
				}
			}
			require.NotContains(t, rc.takeStatuses(), status.LookupStatusProxyOnly)
		})
	}
}

// BenchmarkRawBlockRefresh measures keeping a block of 100 series one 15s refresh at a time, as a dashboard
// does, against building it from one read of the whole block
func BenchmarkRawBlockRefresh(b *testing.B) {
	src := &chunkedSource{ID: rawTestIdentity}
	for i := range 100 {
		var points []testPoint
		for ts := int64(0); ts < rawBlockMs; ts += rawTestStep {
			points = append(points, testPoint{t: ts, f: float64(ts)})
		}
		src.series = append(src.series, chunked(b, podLabels(strconv.Itoa(i)), points))
	}
	members := make([]storage.Series, len(src.series))
	for i, s := range src.series {
		members[i] = s
	}
	refreshes := make([][]rawSeries, 0, rawBlockMs/rawTestStep)
	for ts := int64(0); ts < rawBlockMs; ts += rawTestStep {
		sliver, err := normalize(members, ts, ts+rawTestStep-1)
		if err != nil {
			b.Fatal(err)
		}
		refreshes = append(refreshes, sliver)
	}
	b.Run("refreshed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var block *rawBlock
			for i, sliver := range refreshes {
				end := int64(i+1) * rawTestStep
				block = mergeBlock(block, sliver, timeseries.ExtentList{extent(0, end-1)}, end == rawBlockMs)
			}
		}
	})
	b.Run("whole", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			whole, err := normalize(members, 0, rawBlockMs-1)
			if err != nil {
				b.Fatal(err)
			}
			_ = mergeBlock(nil, whole, timeseries.ExtentList{extent(0, rawBlockMs-1)}, true)
		}
	})
}
