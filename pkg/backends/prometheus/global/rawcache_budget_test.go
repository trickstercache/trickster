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
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/klauspost/compress/snappy"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const (
	budgetTestFrames = 50
	budgetTestPause  = 2 * time.Millisecond
)

var testMetricMatcher = labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric)

// waitForCallers waits until the shared read of the test metric from mint to maxt has want callers
func waitForCallers(t *testing.T, rc *rawTestCache, mint, maxt int64, want int) {
	t.Helper()
	k, _ := rc.key(teamRequest(rawTestTeamA), rawTestSource(t), []*labels.Matcher{testMetricMatcher})
	key := k + "/" + strconv.FormatInt(mint, 10) + "/" + strconv.FormatInt(maxt, 10)
	require.Eventually(t, func() bool {
		rc.flights.mu.Lock()
		f := rc.flights.flights[key]
		rc.flights.mu.Unlock()
		if f == nil {
			return false
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.callers) == want
	}, 5*time.Second, time.Millisecond)
}

// streamingSource answers as a member streaming its answer does: each chunk is counted against the read's
// budget as it is sent, and it records how many reads it serves at once
type streamingSource struct {
	*chunkedSource
	// pause, when set, comes before each chunk, so reads overlap
	pause          time.Duration
	inflight, peak atomic.Int64
	// sent counts the samples sent within each read's range
	sent atomic.Int64
}

func (s *streamingSource) Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	n := s.inflight.Add(1)
	defer s.inflight.Add(-1)
	for peak := s.peak.Load(); n > peak && !s.peak.CompareAndSwap(peak, n); peak = s.peak.Load() {
	}
	series, warnings, err := s.chunkedSource.Select(ctx, mint, maxt, hints, matchers...)
	if err != nil {
		return nil, nil, err
	}
	budget := budgetFrom(ctx)
	for _, s2 := range series {
		cs := s2.(*chunkSeries)
		counted := false
		for i, c := range cs.chunks {
			if s.pause > 0 {
				select {
				case <-time.After(s.pause):
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
			}
			n, err := samplesWithin(c.(chunkenc.Chunk), cs.spans[i], mint, maxt)
			if err != nil {
				return nil, nil, err
			}
			var newSeries int64
			if n > 0 && !counted {
				newSeries, counted = 1, true
			}
			if err := budget.add(newSeries, n); err != nil {
				return nil, nil, err
			}
			s.sent.Add(n)
		}
	}
	return series, warnings, nil
}

func TestRawCacheStopsReadingOnceOverBudget(t *testing.T) {
	src := &streamingSource{chunkedSource: rawTestSource(t), pause: time.Millisecond}
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
	// half of each hour is cached, so a read of eight hours has eight gaps, each far under the limit
	for start := int64(0); start < 8*rawTestHour; start += rawTestHour {
		_, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), src, start, start+30*rawTestMinute)
		require.NoError(t, err)
	}
	src.takeReads()
	src.sent.Store(0)
	src.peak.Store(0)
	// each gap holds about 240 samples, and the first gaps read together hold more than the limit
	const limit = 300
	budget := &Budget{MaxSamples: limit}
	_, _, err := rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), budget), src, 0, 8*rawTestHour)
	require.ErrorIs(t, err, ErrFetchLimit)
	// reading stopped once the evaluation's budget ran out, each read under way sending at most a chunk more
	require.LessOrEqual(t, src.sent.Load(), int64(limit+rawGapReads*rawTestChunk))
	// of the eight gaps, only a few are read at once, and those not begun when the budget ran out never are
	require.LessOrEqual(t, src.peak.Load(), int64(rawGapReads))
	require.Less(t, src.peak.Load(), int64(8))
	reads := src.takeReads()
	require.LessOrEqual(t, len(reads), rawGapReads)
	require.Less(t, len(reads), 8)
	// what a stopped read held wasn't kept, so its gap is read again once the budget allows
	series, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), src, 0, 8*rawTestHour)
	require.NoError(t, err)
	require.NotEmpty(t, src.takeReads())
	require.Equal(t, wantPoints(t, src.chunkedSource, 0, 8*rawTestHour), pointsOf(t, series))
}

func TestRawCacheSharedReadChargesEachCaller(t *testing.T) {
	const mint, maxt = 4 * rawTestHour, 6 * rawTestHour
	src := &streamingSource{chunkedSource: rawTestSource(t)}
	src.gate = make(chan struct{})
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
	small, large := &Budget{MaxSamples: 100}, &Budget{}
	var wg sync.WaitGroup
	var errSmall, errLarge error
	var served []storage.Series
	wg.Go(func() {
		_, _, errSmall = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), small), src, mint, maxt)
	})
	wg.Go(func() {
		served, _, errLarge = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), large), src, mint, maxt)
	})
	waitForCallers(t, rc, mint, maxt, 2)
	close(src.gate)
	wg.Wait()
	// the caller whose budget ran out leaves the read, which goes on for the other
	require.ErrorIs(t, errSmall, ErrFetchLimit)
	require.NoError(t, errLarge)
	want := wantPoints(t, src.chunkedSource, mint, maxt)
	require.Equal(t, want, pointsOf(t, served))
	require.Len(t, src.takeReads(), 1)
	series, samples := large.Fetched()
	require.Equal(t, int64(len(want)), series)
	require.Equal(t, int64(len(want["{__name__=\"up\", pod=\"a\"}"])+len(want["{__name__=\"up\", pod=\"b\"}"])), samples)
}

