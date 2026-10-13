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
	"context"
	"fmt"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/value"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
)

const (
	// storeTestWait bounds waiting on stores that, unrecovered, would never finish
	storeTestWait = 5 * time.Second
	// storeTestCachedTo is where a test block's cached samples end and its read begins
	storeTestCachedTo = 30 * rawTestMinute
	storeTestBug      = "store bug"
	storeTestKey      = rawKeyPrefix + "store-test"
)

// fineHistogram is a histogram counter sample of a schema finer than the engine reads, so it is read reduced, which
// merges its two buckets; a malformed one's spans can't be reduced
func fineHistogram(malformed bool) *histogram.Histogram {
	h := resetHistogram(1)
	h.Schema = histogram.ExponentialSchemaMax + 1
	h.PositiveSpans = []histogram.Span{{Offset: 1, Length: 2}}
	if malformed {
		h.PositiveSpans = []histogram.Span{{Offset: 0, Length: 1}, {Offset: -3, Length: 1}}
	}
	return h
}

// histogramChunkAt encodes a histogram counter sample at each of minutes as one chunk of enc. A malformed
// chunk's samples read as timestamps, but materializing any of them panics, as their schema's spans can't reduce.
func histogramChunkAt(t *testing.T, enc chunkenc.Encoding, malformed bool, minutes ...int64) rawChunk {
	t.Helper()
	h := resetHistogram(1)
	if malformed {
		h = fineHistogram(true)
	}
	chk, err := chunkenc.NewEmptyChunk(enc)
	require.NoError(t, err)
	app, err := chk.Appender()
	require.NoError(t, err)
	for _, m := range minutes {
		var cut chunkenc.Chunk
		if enc == chunkenc.EncFloatHistogram {
			cut, _, app, err = app.AppendFloatHistogram(nil, 0, m*rawTestMinute, h.ToFloat(nil), false)
		} else {
			cut, _, app, err = app.AppendHistogram(nil, 0, m*rawTestMinute, h, false)
		}
		require.NoError(t, err)
		require.Nil(t, cut, "every sample fits the one chunk")
	}
	return rawChunk{mint: minutes[0] * rawTestMinute, maxt: minutes[len(minutes)-1] * rawTestMinute, chk: chk}
}

// histogramSeriesOf is a member's series of chunks
func histogramSeriesOf(lset labels.Labels, chunks ...rawChunk) *chunkSeries {
	cs := &chunkSeries{lset: lset}
	for _, c := range chunks {
		cs.chunks, cs.spans = append(cs.chunks, c.chk), append(cs.spans, c.span)
	}
	return cs
}

// lockedBuffer collects log lines written from any goroutine
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureErrors logs errors to the returned buffer until the test ends
func captureErrors(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	l := logging.StreamLogger(buf, level.Error)
	l.SetLogAsynchronous(false)
	prev := logger.Logger()
	logger.SetLogger(l)
	t.Cleanup(func() { logger.SetLogger(prev) })
	return buf
}

// waitWithin calls wait, failing rather than hanging when it never returns
func waitWithin(t *testing.T, wait func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(storeTestWait):
		t.Fatal("what was waited on never finished")
	}
}

// indexed reports whether rc's index names k's blocks as cached
func indexed(rc *RawCache, k rawKey) bool {
	rc.index.mu.Lock()
	defer rc.index.mu.Unlock()
	_, ok := rc.index.groups[k.group()][k.key]
	return ok
}

// storeTestChunk is a histogram chunk of a sample at each of its minutes, malformed until its member is repaired
type storeTestChunk struct {
	malformed bool
	minutes   []int64
}

func (c storeTestChunk) encode(t *testing.T, enc chunkenc.Encoding, repaired bool) rawChunk {
	t.Helper()
	return histogramChunkAt(t, enc, c.malformed && !repaired, c.minutes...)
}

// storeTestEncodings are the chunk encodings whose samples materialize as histograms
var storeTestEncodings = map[string]chunkenc.Encoding{
	"histogram": chunkenc.EncHistogram, "float histogram": chunkenc.EncFloatHistogram,
}

// storeTestMinutes returns each minute from first to last
func storeTestMinutes(first, last int64) []int64 {
	out := make([]int64, 0, last-first+1)
	for m := first; m <= last; m++ {
		out = append(out, m)
	}
	return out
}

