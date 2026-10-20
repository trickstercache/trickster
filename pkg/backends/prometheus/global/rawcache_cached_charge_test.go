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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/stretchr/testify/require"
)

const (
	// chargeTestBlocks and chargeTestPods are how many blocks a warm cache holds, and the pods living only within each
	chargeTestBlocks = 4
	chargeTestPods   = 10
)

func TestRawCacheChargesReadSeriesBesidesCachedOnes(t *testing.T) {
	const block = rawBlockMs
	// the cache is filled with what the pods hold within cached, then a select with room for one series reads the rest of
	// its range; a read series the cache didn't serve fails it as it arrives, while the read waits to end
	for name, test := range map[string]struct {
		pods   map[string][2]int64
		cached [2]int64
		maxt   int64
		fits   bool
	}{
		"a read holding another series": {
			pods: map[string][2]int64{"a": {0, block - 1}, "b": {block, rawTestEnd}}, cached: [2]int64{0, block - 1},
			maxt: 2*block - 1,
		},
		"reads around the cached range, one holding another series": {
			pods:   map[string][2]int64{"a": {block, 2*block - 1}, "b": {2 * block, rawTestEnd}},
			cached: [2]int64{block, 2*block - 1}, maxt: 3*block - 1,
		},
		"a read holding the cached series": {
			pods: map[string][2]int64{"a": {0, rawTestEnd}}, cached: [2]int64{0, block - 1}, maxt: 2*block - 1, fits: true,
		},
		"reads around the cached range holding the cached series": {
			pods: map[string][2]int64{"a": {0, rawTestEnd}}, cached: [2]int64{block, 2*block - 1}, maxt: 3*block - 1,
			fits: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			// reads are limited as the evaluation is, so they identify their series
			rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{MaxSeries: 1})
			served, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), (&lifetimeServer{pods: test.pods}).source(t),
				test.cached[0], test.cached[1])
			require.NoError(t, err)
			require.Len(t, served, 1)
			server := &lifetimeServer{pods: test.pods}
			if !test.fits {
				server.hold = make(chan struct{})
			}
			src := server.source(t)
			if server.hold != nil {
				// the hold is released before the server closes, which waits for its reads
				t.Cleanup(sync.OnceFunc(func() { close(server.hold) }))
			}
			budget := &Budget{MaxSeries: 1}
			waitWithin(t, func() {
				served, _, err = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), budget), src, 0, test.maxt)
			})
			series, _ := budget.Fetched()
			if test.fits {
				require.NoError(t, err)
				require.Len(t, served, 1)
				require.Equal(t, int64(1), series)
				return
			}
			require.ErrorIs(t, err, ErrFetchLimit)
			require.Equal(t, int64(2), series)
			// a read the select stopped before it reached the member is never seen, so every one that was is
			// required to have ended, which, as the hold is never released, it does only once it is stopped
			require.Eventually(t, func() bool {
				started := server.started.Load()
				return started > 0 && server.ended.Load() == started
			}, storeTestWait, time.Millisecond, "every read that reached the member was stopped before it ended")
		})
	}
}

func TestRawCacheSharedReadChargesEachCallersCachedSeries(t *testing.T) {
	const block = rawBlockMs
	// a is cached before block; the read of the range from block holds b, then waits to end
	pods := map[string][2]int64{"a": {0, block - 1}, "b": {block, rawTestEnd}}
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{MaxSeries: 2})
	_, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), (&lifetimeServer{pods: pods}).source(t), 0, block-1)
	require.NoError(t, err)
	server := &lifetimeServer{pods: pods, hold: make(chan struct{})}
	src := server.source(t)
	release := sync.OnceFunc(func() { close(server.hold) })
	t.Cleanup(release)
	read := func(budget *Budget, mint int64) ([]storage.Series, error) {
		series, _, err := rc.Select(WithBudget(teamRequest(rawTestTeamA), budget), Member{Name: rawTestMember, Source: src},
			mint, 2*block-1, nil, []*labels.Matcher{testMetricMatcher})
		return series, err
	}
	// a select of only the range from block starts the read, which charges it b alone
	var wg sync.WaitGroup
	var fromBlock, whole []storage.Series
	var errFromBlock, errWhole error
	fromBlockBudget := &Budget{MaxSeries: 1}
	wg.Go(func() { fromBlock, errFromBlock = read(fromBlockBudget, block) })
	waitForFlight(t, rc, src, block, 2*block-1, func(f *flight[fetched]) bool { return f.series == 1 })
	// a select of the whole range shares the read, besides a; with room for one series, b is refused as it joins
	refused := &Budget{MaxSeries: 1}
	waitWithin(t, func() { _, err = read(refused, 0) })
	require.ErrorIs(t, err, ErrFetchLimit)
	series, _ := refused.Fetched()
	require.Equal(t, int64(2), series)
	// with room for both, it joins, and is served both once the read ends
	wholeBudget := &Budget{MaxSeries: 2}
	wg.Go(func() { whole, errWhole = read(wholeBudget, 0) })
	waitForCallers(t, rc, block, 2*block-1, 2)
	release()
	wg.Wait()
	rc.Wait()
	require.NoError(t, errFromBlock)
	require.Len(t, fromBlock, 1)
	series, _ = fromBlockBudget.Fetched()
	require.Equal(t, int64(1), series)
	require.NoError(t, errWhole)
	require.Len(t, whole, 2)
	series, _ = wholeBudget.Fetched()
	require.Equal(t, int64(2), series)
}

