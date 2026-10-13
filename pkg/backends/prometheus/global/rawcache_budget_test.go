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
	"maps"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
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

// waitForFlight waits until the shared read of src's test metric from mint to maxt meets cond, which reads it locked
func waitForFlight(t *testing.T, rc *rawTestCache, src Source, mint, maxt int64, cond func(f *flight[fetched]) bool) {
	t.Helper()
	k, _ := rc.key(teamRequest(rawTestTeamA), src, []*labels.Matcher{testMetricMatcher})
	key := k.key + "/" + strconv.FormatInt(mint, 10) + "/" + strconv.FormatInt(maxt, 10)
	require.Eventually(t, func() bool {
		rc.flights.mu.Lock()
		f := rc.flights.flights[key]
		rc.flights.mu.Unlock()
		if f == nil {
			return false
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return cond(f)
	}, 5*time.Second, time.Millisecond)
}

// waitForCallers waits until the shared read of the test metric from mint to maxt has want callers
func waitForCallers(t *testing.T, rc *rawTestCache, mint, maxt int64, want int) {
	t.Helper()
	waitForFlight(t, rc, rawTestSource(t), mint, maxt, func(f *flight[fetched]) bool { return len(f.callers) == want })
}

// streamingSource answers as a member streaming its answer does: each chunk is counted against the read's
// budget as it is sent, each series identified as a decoder does, and it records how many reads it serves at once
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
			var ids []uint64
			if n > 0 && !counted {
				newSeries, counted = 1, true
				if budget.identifies() {
					ids = []uint64{cs.lset.Hash()}
				}
			}
			if err := budget.addSeries(newSeries, n, ids); err != nil {
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
	small, few, large := &Budget{MaxSamples: 100}, &Budget{MaxSeries: 1}, &Budget{}
	var wg sync.WaitGroup
	var errSmall, errFew, errLarge error
	var served []storage.Series
	wg.Go(func() {
		_, _, errSmall = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), small), src, mint, maxt)
	})
	wg.Go(func() {
		_, _, errFew = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), few), src, mint, maxt)
	})
	wg.Go(func() {
		served, _, errLarge = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), large), src, mint, maxt)
	})
	waitForCallers(t, rc, mint, maxt, 3)
	close(src.gate)
	wg.Wait()
	// the callers whose samples or series ran out leave the read, which goes on for the other
	require.ErrorIs(t, errSmall, ErrFetchLimit)
	require.ErrorIs(t, errFew, ErrFetchLimit)
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
	read := func(_ context.Context, share shareFunc) int {
		if share(2, 50, nil) != nil {
			return -1
		}
		close(charged)
		<-release
		if share(1, 50, nil) != nil {
			return -1
		}
		return 1
	}
	first := &Budget{MaxSamples: 1000}
	var wg sync.WaitGroup
	var result, refused int
	var errFirst, errLater error
	wg.Go(func() { result, errFirst = g.do(ctx, "k", &selectCharge{budget: first}, read, nil) })
	<-charged
	// a caller joining later pays the series and samples the read has read so far, and is refused when it can't
	for _, budget := range []*Budget{{MaxSamples: 10}, {MaxSeries: 1}} {
		refusedCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := g.do(refusedCtx, "k", &selectCharge{budget: budget}, read, nil)
		cancel()
		require.ErrorIs(t, err, ErrFetchLimit)
	}
	// one that can pays, then leaves once the read costs more than it allows
	later := &Budget{MaxSeries: 2, MaxSamples: 1000}
	wg.Go(func() { refused, errLater = g.do(ctx, "k", &selectCharge{budget: later}, read, nil) })
	// it pays as it joins, its samples before its series
	require.Eventually(t, func() bool { series, _ := later.Fetched(); return series > 0 }, 5*time.Second, time.Millisecond)
	series, samples := later.Fetched()
	require.Equal(t, [2]int64{2, 50}, [2]int64{series, samples})
	require.True(t, flightIs(&g, "k", func(f *flight[int]) bool { return len(f.callers) == 2 }))
	close(release)
	wg.Wait()
	require.NoError(t, errFirst)
	require.Equal(t, 1, result)
	require.ErrorIs(t, errLater, ErrFetchLimit)
	require.Zero(t, refused)
	series, samples = first.Fetched()
	require.Equal(t, [2]int64{3, 100}, [2]int64{series, samples})

	// a read no caller can afford stops reading
	stopped := make(chan int, 1)
	solo := &selectCharge{budget: &Budget{MaxSeries: 1}}
	_, err := g.do(ctx, "solo", solo, func(_ context.Context, share shareFunc) int {
		if share(2, 0, nil) != nil {
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

func TestFlightGroupReplaysSeriesIDsToLateCallers(t *testing.T) {
	var g flightGroup[int]
	ctx := context.Background()
	charged, release := make(chan struct{}), make(chan struct{})
	read := func(_ context.Context, share shareFunc) int {
		if share(2, 0, []uint64{1, 2}) != nil {
			return -1
		}
		close(charged)
		<-release
		return 1
	}
	var wg sync.WaitGroup
	var errFirst, errJoined error
	wg.Go(func() { _, errFirst = g.do(ctx, "k", &selectCharge{}, read, nil) })
	<-charged
	// a select whose other read reported series 3 joins, paying for the two series the read holds that are new to it
	joined := newSelectCharge(&Budget{MaxSeries: 3}, true)
	require.NoError(t, joined.pay(1, 0, []uint64{3}))
	wg.Go(func() { _, errJoined = g.do(ctx, "k", joined, read, nil) })
	require.Eventually(t, func() bool { series, _ := joined.budget.Fetched(); return series > 1 }, 5*time.Second,
		time.Millisecond)
	series, _ := joined.budget.Fetched()
	require.Equal(t, int64(3), series)
	require.True(t, flightIs(&g, "k", func(f *flight[int]) bool { return len(f.callers) == 2 }))
	// one whose other read reported series 3 and 4 can't afford the read's two more, though no read alone exceeds it
	refused := newSelectCharge(&Budget{MaxSeries: 3}, true)
	require.NoError(t, refused.pay(2, 0, []uint64{3, 4}))
	_, err := g.do(ctx, "k", refused, read, nil)
	require.ErrorIs(t, err, ErrFetchLimit)
	series, _ = refused.budget.Fetched()
	require.Equal(t, int64(4), series)
	require.True(t, flightIs(&g, "k", func(f *flight[int]) bool { return len(f.callers) == 2 }), "the refused caller left")
	close(release)
	wg.Wait()
	require.NoError(t, errFirst)
	require.NoError(t, errJoined)
	g.running.Wait()
}

// flightIs reports whether g's flight of key meets cond, which reads it locked
func flightIs[T any](g *flightGroup[T], key string, cond func(f *flight[T]) bool) bool {
	g.mu.Lock()
	f := g.flights[key]
	g.mu.Unlock()
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return cond(f)
}

// gatedCharge returns the charge of a select of budget, whose first payment, a late caller's for what a shared read
// held before it joined, closes entered and then waits until resume is called
func gatedCharge(t *testing.T, budget *Budget, identified bool) (charge *selectCharge, entered chan struct{},
	resume func(),
) {
	t.Helper()
	entered, gate := make(chan struct{}), make(chan struct{})
	resume = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(resume)
	var paid atomic.Bool
	// an evaluation's budget has no share; setting one holds the select's first payment as it charges the budget
	budget.share = func(int64, int64, []uint64) error {
		if paid.CompareAndSwap(false, true) {
			close(entered)
			<-gate
		}
		return nil
	}
	return newSelectCharge(budget, identified), entered, resume
}

func TestFlightGroupLateCallersPayOutsideItsLocks(t *testing.T) {
	const held, heldSamples, added = 4096, 10, 1
	ids := make([]uint64, held)
	for i := range ids {
		ids[i] = uint64(i)
	}
	tests := map[string]struct {
		budget     *Budget
		identified bool
		// grows is whether the read reports another series while the late caller pays for those it held before
		grows bool
		// refused is whether the late caller's payment for what the read held fails, released whether the read
		// releases it before it has paid
		refused, released bool
	}{
		"affordable":                  {budget: &Budget{MaxSeries: held + added}, identified: true, grows: true},
		"refused by its payment":      {budget: &Budget{MaxSeries: held - 1}, identified: true, refused: true},
		"released before it has paid": {budget: &Budget{MaxSeries: held}, grows: true, released: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var g flightGroup[int]
			ctx := context.Background()
			charged, release := make(chan struct{}), make(chan struct{})
			releaseRead := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseRead)
			read := func(_ context.Context, share shareFunc) int {
				if share(held, heldSamples, ids) != nil {
					return -1
				}
				close(charged)
				<-release
				if tc.grows && share(added, heldSamples, []uint64{held}) != nil {
					return -1
				}
				return 1
			}
			var first, late sync.WaitGroup
			var result, lateResult int
			var errFirst, errLate error
			first.Go(func() { result, errFirst = g.do(ctx, "k", &selectCharge{}, read, nil) })
			<-charged
			charge, entered, resume := gatedCharge(t, tc.budget, tc.identified)
			late.Go(func() { lateResult, errLate = g.do(ctx, "k", charge, read, nil) })
			waitWithin(t, func() { <-entered })
			// while the late caller pays for the many series the read held, another key's read runs, and the late caller
			// is already among the read's callers
			var other, callers int
			waitWithin(t, func() { other, _ = g.do(ctx, "other", &selectCharge{}, answerWith(2, nil), nil) })
			require.Equal(t, 2, other)
			waitWithin(t, func() { flightIs(&g, "k", func(f *flight[int]) bool { callers = len(f.callers); return true }) })
			require.Equal(t, 2, callers)
			if !tc.refused {
				// the read goes on charging and answering its callers before the late caller has paid
				releaseRead()
				waitWithin(t, first.Wait)
				require.NoError(t, errFirst)
				require.Equal(t, 1, result)
			}
			resume()
			waitWithin(t, late.Wait)
			if tc.refused || tc.released {
				require.ErrorIs(t, errLate, ErrFetchLimit)
				require.Zero(t, lateResult)
			} else {
				require.NoError(t, errLate)
				require.Equal(t, 1, lateResult)
				series, samples := tc.budget.Fetched()
				require.Equal(t, [2]int64{held + added, 2 * heldSamples}, [2]int64{series, samples})
			}
			if tc.refused {
				// a caller its payment refused leaves the read, which goes on for the others
				require.True(t, flightIs(&g, "k", func(f *flight[int]) bool {
					return len(f.callers) == 1 && f.callers[0].charge != charge && !f.ended
				}))
				require.Len(t, charge.seen, held, "the set grew only one past the limit")
				releaseRead()
				waitWithin(t, first.Wait)
				require.NoError(t, errFirst)
				require.Equal(t, 1, result)
			}
			waitWithin(t, g.running.Wait)
		})
	}
}

func TestSelectChargeChargesEachSeriesOnce(t *testing.T) {
	budget := &Budget{MaxSeries: 5}
	charge := &selectCharge{budget: budget}
	// a select's reads cover disjoint ranges of the same series, so the most any one has held is charged
	require.NoError(t, charge.pay(3, 10, nil))
	require.NoError(t, charge.pay(2, 10, nil))
	require.NoError(t, charge.pay(3, 0, nil))
	series, samples := budget.Fetched()
	require.Equal(t, [2]int64{3, 20}, [2]int64{series, samples})
	require.NoError(t, charge.pay(4, 5, nil))
	series, _ = budget.Fetched()
	require.Equal(t, int64(4), series)
	// what the select serves, its reads' series and those only cached, settles the charge exactly
	require.NoError(t, charge.settle(5))
	series, samples = budget.Fetched()
	require.Equal(t, [2]int64{5, 25}, [2]int64{series, samples})
	require.ErrorIs(t, (&selectCharge{budget: budget}).pay(1, 0, nil), ErrFetchLimit, "every select charges the same budget")
	// and takes back what its reads charged for copies of a series, which it serves as one
	budget = &Budget{}
	charge = &selectCharge{budget: budget}
	require.NoError(t, charge.pay(3, 0, nil))
	require.NoError(t, charge.settle(2))
	series, _ = budget.Fetched()
	require.Equal(t, int64(2), series)

	// reads raising the charge at once still charge the most any one held
	const reads, perRead = 8, 500
	budget = &Budget{MaxSeries: perRead}
	charge = &selectCharge{budget: budget}
	var wg sync.WaitGroup
	for range reads {
		wg.Go(func() {
			for n := int64(1); n <= perRead; n++ {
				require.NoError(t, charge.pay(n, 1, nil))
			}
		})
	}
	wg.Wait()
	series, samples = budget.Fetched()
	require.Equal(t, [2]int64{perRead, reads * perRead}, [2]int64{series, samples})
	require.NoError(t, charge.settle(perRead))
	series, _ = budget.Fetched()
	require.Equal(t, int64(perRead), series)
}

func TestSelectChargePrepaysWhatTheCacheServes(t *testing.T) {
	budget := &Budget{MaxSeries: 5, MaxSamples: 100}
	charge := newSelectCharge(budget, false)
	require.NoError(t, charge.prepay(3, 40))
	// a read charges all its samples, as they lie outside what's cached, but only series past those cached, as they may be the same
	require.NoError(t, charge.pay(2, 10, nil))
	series, samples := budget.Fetched()
	require.Equal(t, [2]int64{3, 50}, [2]int64{series, samples})
	require.NoError(t, charge.pay(4, 10, nil))
	series, samples = budget.Fetched()
	require.Equal(t, [2]int64{4, 60}, [2]int64{series, samples})
	require.NoError(t, charge.settle(5))
	series, _ = budget.Fetched()
	require.Equal(t, int64(5), series)
	// with ids, a read's distinct series are charged once they outnumber those cached
	budget = &Budget{MaxSeries: 5}
	charge = newSelectCharge(budget, true)
	require.NoError(t, charge.prepay(3, 0))
	require.NoError(t, charge.pay(0, 0, []uint64{1, 2}))
	require.NoError(t, charge.pay(0, 0, []uint64{2, 3}))
	series, _ = budget.Fetched()
	require.Equal(t, int64(3), series)
	require.NoError(t, charge.pay(0, 0, []uint64{4}))
	series, _ = budget.Fetched()
	require.Equal(t, int64(4), series)
	// what the cache serves alone can exceed the allowance, before anything is read
	require.ErrorIs(t, newSelectCharge(&Budget{MaxSamples: 10}, false).prepay(0, 11), ErrFetchLimit)
	require.ErrorIs(t, newSelectCharge(&Budget{MaxSeries: 2}, false).prepay(3, 0), ErrFetchLimit)
	require.NoError(t, newSelectCharge(nil, false).prepay(3, 11))
}

func TestSelectChargeCountsIDsOnce(t *testing.T) {
	// a select counts ids only when its reads identify series and its budget limits them
	require.False(t, newSelectCharge(&Budget{MaxSeries: 5}, false).identify)
	require.False(t, newSelectCharge(&Budget{}, true).identify)
	require.False(t, newSelectCharge(nil, true).identify)
	unidentified := newSelectCharge(&Budget{MaxSeries: 5}, false)
	require.NoError(t, unidentified.pay(2, 0, []uint64{1, 2}))
	require.Nil(t, unidentified.seen, "a select not counting ids keeps none")
	budget := &Budget{MaxSeries: 5}
	charge := newSelectCharge(budget, true)
	// reads of disjoint ranges holding different series are charged for all of them, those they share once
	require.NoError(t, charge.pay(2, 10, []uint64{1, 2}))
	require.NoError(t, charge.pay(2, 10, []uint64{2, 3}))
	require.NoError(t, charge.pay(1, 0, []uint64{1}))
	series, samples := budget.Fetched()
	require.Equal(t, [2]int64{3, 20}, [2]int64{series, samples})
	// a read reporting series without ids still raises the charge to all those it held
	require.NoError(t, charge.pay(4, 0, nil))
	series, _ = budget.Fetched()
	require.Equal(t, int64(4), series)
	// the set grows only one past the limit, which then fails every pay
	require.ErrorIs(t, charge.pay(4, 0, []uint64{4, 5, 6, 7, 8, 9}), ErrFetchLimit)
	require.Len(t, charge.seen, 6)
	require.ErrorIs(t, charge.pay(1, 0, []uint64{10}), ErrFetchLimit)
	require.Len(t, charge.seen, 6)

	// reads reporting ids at once charge each distinct one once
	const reads, perRead = 8, 500
	budget = &Budget{MaxSeries: reads * perRead}
	charge = newSelectCharge(budget, true)
	var wg sync.WaitGroup
	for r := range reads {
		wg.Go(func() {
			for n := range perRead {
				// each read's ids overlap the next read's by half
				id := uint64(r*perRead/2 + n)
				require.NoError(t, charge.pay(int64(n+1), 1, []uint64{id}))
			}
		})
	}
	wg.Wait()
	series, samples = budget.Fetched()
	require.Equal(t, [2]int64{(reads + 1) * perRead / 2, reads * perRead}, [2]int64{series, samples})
}

func TestSelectChargeSizesItsSetForItsFirstIDs(t *testing.T) {
	const many = 1 << 14
	ids := make([]uint64, many)
	for i := range ids {
		ids[i] = uint64(i)
	}
	// bytes rather than allocation counts, which other tests' goroutines can tip by one
	allocated := func(f func()) uint64 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		f()
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	payAll := func(limit int64) uint64 {
		return allocated(func() { _ = newSelectCharge(&Budget{MaxSeries: limit}, true).pay(0, 0, ids) })
	}
	grown := allocated(func() {
		seen := make(map[uint64]struct{})
		for _, id := range ids {
			seen[id] = struct{}{}
		}
	})
	sized := payAll(many)
	// a select's first ids, as many as a late caller pays for what a shared read held, size its set at once
	require.Less(t, sized, grown)
	// though never past one more than the limit, where the set stops growing
	require.Less(t, payAll(many/1024), sized)
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

// podsServer streams pods series of the test metric over remote read, a frame each with a pause after, every series
// sampled each minute of the read's range; it counts the frames it wrote, and the reads it served once they end
type podsServer struct {
	pods  int
	pause time.Duration
	// hold, when set, holds each read after holdAfter frames until it closes
	hold      chan struct{}
	holdAfter int

	written, ended atomic.Int64
}

// source returns a member, identified by id, read from a server of its own
func (p *podsServer) source(t *testing.T, id string) *RemoteRead {
	t.Helper()
	rr := newTestRemoteRead(t, func(w http.ResponseWriter, r *http.Request) {
		defer p.ended.Add(1)
		q := decodeTestRequest(t, r).Queries[0]
		w.Header().Set(headers.NameContentType, contentTypeStreamed+"; proto=prometheus.ChunkedReadResponse")
		chunks := minuteChunks(t, q.StartTimestampMs, q.EndTimestampMs)
		for i := range p.pods {
			if p.hold != nil && i == p.holdAfter {
				select {
				case <-p.hold:
				case <-r.Context().Done():
					return
				}
			}
			var body bytes.Buffer
			writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels(fmt.Sprintf("%03d", i)), Chunks: chunks})
			if _, err := w.Write(body.Bytes()); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			p.written.Add(1)
			select {
			case <-time.After(p.pause):
			case <-r.Context().Done():
				return
			}
		}
	})
	rr.ID = id
	return rr
}