func TestRawCacheSurvivesStorePanics(t *testing.T) {
	lset := podLabels("a")
	const blockEnd = rawBlockMs - 1
	earlier := storeTestChunk{minutes: []int64{28, 29, 30}}
	later := storeTestChunk{minutes: []int64{31, 32, 33}}
	malformedEarlier, malformedLater := earlier, later
	malformedEarlier.malformed, malformedLater.malformed = true, true
	cases := map[string]struct {
		// cached, when set, is held by the block until storeTestCachedTo before the first read
		cached *storeTestChunk
		// served is what the member answers each read with
		served     []storeTestChunk
		mint, maxt int64
		// event is logged for the panic; the read fails with errSourcePanic when it is memberPanicEvent
		event string
	}{
		// completing the block compacts the cached chunk with the member's, materializing the cached one
		"cached malformed chunk": {
			cached: &malformedEarlier, served: []storeTestChunk{later},
			mint: storeTestCachedTo + 1, maxt: blockEnd, event: storePanicEvent,
		},
		// a chunk wholly inside the read, with nothing to compact it with, is still materialized as it is read
		"member malformed chunk": {
			served: []storeTestChunk{malformedLater}, mint: 0, maxt: blockEnd, event: memberPanicEvent,
		},
		// the member's chunk would be compacted with the cached one, which outlives the member's failure
		"member malformed chunk after a cached one": {
			cached: &earlier, served: []storeTestChunk{malformedLater},
			mint: storeTestCachedTo + 1, maxt: blockEnd, event: memberPanicEvent,
		},
		"member malformed adjacent chunks": {
			served: []storeTestChunk{earlier, malformedLater}, mint: 0, maxt: blockEnd, event: memberPanicEvent,
		},
		"member malformed chunk across the read": {
			served: []storeTestChunk{malformedLater}, mint: 0, maxt: 32 * rawTestMinute, event: memberPanicEvent,
		},
	}
	for name, tc := range cases {
		// only a sound cached chunk outlives the failure, which is either the member's or its own
		kept := tc.cached != nil && !tc.cached.malformed
		for encName, enc := range storeTestEncodings {
			for cacheName, newCache := range rawTestCaches(t) {
				t.Run(name+"/"+encName+"/"+cacheName, func(t *testing.T) {
					logs := captureErrors(t)
					src := &chunkedSource{ID: rawTestIdentity}
					serve := func(repaired bool) {
						served := make([]rawChunk, len(tc.served))
						for i, c := range tc.served {
							served[i] = c.encode(t, enc, repaired)
						}
						src.series = []*chunkSeries{histogramSeriesOf(lset, served...)}
					}
					serve(false)
					rc := newRawTestCache(newCache(), RawCacheOptions{})
					rc.setNow(rawBlockMs + rawTestVolatile.Milliseconds())
					ctx := teamRequest(rawTestTeamA)
					matchers := []*labels.Matcher{testMetricMatcher}
					k, ok := rc.key(ctx, src, matchers)
					require.True(t, ok)
					bk := blockKey(k.key, 0)
					if tc.cached != nil {
						rc.engine.Store(bk, &nativedelta.Entry[*rawBlock]{
							Payload: &rawBlock{series: []rawSeries{{lset: lset, chunks: []rawChunk{tc.cached.encode(t, enc, false)}}}},
							Extents: timeseries.ExtentList{extent(0, storeTestCachedTo)},
						})
					}
					read := func() ([]storage.Series, error) {
						t.Helper()
						series, _, err := rc.Select(ctx, Member{Name: rawTestMember, Source: src}, tc.mint, tc.maxt, nil,
							matchers)
						waitWithin(t, rc.Wait)
						return series, err
					}

					// the read's callers are answered before what it keeps is stored, so a store's panic never reaches them
					series, err := read()
					if tc.event == memberPanicEvent {
						require.ErrorIs(t, err, errSourcePanic)
					} else {
						require.NoError(t, err)
						require.Len(t, series, 1)
					}
					require.Contains(t, logs.String(), tc.event)
					require.NotContains(t, logs.String(), flightPanicEvent, "the store recovers its own panic")
					if tc.event == memberPanicEvent {
						require.NotContains(t, logs.String(), storePanicEvent, "a failed read stores nothing")
					}
					// a block that couldn't be kept is dropped, and the selector isn't indexed by what failed
					entry, ok := rc.engine.Retrieve(bk)
					require.Equal(t, kept, ok, "only a sound cached block is kept")
					if kept {
						require.Equal(t, timeseries.ExtentList{extent(0, storeTestCachedTo)}, entry.Extents)
					}
					require.False(t, indexed(rc.RawCache, k))
					// the block's lock stripe is free, and the shared read is forgotten
					mu := &rc.locks[stripe(bk)]
					require.True(t, mu.TryLock(), "the stripe's lock is released")
					mu.Unlock()
					rc.flights.mu.Lock()
					require.Empty(t, rc.flights.flights)
					rc.flights.mu.Unlock()
					require.Len(t, src.takeReads(), 1)

					// once the member's answer is sound, the same block is read and kept again, then served from the cache
					serve(true)
					for _, reads := range []int{1, 0} {
						series, err = read()
						require.NoError(t, err)
						require.Len(t, series, 1)
						require.NotEmpty(t, readPoints(t, series[0]))
						require.Len(t, src.takeReads(), reads)
					}
					entry, ok = rc.engine.Retrieve(bk)
					require.True(t, ok)
					want := extent(tc.mint, tc.maxt)
					if kept {
						want.Start = time.UnixMilli(0)
					}
					require.Equal(t, timeseries.ExtentList{want}, entry.Extents)
					require.True(t, indexed(rc.RawCache, k))
				})
			}
		}
	}
}