func TestRawCacheStopsCachedWorkAtTheBudget(t *testing.T) {
	const block = rawBlockMs
	const maxt = chargeTestBlocks*block - 1
	// each block holds pods living only within it
	pods := make(map[string][2]int64, chargeTestBlocks*chargeTestPods)
	for b := range int64(chargeTestBlocks) {
		for i := range chargeTestPods {
			pods[fmt.Sprintf("%d-%02d", b, i)] = [2]int64{b * block, (b+1)*block - 1}
		}
	}
	perBlock := chargeTestPods * minuteSamples(0, block-1)
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			c, counted := counting(newCache())
			rc := newRawTestCache(c, RawCacheOptions{})
			server := &lifetimeServer{pods: pods}
			src := server.source(t)
			_, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), src, 0, maxt)
			require.NoError(t, err)
			// selectWithin selects every block, warm, returning the series, error and blocks retrieved
			selectWithin := func(ctx context.Context, budget *Budget) ([]storage.Series, error, int) {
				counted.takeAll()
				served, _, err := rc.selectAt(t, WithBudget(ctx, budget), src, 0, maxt)
				return served, err, counted.takeAll()
			}

			all := &Budget{MaxSeries: chargeTestBlocks * chargeTestPods, MaxSamples: chargeTestBlocks * perBlock}
			served, err, retrieved := selectWithin(teamRequest(rawTestTeamA), all)
			require.NoError(t, err)
			require.Len(t, served, chargeTestBlocks*chargeTestPods)
			require.Equal(t, chargeTestBlocks, retrieved)
			series, samples := all.Fetched()
			require.Equal(t, [2]int64{chargeTestBlocks * chargeTestPods, chargeTestBlocks * perBlock},
				[2]int64{series, samples}, "a select within the budget is charged all it serves, once")
			require.Equal(t, []status.LookupStatus{status.LookupStatusKeyMiss, status.LookupStatusHit}, rc.takeStatuses())

			// a budget that runs out within the third block stops the select there: no later block is retrieved, and none
			// of that block's series is charged, as the select failed before keeping them
			for limit, budget := range map[string]*Budget{
				"series":  {MaxSeries: 2*chargeTestPods + chargeTestPods/2},
				"samples": {MaxSamples: 2*perBlock + perBlock/2},
			} {
				_, err, retrieved = selectWithin(teamRequest(rawTestTeamA), budget)
				require.ErrorIs(t, err, ErrFetchLimit, limit)
				require.Equal(t, 3, retrieved, limit)
				series, samples = budget.Fetched()
				require.Equal(t, [2]int64{2 * chargeTestPods, 2 * perBlock}, [2]int64{series, samples}, limit)
			}

			// a select whose context ends while it reads the cache retrieves no more blocks
			ctx, cancel := context.WithCancel(teamRequest(rawTestTeamA))
			counted.setEach(cancel)
			_, err, retrieved = selectWithin(ctx, nil)
			counted.setEach(nil)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, 1, retrieved)
			require.Empty(t, rc.takeStatuses(), "a select that stops reading the cache reads no member")

			// selects that stopped for their budget or context left every block cached
			served, err, retrieved = selectWithin(teamRequest(rawTestTeamA), nil)
			require.NoError(t, err)
			require.Len(t, served, chargeTestBlocks*chargeTestPods)
			require.Equal(t, chargeTestBlocks, retrieved)
			require.Equal(t, []status.LookupStatus{status.LookupStatusHit}, rc.takeStatuses())
		})
	}
}