func TestFlightGroupChargesEveryCaller(t *testing.T) {
	var g flightGroup[int]
	ctx := context.Background()
	charged, release := make(chan struct{}), make(chan struct{})
	read := func(_ context.Context, share func(int64) error) int {
		if share(50) != nil {
			return -1
		}
		close(charged)
		<-release
		if share(50) != nil {
			return -1
		}
		return 1
	}
	first := &Budget{MaxSamples: 1000}
	var wg sync.WaitGroup
	var result, refused int
	var errFirst, errLater error
	wg.Go(func() { result, errFirst = g.do(ctx, "k", first, read, nil) })
	<-charged
	// a caller joining later pays what the read has read so far, and is refused when it can't
	refusedCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := g.do(refusedCtx, "k", &Budget{MaxSamples: 10}, read, nil)
	require.ErrorIs(t, err, ErrFetchLimit)
	// one that can pays, then leaves once the read costs more than it allows
	later := &Budget{MaxSamples: 60}
	wg.Go(func() { refused, errLater = g.do(ctx, "k", later, read, nil) })
	require.Eventually(t, func() bool {
		g.mu.Lock()
		f := g.flights["k"]
		g.mu.Unlock()
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.callers) == 2
	}, 5*time.Second, time.Millisecond)
	close(release)
	wg.Wait()
	require.NoError(t, errFirst)
	require.Equal(t, 1, result)
	require.ErrorIs(t, errLater, ErrFetchLimit)
	require.Zero(t, refused)
	_, samples := first.Fetched()
	require.Equal(t, int64(100), samples)

	// a read no caller can afford stops reading
	stopped := make(chan int, 1)
	_, err = g.do(ctx, "solo", &Budget{MaxSamples: 10}, func(_ context.Context, share func(int64) error) int {
		if share(50) != nil {
			stopped <- -1
			return -1
		}
		stopped <- 1
		return 1
	}, nil)
	require.ErrorIs(t, err, ErrFetchLimit)
	require.Equal(t, -1, <-stopped)
	g.running.Wait()
}

// framedRemoteRead serves budgetTestFrames frames of 120 samples each, pausing between them, and records
// how many it wrote before the client went away
func framedRemoteRead(t *testing.T, written *atomic.Int64) *RemoteRead {
	t.Helper()
	rr := newTestRemoteRead(t, func(w http.ResponseWriter, r *http.Request) {
		_ = decodeTestRequest(t, r)
		w.Header().Set(headers.NameContentType, contentTypeStreamed+"; proto=prometheus.ChunkedReadResponse")
		for i := range budgetTestFrames {
			points := make([]testPoint, rawTestChunk)
			for j := range points {
				points[j] = testPoint{t: int64(i*rawTestChunk+j) * rawTestStep, f: float64(j)}
			}
			var body bytes.Buffer
			writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{xorChunk(t, points...)}})
			if _, err := w.Write(body.Bytes()); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			written.Add(1)
			select {
			case <-time.After(budgetTestPause):
			case <-r.Context().Done():
				return
			}
		}
	})
	rr.ID = rawTestIdentity
	return rr
}

func TestRawCacheStopsTransportsOverBudget(t *testing.T) {
	const limit = 3 * rawTestChunk
	maxt := int64(budgetTestFrames*rawTestChunk) * rawTestStep
	t.Run("remote read", func(t *testing.T) {
		var written atomic.Int64
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
		rc.setNow(maxt + rawTestHour)
		budget := &Budget{MaxSamples: limit}
		_, _, err := rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), budget), framedRemoteRead(t, &written), 0, maxt)
		require.ErrorIs(t, err, ErrFetchLimit)
		_, samples := budget.Fetched()
		require.LessOrEqual(t, samples, int64(limit+rawTestChunk), "decoding stopped within a frame of the limit")
		require.Eventually(t, func() bool { return written.Load() < budgetTestFrames }, 5*time.Second, time.Millisecond)
	})
	t.Run("storeapi", func(t *testing.T) {
		store := &testStore{}
		for i := range budgetTestFrames {
			points := make([]testPoint, rawTestChunk)
			for j := range points {
				points[j] = testPoint{t: int64(i*rawTestChunk+j) * rawTestStep, f: float64(j)}
			}
			store.frames = append(store.frames, encodeTestSeries([]string{labels.MetricName, testMetric, testPod, "a"},
				spanned(encodeTestChunk(storeChunkXOR, xorData(t, points...), true), points[0].t, points[len(points)-1].t)))
		}
		source := store.serve(t)
		source.ID = rawTestIdentity
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
		rc.setNow(maxt + rawTestHour)
		budget := &Budget{MaxSamples: limit}
		_, _, err := rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), budget), source, 0, maxt)
		require.ErrorIs(t, err, ErrFetchLimit)
		_, samples := budget.Fetched()
		require.LessOrEqual(t, samples, int64(limit+rawTestChunk), "decoding stopped within a message of the limit")
	})
}