// floatHistogramsFrom materializes s's histogram samples from from on
func floatHistogramsFrom(t *testing.T, s storage.Series, from int64) []*histogram.FloatHistogram {
	t.Helper()
	var out []*histogram.FloatHistogram
	it := s.Iterator(nil)
	for vt := it.Seek(from); vt != chunkenc.ValNone; vt = it.Next() {
		require.Equal(t, chunkenc.ValFloatHistogram, vt)
		_, fh := it.AtFloatHistogram(nil)
		out = append(out, fh)
	}
	require.NoError(t, it.Err())
	return out
}

func TestRawCacheRefusesUnreducibleSampleHistograms(t *testing.T) {
	const blockEnd = rawBlockMs - 1
	lset := podLabels("a")
	served := []int64{31, 32, 33}
	from := served[0] * rawTestMinute
	for _, cached := range []bool{false, true} {
		for kind, encode := range sampleHistogramKinds {
			for cacheName, newCache := range rawTestCaches(t) {
				t.Run(fmt.Sprintf("cached=%t/%s/%s", cached, kind, cacheName), func(t *testing.T) {
					logs := captureErrors(t)
					var repaired atomic.Bool
					var mu sync.Mutex
					var reads [][2]int64
					takeReads := func() [][2]int64 {
						mu.Lock()
						defer mu.Unlock()
						out := reads
						reads = nil
						return out
					}
					// the member answers remote reads as samples, its histograms' spans unreducible until it is repaired
					src := newTestRemoteRead(t, func(w http.ResponseWriter, r *http.Request) {
						q := decodeTestRequest(t, r).Queries[0]
						mu.Lock()
						reads = append(reads, [2]int64{q.StartTimestampMs, q.EndTimestampMs})
						mu.Unlock()
						ts := &prompb.TimeSeries{Labels: testLabels("a")}
						for _, m := range served {
							if at := m * rawTestMinute; at >= q.StartTimestampMs && at <= q.EndTimestampMs {
								ts.Histograms = append(ts.Histograms, encode(at, fineHistogram(!repaired.Load())))
							}
						}
						writeSamples(t, w, ts)
					})
					src.SourceIdentity = SourceIdentity{ID: rawTestIdentity}
					rc := newRawTestCache(newCache(), RawCacheOptions{})
					rc.setNow(rawBlockMs + rawTestVolatile.Milliseconds())
					ctx := teamRequest(rawTestTeamA)
					matchers := []*labels.Matcher{testMetricMatcher}
					k, ok := rc.key(ctx, src, matchers)
					require.True(t, ok)
					bk := blockKey(k.key, 0)
					var mint int64
					if cached {
						mint = storeTestCachedTo + 1
						rc.engine.Store(bk, &nativedelta.Entry[*rawBlock]{
							Payload: &rawBlock{series: []rawSeries{{lset: lset, chunks: []rawChunk{
								histogramChunkAt(t, storeTestEncodings[kind], false, 28, 29, 30),
							}}}},
							Extents: timeseries.ExtentList{extent(0, storeTestCachedTo)},
						})
					}
					read := func() ([]storage.Series, error) {
						t.Helper()
						series, _, err := rc.Select(ctx, Member{Name: rawTestMember, Source: src}, mint, blockEnd, nil, matchers)
						waitWithin(t, rc.Wait)
						return series, err
					}

					// the member's answer fails to decode, so nothing panics later and no block is kept or dropped for it
					_, err := read()
					require.ErrorIs(t, err, errRemoteRead)
					require.ErrorIs(t, err, histogram.ErrHistogramSpanNegativeOffset)
					for _, event := range []string{memberPanicEvent, storePanicEvent, flightPanicEvent} {
						require.NotContains(t, logs.String(), event)
					}
					entry, ok := rc.engine.Retrieve(bk)
					require.Equal(t, cached, ok, "only the cached block is kept")
					if cached {
						require.Equal(t, timeseries.ExtentList{extent(0, storeTestCachedTo)}, entry.Extents)
					}
					require.False(t, indexed(rc.RawCache, k))
					require.Equal(t, [][2]int64{{mint, blockEnd}}, takeReads())

					// once repaired, the block is kept and then served from the cache, its histograms read as the member's
					// own answer reads them, reduced to a schema the engine reads
					repaired.Store(true)
					for _, want := range []int{1, 0} {
						series, err := read()
						require.NoError(t, err)
						require.Len(t, series, 1)
						require.Len(t, takeReads(), want)
						got := floatHistogramsFrom(t, series[0], from)
						direct, _, err := src.Select(ctx, mint, blockEnd, nil, matchers...)
						require.NoError(t, err)
						require.Len(t, direct, 1)
						takeReads()
						wantHistograms := floatHistogramsFrom(t, direct[0], from)
						require.Len(t, got, len(served))
						require.Len(t, wantHistograms, len(served))
						for i, fh := range got {
							require.True(t, wantHistograms[i].Equals(fh), "read %v, want %v", fh, wantHistograms[i])
							require.Equal(t, histogram.ExponentialSchemaMax, fh.Schema)
						}
					}
					entry, ok = rc.engine.Retrieve(bk)
					require.True(t, ok)
					require.Equal(t, timeseries.ExtentList{extent(0, blockEnd)}, entry.Extents)
					require.True(t, indexed(rc.RawCache, k))
				})
			}
		}
	}
}