// lifetimeServer streams over remote read each of its pods living within a read's range, a frame each, sampled every
// minute it lives there; with hold set, a read then stays open until hold closes or the read is canceled, as canceled counts
type lifetimeServer struct {
	// pods holds each pod's first and last sample times
	pods     map[string][2]int64
	hold     chan struct{}
	canceled atomic.Int64
}

// source returns a member read from a server of its own, with the test source's identity
func (l *lifetimeServer) source(t *testing.T) *RemoteRead {
	t.Helper()
	rr := newTestRemoteRead(t, func(w http.ResponseWriter, r *http.Request) {
		q := decodeTestRequest(t, r).Queries[0]
		w.Header().Set(headers.NameContentType, limitTestStreamed)
		for _, pod := range slices.Sorted(maps.Keys(l.pods)) {
			lo, hi := max(q.StartTimestampMs, l.pods[pod][0]), min(q.EndTimestampMs, l.pods[pod][1])
			if minuteSamples(lo, hi) <= 0 {
				continue
			}
			var body bytes.Buffer
			writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels(pod), Chunks: minuteChunks(t, lo, hi)})
			if _, err := w.Write(body.Bytes()); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
		if l.hold == nil {
			return
		}
		select {
		case <-l.hold:
		case <-r.Context().Done():
			l.canceled.Add(1)
		}
	})
	rr.ID = rawTestIdentity
	return rr
}