// samplesRemoteRead answers with src's samples in each read's range, as a member answering with samples does
func samplesRemoteRead(t *testing.T, src *chunkedSource) *RemoteRead {
	t.Helper()
	return newTestRemoteRead(t, func(w http.ResponseWriter, r *http.Request) {
		q := decodeTestRequest(t, r).Queries[0]
		var resp prompb.ReadResponse
		result := &prompb.QueryResult{}
		for _, cs := range src.series {
			ts := &prompb.TimeSeries{Labels: prompb.FromLabels(cs.lset, nil)}
			for _, p := range readPoints(t, cs) {
				if p.t >= q.StartTimestampMs && p.t <= q.EndTimestampMs {
					ts.Samples = append(ts.Samples, prompb.Sample{Timestamp: p.t, Value: p.f})
				}
			}
			if len(ts.Samples) > 0 {
				result.Timeseries = append(result.Timeseries, ts)
			}
		}
		resp.Results = []*prompb.QueryResult{result}
		data, err := resp.Marshal()
		require.NoError(t, err)
		_, _ = w.Write(snappy.Encode(nil, data))
	})
}

// sparseSource adds a series sampled every 40 minutes, whose one chunk spans intervals holding none of
// its samples
func sparseSource(t *testing.T) *chunkedSource {
	t.Helper()
	src := rawTestSource(t)
	var points []testPoint
	for ts := int64(0); ts <= rawTestEnd; ts += 40 * rawTestMinute {
		points = append(points, testPoint{t: ts, f: float64(ts)})
	}
	src.series = append(src.series, chunked(t, podLabels("sparse"), points))
	return src
}

func TestRawCacheCountsExactlyWhatItServes(t *testing.T) {
	windows := map[string][2]int64{
		"within a block, between sparse samples": {3*rawTestHour + 7_000, 3*rawTestHour + 19*rawTestMinute},
		"across a block boundary":                {2*rawTestHour - 10*rawTestMinute, 2*rawTestHour + 10*rawTestMinute},
	}
	// caches filled by one broad read, or by hourly reads, hold differently cut chunks
	histories := map[string]func(rc *rawTestCache, src *chunkedSource){
		"one broad read": func(rc *rawTestCache, src *chunkedSource) {
			_, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), src, 0, rawTestEnd)
			require.NoError(t, err)
		},
		"hourly reads": func(rc *rawTestCache, src *chunkedSource) {
			for start := int64(0); start < rawTestEnd; start += rawTestHour {
				_, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), src, start, start+rawTestHour-1)
				require.NoError(t, err)
			}
		},
	}
	src := sparseSource(t)
	samples := samplesRemoteRead(t, src)
	cold := func(window [2]int64, budget *Budget) error {
		q := NewQueryable([]Member{{Name: rawTestMember, Source: samples}}, nil, merge.TSMDeduplicationChain, false, nil)
		qr, err := q.Querier(window[0], window[1])
		require.NoError(t, err)
		set := qr.Select(WithBudget(teamRequest(rawTestTeamA), budget), true,
			&storage.SelectHints{Start: window[0], End: window[1]}, testMetricMatcher)
		for set.Next() {
		}
		return set.Err()
	}
	for name, window := range windows {
		want := &Budget{}
		require.NoError(t, cold(window, want))
		wantSeries, wantSamples := want.Fetched()
		require.Positive(t, wantSamples, name)
		// a cold read with exactly the samples it serves succeeds, and with one fewer fails
		require.NoError(t, cold(window, &Budget{MaxSamples: wantSamples, MaxSeries: wantSeries}))
		require.ErrorIs(t, cold(window, &Budget{MaxSamples: wantSamples - 1}), ErrFetchLimit)
		for history, fill := range histories {
			for cacheName, newCache := range rawTestCaches(t) {
				t.Run(name+"/"+history+"/"+cacheName, func(t *testing.T) {
					rc := newRawTestCache(newCache(), RawCacheOptions{})
					fill(rc, src)
					src.takeReads()
					got := &Budget{}
					_, _, err := rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), got), src, window[0], window[1])
					require.NoError(t, err)
					require.Empty(t, src.takeReads(), "the window is served from the cache")
					gotSeries, gotSamples := got.Fetched()
					require.Equal(t, wantSeries, gotSeries, "the sparse series has no sample in range, so isn't counted")
					require.Equal(t, wantSamples, gotSamples)
					// limits between the samples served and those their cached chunks hold meet the same outcome
					exact := &Budget{MaxSamples: wantSamples, MaxSeries: wantSeries}
					_, _, err = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), exact), src, window[0], window[1])
					require.NoError(t, err)
					_, _, err = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), &Budget{MaxSamples: wantSamples - 1}),
						src, window[0], window[1])
					require.ErrorIs(t, err, ErrFetchLimit)
				})
			}
		}
	}
}