func TestRawCacheKeepsBlockOverMalformedMemberHead(t *testing.T) {
	lset := podLabels("a")
	const now, tail = 41 * rawTestMinute, storeTestCachedTo + 1
	cutoff := now - rawTestVolatile.Milliseconds()
	head := storeTestMinutes(31, 40)
	for encName, enc := range storeTestEncodings {
		for cacheName, newCache := range rawTestCaches(t) {
			t.Run(encName+"/"+cacheName, func(t *testing.T) {
				logs := captureErrors(t)
				src := &chunkedSource{ID: rawTestIdentity}
				serve := func(malformed bool) {
					src.series = []*chunkSeries{histogramSeriesOf(lset, histogramChunkAt(t, enc, malformed, head...))}
				}
				serve(true)
				rc := newRawTestCache(newCache(), RawCacheOptions{})
				rc.setNow(now)
				ctx := teamRequest(rawTestTeamA)
				matchers := []*labels.Matcher{testMetricMatcher}
				k, ok := rc.key(ctx, src, matchers)
				require.True(t, ok)
				bk := blockKey(k.key, 0)
				rc.engine.Store(bk, &nativedelta.Entry[*rawBlock]{
					Payload: &rawBlock{series: []rawSeries{{lset: lset, chunks: []rawChunk{histogramChunkAt(t, enc, false, 28, 29, 30)}}}},
					Extents: timeseries.ExtentList{extent(0, storeTestCachedTo)},
				})
				read := func() ([]storage.Series, error) {
					t.Helper()
					series, _, err := rc.Select(ctx, Member{Name: rawTestMember, Source: src}, 0, now, nil, matchers)
					waitWithin(t, rc.Wait)
					return series, err
				}
				// the member's head chunk crosses the volatile cutoff, so its kept part would be encoded again as it is
				// stored; its layout fails the member's read instead, each time, and only the uncached tail is read again
				for range 2 {
					_, err := read()
					entry, ok := rc.engine.Retrieve(bk)
					require.True(t, ok, "the cached block outlives the member's failure")
					require.Equal(t, timeseries.ExtentList{extent(0, storeTestCachedTo)}, entry.Extents)
					require.Equal(t, [][2]int64{{tail, now}}, src.takeReads())
					require.ErrorIs(t, err, errSourcePanic)
				}
				require.Contains(t, logs.String(), memberPanicEvent)
				require.NotContains(t, logs.String(), storePanicEvent)

				serve(false)
				series, err := read()
				require.NoError(t, err)
				require.Len(t, series, 1)
				require.Len(t, readPoints(t, series[0]), 3+len(head))
				require.Equal(t, [][2]int64{{tail, now}}, src.takeReads())
				entry, ok := rc.engine.Retrieve(bk)
				require.True(t, ok)
				require.Equal(t, timeseries.ExtentList{extent(0, cutoff)}, entry.Extents)
				// later reads take only the volatile window from the member
				_, err = read()
				require.NoError(t, err)
				require.Equal(t, [][2]int64{{cutoff + 1, now}}, src.takeReads())
			})
		}
	}
}