// minuteSamples is how many whole minutes lie from mint to maxt, each a sample of every series a podsServer streams
func minuteSamples(mint, maxt int64) int64 {
	return maxt/rawTestMinute - (mint+rawTestMinute-1)/rawTestMinute + 1
}

func minuteChunks(t *testing.T, mint, maxt int64) []prompb.Chunk {
	t.Helper()
	var points []testPoint
	for ts := (mint + rawTestMinute - 1) / rawTestMinute * rawTestMinute; ts <= maxt; ts += rawTestMinute {
		points = append(points, testPoint{t: ts, f: float64(ts)})
	}
	var chunks []prompb.Chunk
	for from := 0; from < len(points); from += rawTestChunk {
		chunks = append(chunks, xorChunk(t, points[from:min(from+rawTestChunk, len(points))]...))
	}
	return chunks
}

func TestRawCacheChargesMembersSeriesAsTheyArrive(t *testing.T) {
	const members, pods, maxt = 3, 20, 2 * rawTestHour
	// every member holds the same series, which deduplication merges, yet each member's are charged
	const all = members * pods
	server := &podsServer{pods: pods, pause: budgetTestPause}
	sources := make([]Member, members)
	for i := range sources {
		name := "member-" + strconv.Itoa(i)
		sources[i] = Member{Name: name, Source: server.source(t, name)}
	}
	evaluate := func(t *testing.T, rc *rawTestCache, budget *Budget) (int, error) {
		t.Helper()
		qr, err := NewQueryable(sources, nil, merge.TSMDeduplicationChain, false, nil).WithRawCache(rc.RawCache).
			Querier(0, maxt)
		require.NoError(t, err)
		set := qr.Select(WithBudget(teamRequest(rawTestTeamA), budget), true, &storage.SelectHints{Start: 0, End: maxt},
			testMetricMatcher)
		var n int
		for set.Next() {
			n++
		}
		rc.Wait()
		return n, set.Err()
	}
	// settled waits until the servers have ended reads, so what they wrote is final, and returns it
	settled := func(t *testing.T, reads int64) int64 {
		t.Helper()
		require.Eventually(t, func() bool { return server.ended.Load() == reads }, 5*time.Second, time.Millisecond)
		written := server.written.Load()
		server.written.Store(0)
		server.ended.Store(0)
		return written
	}
	// each read is limited as the evaluation is, so every member alone is well within it
	newCache := func(limit int64) *rawTestCache {
		return newRawTestCache(&rawByteCache{}, RawCacheOptions{MaxSeries: limit})
	}
	t.Run("collectively over the limit, every member's read stops", func(t *testing.T) {
		const limit = all/2 + 1
		rc := newCache(limit)
		rc.setNow(maxt + rawTestHour)
		budget := &Budget{MaxSeries: limit}
		_, err := evaluate(t, rc, budget)
		require.ErrorIs(t, err, ErrFetchLimit)
		written := settled(t, members)
		require.Less(t, written, int64(all), "the members' reads stopped before sending every series")
		series, _ := budget.Fetched()
		require.LessOrEqual(t, series, int64(limit+members), "each read stopped within a frame of the limit")
	})
	t.Run("collectively within the limit, each series is charged once", func(t *testing.T) {
		rc := newCache(all)
		rc.setNow(maxt + rawTestHour)
		budget := &Budget{MaxSeries: all}
		n, err := evaluate(t, rc, budget)
		require.NoError(t, err)
		require.Equal(t, pods, n)
		require.Equal(t, int64(all), settled(t, members))
		series, samples := budget.Fetched()
		require.Equal(t, [2]int64{all, all * minuteSamples(0, maxt)}, [2]int64{series, samples})
		// now the volatile window's samples are read again, the rest served from the cache, and still charged once
		rc.setNow(maxt)
		budget = &Budget{MaxSeries: all}
		_, err = evaluate(t, rc, budget)
		require.NoError(t, err)
		require.Equal(t, int64(all), settled(t, members))
		series, samples = budget.Fetched()
		require.Equal(t, [2]int64{all, all * minuteSamples(0, maxt)}, [2]int64{series, samples})
		require.Contains(t, rc.takeStatuses(), cachestatus.LookupStatusHit)
		// with the cache warm, what it serves is charged before the members are read, so once the members' cached series
		// together exceed the limit, a member whose series would pass it reads nothing, and the evaluation fails
		const limit = all*3/4 + 1
		budget = &Budget{MaxSeries: limit}
		_, err = evaluate(t, rc, budget)
		require.ErrorIs(t, err, ErrFetchLimit)
		series, _ = budget.Fetched()
		require.LessOrEqual(t, series, int64(limit+pods), "the charge stops within one member's cached series of the limit")
	})
}

func TestRawCacheChargesSeriesOnceAcrossGapsAndFrames(t *testing.T) {
	t.Run("gaps", func(t *testing.T) {
		src := &streamingSource{chunkedSource: rawTestSource(t), pause: time.Millisecond}
		// reads limited in series identify them, as they are whenever the evaluation is, and the select counts their ids;
		// unlimited reads don't, and the select counts the most series one read held
		for name, readLimit := range map[string]int64{"by id": 2, "by count": 0} {
			t.Run(name, func(t *testing.T) {
				// half of each hour is cached, so a read of eight hours has eight gaps, each holding both series
				fill := func() *rawTestCache {
					rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{MaxSeries: readLimit})
					for start := int64(0); start < 8*rawTestHour; start += rawTestHour {
						_, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), src, start, start+30*rawTestMinute)
						require.NoError(t, err)
					}
					src.takeReads()
					return rc
				}
				rc := fill()
				budget := &Budget{MaxSeries: 2}
				_, _, err := rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), budget), src, 0, 8*rawTestHour)
				require.NoError(t, err, "a series read in every gap is charged once")
				require.Len(t, src.takeReads(), 8)
				series, _ := budget.Fetched()
				require.Equal(t, int64(2), series)
				// a limit the cached series exceed fails the select before any of its gaps is read
				rc = fill()
				_, _, err = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), &Budget{MaxSeries: 1}), src, 0, 8*rawTestHour)
				require.ErrorIs(t, err, ErrFetchLimit)
				require.Empty(t, src.takeReads())
			})
		}
	})
	t.Run("gaps holding different series", func(t *testing.T) {
		// one series stops and another starts within the cached range between the gaps, which holds neither, so the select
		// serves more than either read holds, and the cache serves none of them
		pods := map[string][2]int64{"a": {5 * rawTestHour, rawTestEnd}, "b": {0, 3*rawTestHour - 1}}
		filler := &lifetimeServer{pods: pods}
		fill := func() *rawTestCache {
			rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{MaxSeries: 2})
			cached, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), filler.source(t), 3*rawTestHour, 5*rawTestHour-1)
			require.NoError(t, err)
			require.Empty(t, cached)
			return rc
		}
		budget := &Budget{MaxSeries: 2}
		served, _, err := fill().selectAt(t, WithBudget(teamRequest(rawTestTeamA), budget), filler.source(t), 0, 8*rawTestHour)
		require.NoError(t, err)
		require.Len(t, served, 2)
		series, _ := budget.Fetched()
		require.Equal(t, int64(2), series)
		// with room for one, the second series fails the select as it arrives, while both reads are still under way
		held := &lifetimeServer{pods: pods, hold: make(chan struct{})}
		src := held.source(t)
		// the hold is released before the server closes, which waits for its reads
		t.Cleanup(sync.OnceFunc(func() { close(held.hold) }))
		rc := fill()
		budget = &Budget{MaxSeries: 1}
		waitWithin(t, func() {
			_, _, err = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), budget), src, 0, 8*rawTestHour)
		})
		require.ErrorIs(t, err, ErrFetchLimit)
		series, _ = budget.Fetched()
		require.Equal(t, int64(2), series)
		require.Eventually(t, func() bool { return held.canceled.Load() == 2 }, storeTestWait, time.Millisecond,
			"both reads were stopped before they ended")
	})
	t.Run("frames", func(t *testing.T) {
		var written atomic.Int64
		maxt := int64(budgetTestFrames*rawTestChunk) * rawTestStep
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
		rc.setNow(maxt + rawTestHour)
		// one series sent across every frame
		budget := &Budget{MaxSeries: 1}
		_, _, err := rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), budget), framedRemoteRead(t, &written), 0, maxt)
		require.NoError(t, err)
		series, samples := budget.Fetched()
		require.Equal(t, [2]int64{1, budgetTestFrames * rawTestChunk}, [2]int64{series, samples})
	})
}