func TestRawCacheKeepBlockDropsOnlyItsOwnFailure(t *testing.T) {
	lset := podLabels("a")
	cases := map[string]struct {
		// cachedMalformed is whether the block's chunk is malformed, rather than the stored one
		cachedMalformed bool
		stored          []int64
		hi              int64
		kept            bool
	}{
		// cropping the stored chunk at the store's end reads only its own samples, so the block is kept as it was
		"crop": {stored: storeTestMinutes(31, 40), hi: 36 * rawTestMinute, kept: true},
		// completing the block compacts its own chunk with the stored one, so the block is dropped
		"merge": {cachedMalformed: true, stored: []int64{31, 32, 33}, hi: rawBlockMs - 1},
	}
	for name, tc := range cases {
		for encName, enc := range storeTestEncodings {
			for cacheName, newCache := range rawTestCaches(t) {
				t.Run(name+"/"+encName+"/"+cacheName, func(t *testing.T) {
					logs := captureErrors(t)
					rc := newRawTestCache(newCache(), RawCacheOptions{})
					bk := blockKey(storeTestKey, 0)
					cached := histogramChunkAt(t, enc, tc.cachedMalformed, 28, 29, 30)
					rc.engine.Store(bk, &nativedelta.Entry[*rawBlock]{
						Payload: &rawBlock{series: []rawSeries{{lset: lset, chunks: []rawChunk{cached}}}},
						Extents: timeseries.ExtentList{extent(0, storeTestCachedTo)},
					})
					stored := histogramChunkAt(t, enc, !tc.cachedMalformed, tc.stored...)
					err := rc.keepBlock(bk, []rawSeries{{lset: lset, chunks: []rawChunk{stored}}}, storeTestCachedTo+1, tc.hi)
					require.ErrorIs(t, err, errStorePanic)
					require.Contains(t, logs.String(), storePanicEvent)
					entry, ok := rc.engine.Retrieve(bk)
					require.Equal(t, tc.kept, ok)
					if tc.kept {
						require.Equal(t, timeseries.ExtentList{extent(0, storeTestCachedTo)}, entry.Extents)
					}
					mu := &rc.locks[stripe(bk)]
					require.True(t, mu.TryLock(), "the stripe's lock is released")
					mu.Unlock()
				})
			}
		}
	}
}

// staleFirstChunk is a chunk whose first sample reads as a stale marker, which carries no bucket layout
type staleFirstChunk struct{ chunkenc.Chunk }

func (c staleFirstChunk) Iterator(chunkenc.Iterator) chunkenc.Iterator {
	return &staleFirstIterator{Iterator: c.Chunk.Iterator(nil)}
}

type staleFirstIterator struct {
	chunkenc.Iterator
	read int
}

func (it *staleFirstIterator) Next() chunkenc.ValueType {
	it.read++
	return it.Iterator.Next()
}

func (it *staleFirstIterator) AtHistogram(h *histogram.Histogram) (int64, *histogram.Histogram) {
	if it.read == 1 {
		return it.AtT(), &histogram.Histogram{Sum: math.Float64frombits(value.StaleNaN)}
	}
	return it.Iterator.AtHistogram(h)
}

func (it *staleFirstIterator) AtFloatHistogram(fh *histogram.FloatHistogram) (int64, *histogram.FloatHistogram) {
	if it.read == 1 {
		return it.AtT(), &histogram.FloatHistogram{Sum: math.Float64frombits(value.StaleNaN)}
	}
	return it.Iterator.AtFloatHistogram(fh)
}

// panicOf returns what fn panics with, or nil
func panicOf(fn func()) (p any) {
	defer func() { p = recover() }()
	fn()
	return nil
}

func TestChunkReaderMaterializesPastStaleMarkers(t *testing.T) {
	for encName, enc := range storeTestEncodings {
		t.Run(encName, func(t *testing.T) {
			var r chunkReader
			sp, rising, err := r.readSpan(staleFirstChunk{histogramChunkAt(t, enc, false, 31, 32, 33).chk})
			require.NoError(t, err)
			require.True(t, rising)
			require.Equal(t, span{mint: 31 * rawTestMinute, maxt: 33 * rawTestMinute}, sp)
			// the first sample read with the chunk's layout materializes it
			malformed := staleFirstChunk{histogramChunkAt(t, enc, true, 31, 32, 33).chk}
			p, _ := panicOf(func() { _, _, _ = r.readSpan(malformed) }).(error)
			require.ErrorIs(t, p, histogram.ErrHistogramSpanNegativeOffset)
		})
	}
}

func TestChunkReaderMaterializesWithoutAllocating(t *testing.T) {
	for encName, enc := range storeTestEncodings {
		t.Run(encName, func(t *testing.T) {
			chk := histogramChunkAt(t, enc, false, storeTestMinutes(1, rawChunkSamples)...).chk
			var r chunkReader
			// the first read allocates the reader's iterator and histogram, which later reads reuse
			_, _, err := r.readSpan(chk)
			require.NoError(t, err)
			it := chk.Iterator(nil)
			through := testing.AllocsPerRun(100, func() {
				it = chk.Iterator(it)
				for vt := it.Next(); vt != chunkenc.ValNone; vt = it.Next() {
					_ = it.AtT()
				}
			})
			read := testing.AllocsPerRun(100, func() { _, _, _ = r.readSpan(chk) })
			require.Equal(t, through, read, "materializing a layout costs nothing beyond reading its chunk through")
		})
	}
}

// answerWith returns a shared read answering v, counting its runs in runs when set
func answerWith(v int, runs *atomic.Int32) func(context.Context, shareFunc) int {
	return func(context.Context, shareFunc) int {
		if runs != nil {
			runs.Add(1)
		}
		return v
	}
}

func TestFlightGroupAnswersBeforeAfter(t *testing.T) {
	var g flightGroup[int]
	var runs atomic.Int32
	storing, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	do := func(v int, after func(int)) (got int, err error) {
		t.Helper()
		waitWithin(t, func() {
			got, err = g.do(context.Background(), rawTestMember, &selectCharge{}, answerWith(v, &runs), after)
		})
		return got, err
	}
	// the caller is answered while after still runs
	got, err := do(1, func(int) {
		close(storing)
		<-release
	})
	require.NoError(t, err)
	require.Equal(t, 1, got)
	waitWithin(t, func() { <-storing })
	// a caller arriving meanwhile joins the finished flight rather than running another
	got, err = do(2, nil)
	require.NoError(t, err)
	require.Equal(t, 1, got, "joined the flight still storing")
	require.Equal(t, int32(1), runs.Load())
	releaseOnce()
	waitWithin(t, g.running.Wait)
	g.mu.Lock()
	require.Empty(t, g.flights, "the flight is forgotten once after returns")
	g.mu.Unlock()
}