func TestRawCacheLateCallersPayWhatTheReadHeld(t *testing.T) {
	const pods, held, maxt = 30, 5, 2 * rawTestHour
	server := &podsServer{pods: pods, hold: make(chan struct{}), holdAfter: held}
	release := sync.OnceFunc(func() { close(server.hold) })
	src := server.source(t, rawTestIdentity)
	t.Cleanup(release)
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
	rc.setNow(maxt + rawTestHour)
	read := func(budget *Budget) ([]storage.Series, error) {
		series, _, err := rc.Select(WithBudget(teamRequest(rawTestTeamA), budget), Member{Name: rawTestMember, Source: src},
			0, maxt, &storage.SelectHints{Start: 0, End: maxt}, []*labels.Matcher{testMetricMatcher})
		return series, err
	}
	perSeries := minuteSamples(0, maxt)
	first := &Budget{}
	var wg sync.WaitGroup
	var served []storage.Series
	var errFirst, errLater error
	wg.Go(func() { served, errFirst = read(first) })
	waitForFlight(t, rc, src, 0, maxt, func(f *flight[fetched]) bool { return f.series == held })
	// a caller joining now pays the series and samples read so far, and is refused at once when it can't
	for _, budget := range []*Budget{{MaxSeries: held - 1}, {MaxSamples: held*perSeries - 1}} {
		var err error
		waitWithin(t, func() { _, err = read(budget) })
		require.ErrorIs(t, err, ErrFetchLimit)
	}
	// one that can afford them joins, then leaves once the read holds more series than it allows
	later := &Budget{MaxSeries: 2 * held}
	wg.Go(func() { _, errLater = read(later) })
	// it pays as it joins, its samples before its series
	require.Eventually(t, func() bool { series, _ := later.Fetched(); return series > 0 }, storeTestWait, time.Millisecond)
	series, samples := later.Fetched()
	require.Equal(t, [2]int64{held, held * perSeries}, [2]int64{series, samples})
	waitForFlight(t, rc, src, 0, maxt, func(f *flight[fetched]) bool { return len(f.callers) == 2 })
	release()
	wg.Wait()
	rc.Wait()
	require.ErrorIs(t, errLater, ErrFetchLimit)
	// it left at the first series past its limit, not once the read ended
	series, _ = later.Fetched()
	require.Equal(t, int64(2*held+1), series)
	require.NoError(t, errFirst)
	require.Len(t, served, pods)
	series, samples = first.Fetched()
	require.Equal(t, [2]int64{pods, pods * perSeries}, [2]int64{series, samples})
	require.Equal(t, int64(1), server.ended.Load(), "every caller shared one read")
}

// copiesSource streams pod a, then pod b, then the rest of pod a in a later frame, a copy of a the read joins to it, as
// two series sampled every minute of the read's range
func copiesSource(t *testing.T, id string) *RemoteRead {
	t.Helper()
	rr := newTestRemoteRead(t, func(w http.ResponseWriter, r *http.Request) {
		q := decodeTestRequest(t, r).Queries[0]
		chunks := minuteChunks(t, q.StartTimestampMs, q.EndTimestampMs)
		var body bytes.Buffer
		writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: chunks[:1]},
			&prompb.ChunkedSeries{Labels: testLabels("b"), Chunks: chunks})
		writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: chunks[1:]})
		w.Header().Set(headers.NameContentType, limitTestStreamed)
		_, _ = w.Write(body.Bytes())
	})
	rr.ID = id
	return rr
}

func TestRawCacheChargesStreamedCopiesOnce(t *testing.T) {
	const maxt = 2 * rawTestHour
	members := make([]Member, 2)
	for i := range members {
		name := "member-" + strconv.Itoa(i)
		members[i] = Member{Name: name, Source: copiesSource(t, name)}
	}
	hints := &storage.SelectHints{Start: 0, End: maxt}
	// selectAll reads each member's series through a cache whose reads are limited as the evaluation is
	selectAll := func(t *testing.T, budget *Budget, members ...Member) error {
		t.Helper()
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{MaxSeries: budget.MaxSeries})
		rc.setNow(maxt + rawTestHour)
		ctx := WithBudget(teamRequest(rawTestTeamA), budget)
		for _, m := range members {
			served, _, err := rc.Select(ctx, m, 0, maxt, hints, []*labels.Matcher{testMetricMatcher})
			if err != nil {
				return err
			}
			require.Len(t, served, 2)
		}
		rc.Wait()
		return nil
	}
	perSeries := minuteSamples(0, maxt)
	// without a limit, a read counts the copy as it arrives, and the select takes it back once it serves two series
	budget := &Budget{}
	require.NoError(t, selectAll(t, budget, members[0]))
	series, samples := budget.Fetched()
	require.Equal(t, [2]int64{2, 2 * perSeries}, [2]int64{series, samples})
	// with one, reads tell the copy apart as it arrives, so members serving as many series as the limit allows pass
	for _, limit := range []int64{2, 4} {
		budget = &Budget{MaxSeries: limit}
		require.NoError(t, selectAll(t, budget, members[:limit/2]...))
		series, _ = budget.Fetched()
		require.Equal(t, limit, series)
	}
	require.ErrorIs(t, selectAll(t, &Budget{MaxSeries: 3}, members...), ErrFetchLimit)
	// a read without the cache counts the copy once too
	budget = &Budget{MaxSeries: 2}
	served, _, err := members[0].Source.Select(WithBudget(teamRequest(rawTestTeamA), budget), 0, maxt, hints,
		testMetricMatcher)
	require.NoError(t, err)
	require.Len(t, served, 3, "the copy is joined to its series later, by deduplication")
	series, _ = budget.Fetched()
	require.Equal(t, int64(2), series)
}