func TestRawCacheAnswersBeforeStoring(t *testing.T) {
	const blockEnd = rawBlockMs - 1
	for cacheName, newCache := range rawTestCaches(t) {
		t.Run(cacheName, func(t *testing.T) {
			src := rawTestSource(t)
			rc := newRawTestCache(newCache(), RawCacheOptions{})
			ctx := teamRequest(rawTestTeamA)
			matchers := []*labels.Matcher{testMetricMatcher}
			k, ok := rc.key(ctx, src, matchers)
			require.True(t, ok)
			bk := blockKey(k.key, 0)
			// holding the block's lock stripe holds the store of what a read keeps
			mu := &rc.locks[stripe(bk)]
			mu.Lock()
			unlock := sync.OnceFunc(mu.Unlock)
			t.Cleanup(unlock)
			want := wantPoints(t, src, 0, blockEnd)
			read := func() {
				t.Helper()
				var series []storage.Series
				var err error
				waitWithin(t, func() {
					series, _, err = rc.Select(ctx, Member{Name: rawTestMember, Source: src}, 0, blockEnd, nil, matchers)
				})
				require.NoError(t, err)
				require.Equal(t, want, pointsOf(t, series))
			}
			// the read's callers are answered while what it keeps waits to be stored, and a read of the same samples
			// meanwhile joins it rather than reading the member again
			read()
			read()
			require.Equal(t, [][2]int64{{0, blockEnd}}, src.takeReads())
			_, ok = rc.engine.Retrieve(bk)
			require.False(t, ok, "nothing is stored while the stripe is held")
			unlock()
			waitWithin(t, rc.Wait)
			_, ok = rc.engine.Retrieve(bk)
			require.True(t, ok)
			require.True(t, indexed(rc.RawCache, k))
			read()
			require.Empty(t, src.takeReads())
		})
	}
}

func TestFlightGroupSurvivesPanickingAfter(t *testing.T) {
	logs := captureErrors(t)
	var g flightGroup[int]
	got, err := g.do(context.Background(), rawTestMember, &selectCharge{}, answerWith(1, nil), func(int) { panic(storeTestBug) })
	require.NoError(t, err)
	require.Equal(t, 1, got)
	waitWithin(t, g.running.Wait)
	require.Contains(t, logs.String(), flightPanicEvent)
	g.mu.Lock()
	require.Empty(t, g.flights, "the flight is forgotten")
	g.mu.Unlock()
	// a later caller runs its own read rather than joining the one whose after panicked
	got, err = g.do(context.Background(), rawTestMember, &selectCharge{}, answerWith(2, nil), nil)
	require.NoError(t, err)
	require.Equal(t, 2, got)
	waitWithin(t, g.running.Wait)
}

func TestFlightGroupStartsAnotherReadOnceOneEnds(t *testing.T) {
	var g flightGroup[int]
	var runs atomic.Int32
	stopped, finish := make(chan struct{}), make(chan struct{})
	finishOnce := sync.OnceFunc(func() { close(finish) })
	t.Cleanup(finishOnce)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var errFirst error
	wg.Go(func() {
		_, errFirst = g.do(ctx, rawTestMember, &selectCharge{}, func(rctx context.Context, _ shareFunc) int {
			runs.Add(1)
			<-rctx.Done()
			close(stopped)
			<-finish
			return 1
		}, nil)
	})
	require.Eventually(t, func() bool {
		return flightIs(&g, rawTestMember, func(f *flight[int]) bool { return len(f.callers) == 1 })
	}, storeTestWait, time.Millisecond)
	// its only caller leaving ends the read, which is still finishing when another caller arrives and starts its own
	cancel()
	waitWithin(t, func() { <-stopped })
	waitWithin(t, wg.Wait)
	require.ErrorIs(t, errFirst, context.Canceled)
	var got int
	var err error
	waitWithin(t, func() {
		got, err = g.do(context.Background(), rawTestMember, &selectCharge{}, answerWith(2, &runs), nil)
	})
	require.NoError(t, err)
	require.Equal(t, 2, got)
	require.Equal(t, int32(2), runs.Load())
	finishOnce()
	waitWithin(t, g.running.Wait)
	g.mu.Lock()
	require.Empty(t, g.flights)
	g.mu.Unlock()
}