// selectCountingSource counts the reads made of its source
type selectCountingSource struct {
	Source
	selects atomic.Int64
}

func (s *selectCountingSource) Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	s.selects.Add(1)
	return s.Source.Select(ctx, mint, maxt, hints, matchers...)
}

func TestRawCacheChargesWhatItServesBeforeReading(t *testing.T) {
	const pods, maxt = 20, 2 * rawTestHour
	server := &podsServer{pods: pods, pause: budgetTestPause}
	src := &selectCountingSource{Source: server.source(t, rawTestIdentity)}
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
	read := func(budget *Budget) ([]storage.Series, error) {
		t.Helper()
		series, _, err := rc.Select(WithBudget(teamRequest(rawTestTeamA), budget), Member{Name: rawTestMember, Source: src},
			0, maxt, nil, []*labels.Matcher{testMetricMatcher})
		rc.Wait()
		return series, err
	}
	// settled waits until the server has ended every read made, so what it wrote is final, and returns the reads and frames
	settled := func(t *testing.T) (int64, int64) {
		t.Helper()
		require.Eventually(t, func() bool { return server.ended.Load() == src.selects.Load() }, storeTestWait, time.Millisecond)
		reads, written := src.selects.Load(), server.written.Load()
		src.selects.Store(0)
		server.ended.Store(0)
		server.written.Store(0)
		return reads, written
	}
	// the whole range is read and kept, then all but the volatile window's samples are served from the cache
	rc.setNow(maxt + rawTestHour)
	_, err := read(nil)
	require.NoError(t, err)
	settled(t)
	rc.setNow(maxt)
	all := minuteSamples(0, maxt)
	volatile := minuteSamples(maxt-rawTestVolatile.Milliseconds()+1, maxt)
	cached := all - volatile

	t.Run("within the limit, cached and read data are charged exactly once", func(t *testing.T) {
		budget := &Budget{MaxSeries: pods, MaxSamples: pods * all}
		series, err := read(budget)
		require.NoError(t, err)
		require.Len(t, series, pods)
		fetchedSeries, fetchedSamples := budget.Fetched()
		require.Equal(t, [2]int64{pods, pods * all}, [2]int64{fetchedSeries, fetchedSamples})
		reads, written := settled(t)
		require.Equal(t, [2]int64{1, pods}, [2]int64{reads, written})
	})
	t.Run("cached samples leave room for a few frames, so the read stops after them", func(t *testing.T) {
		_, err := read(&Budget{MaxSamples: pods*cached + 3*volatile})
		require.ErrorIs(t, err, ErrFetchLimit)
		reads, written := settled(t)
		require.Equal(t, int64(1), reads)
		require.Less(t, written, int64(pods), "the read stopped before sending every series")
	})
	for name, budget := range map[string]*Budget{
		"cached series alone exceed the limit":  {MaxSeries: pods - 1},
		"cached samples alone exceed the limit": {MaxSamples: pods*cached - 1},
	} {
		t.Run(name+", so nothing is read", func(t *testing.T) {
			_, err := read(budget)
			require.ErrorIs(t, err, ErrFetchLimit)
			reads, written := settled(t)
			require.Equal(t, [2]int64{0, 0}, [2]int64{reads, written})
		})
	}
}
