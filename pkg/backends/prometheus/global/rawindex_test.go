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
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/stretchr/testify/require"
)

const (
	rawIndexScope = "scope"
	rawIndexOther = "other"
)

var (
	podA      = labels.MustNewMatcher(labels.MatchEqual, testPod, "a")
	podB      = labels.MustNewMatcher(labels.MatchEqual, testPod, "b")
	podAorB   = labels.MustNewMatcher(labels.MatchRegexp, testPod, "a|b")
	noReplica = labels.MustNewMatcher(labels.MatchEqual, testReplicaLabel, "")
)

// indexKey returns the key a selector of matchers has in scope, as RawCache.key orders and names it
func indexKey(scope string, matchers ...*labels.Matcher) rawKey {
	k := rawKey{scope: scope, matchers: slices.SortedFunc(slices.Values(matchers), compareMatchers)}
	for _, m := range k.matchers {
		k.key += m.String()
		if m.Name == labels.MetricName && m.Type == labels.MatchEqual {
			k.metric = m.Value
		}
	}
	return k
}

func TestSubsumes(t *testing.T) {
	sorted := func(ms ...*labels.Matcher) []*labels.Matcher {
		return slices.SortedFunc(slices.Values(ms), compareMatchers)
	}
	for name, tc := range map[string]struct {
		broad, narrow []*labels.Matcher
		extra         []*labels.Matcher
		ok            bool
	}{
		"metric only":          {sorted(testMetricMatcher), sorted(testMetricMatcher, podA), []*labels.Matcher{podA}, true},
		"two extra":            {sorted(testMetricMatcher), sorted(testMetricMatcher, podA, noReplica), sorted(podA, noReplica), true},
		"one shared":           {sorted(testMetricMatcher, podA), sorted(testMetricMatcher, podA, noReplica), []*labels.Matcher{noReplica}, true},
		"equal":                {sorted(testMetricMatcher, podA), sorted(testMetricMatcher, podA), nil, false},
		"broader":              {sorted(testMetricMatcher, podA), sorted(testMetricMatcher), nil, false},
		"another value":        {sorted(testMetricMatcher, podB), sorted(testMetricMatcher, podA, noReplica), nil, false},
		"regexes not reasoned": {sorted(testMetricMatcher, podAorB), sorted(testMetricMatcher, podA, noReplica), nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			extra, ok := subsumes(tc.broad, tc.narrow)
			require.Equal(t, tc.ok, ok)
			if ok {
				require.Equal(t, tc.extra, extra)
			}
		})
	}
}

func TestRawIndex(t *testing.T) {
	now := time.UnixMilli(rawTestEnd)
	broad, mid := indexKey(rawIndexScope, testMetricMatcher), indexKey(rawIndexScope, testMetricMatcher, podA)
	narrow := indexKey(rawIndexScope, testMetricMatcher, podA, noReplica)
	keys := func(cs []rawCandidate) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.key)
		}
		return out
	}
	t.Run("narrowest strict subsets of the same scope and metric", func(t *testing.T) {
		var x rawIndex
		for _, k := range []rawKey{
			broad, mid, narrow, indexKey(rawIndexOther, testMetricMatcher),
			indexKey(rawIndexScope, labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, "other")),
			indexKey(rawIndexScope, testMetricMatcher, podB),
		} {
			x.record(k, now, time.Hour)
		}
		require.Equal(t, []string{mid.key, broad.key}, keys(x.candidates(narrow, now)))
		require.Equal(t, []string{broad.key}, keys(x.candidates(mid, now)))
		require.Empty(t, x.candidates(broad, now))
	})
	t.Run("selectors without a metric name", func(t *testing.T) {
		var x rawIndex
		nameless := indexKey(rawIndexScope, podA)
		x.record(nameless, now, time.Hour)
		require.Empty(t, x.groups)
		require.Empty(t, x.candidates(indexKey(rawIndexScope, podA, noReplica), now))
	})
	t.Run("expiry and renewal", func(t *testing.T) {
		var x rawIndex
		x.record(broad, now, time.Minute)
		require.Len(t, x.candidates(mid, now.Add(time.Minute)), 1)
		x.record(broad, now.Add(time.Minute), time.Minute)
		require.Len(t, x.candidates(mid, now.Add(2*time.Minute)), 1, "a use renews it")
		require.Empty(t, x.candidates(mid, now.Add(3*time.Minute)))
		require.Empty(t, x.groups, "an expired selector is forgotten")
	})
	t.Run("bounds", func(t *testing.T) {
		var x rawIndex
		x.record(broad, now, time.Hour)
		for i := range rawIndexGroupLimit {
			x.record(indexKey(rawIndexScope, testMetricMatcher,
				labels.MustNewMatcher(labels.MatchEqual, testPod, strconv.Itoa(i))), now.Add(time.Minute), time.Hour)
		}
		require.Len(t, x.groups[broad.group()], rawIndexGroupLimit)
		require.Empty(t, x.candidates(mid, now), "the group's oldest selector made way")
		var y rawIndex
		for i := range rawIndexLimit + 1 {
			y.record(indexKey(strconv.Itoa(i), testMetricMatcher), now, time.Hour)
		}
		require.Equal(t, rawIndexLimit, y.lru.Len())
		require.NotContains(t, y.groups, indexKey("0", testMetricMatcher).group(), "the least recently used made way")
	})
	t.Run("tries", func(t *testing.T) {
		var x rawIndex
		matchers := []*labels.Matcher{testMetricMatcher}
		for i := range rawSubsumeTries + 2 {
			matchers = append(matchers, labels.MustNewMatcher(labels.MatchEqual, "l"+strconv.Itoa(i), "v"))
			x.record(indexKey(rawIndexScope, slices.Clone(matchers)...), now, time.Hour)
		}
		matchers = append(matchers, podA)
		got := x.candidates(indexKey(rawIndexScope, matchers...), now)
		require.Len(t, got, rawSubsumeTries)
		require.Len(t, got[0].extra, 1, "the narrowest first")
	})
	t.Run("concurrent use", func(t *testing.T) {
		var x rawIndex
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				for j := range 200 {
					x.record(indexKey(strconv.Itoa(j%16), testMetricMatcher), now, time.Hour)
					x.candidates(indexKey(strconv.Itoa(i), testMetricMatcher, podA), now)
				}
			})
		}
		wg.Wait()
		require.Equal(t, 16, x.lru.Len())
	})
}

// selectMatching reads src's series matching matchers from mint to maxt for team A
func (rc *rawTestCache) selectMatching(t *testing.T, ctx context.Context, src Source, mint, maxt int64,
	matchers ...*labels.Matcher,
) []storage.Series {
	t.Helper()
	series, _, err := rc.Select(ctx, Member{Name: rawTestMember, Source: src}, mint, maxt,
		&storage.SelectHints{Start: mint, End: maxt}, matchers)
	rc.Wait()
	require.NoError(t, err)
	return series
}

// wantMatching returns src's samples of the series matching matchers from mint to maxt
func wantMatching(t *testing.T, src *chunkedSource, mint, maxt int64, matchers ...*labels.Matcher) map[string][]testPoint {
	t.Helper()
	var all []storage.Series
	for _, s := range src.series {
		if matchesAll(s.lset, matchers) {
			all = append(all, s)
		}
	}
	return pointsOf(t, bound(all, mint, maxt))
}

func TestRawCacheAnswersNarrowerSelectors(t *testing.T) {
	const mint, maxt = 4 * rawTestHour, 9 * rawTestHour
	ctx := teamRequest(rawTestTeamA)
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			c := newCache()
			src := rawTestSource(t)
			var subsumed int
			rc := newRawTestCache(c, RawCacheOptions{Subsumed: func() { subsumed++ }})
			read := func(lo, hi int64, matchers ...*labels.Matcher) [][2]int64 {
				t.Helper()
				require.Equal(t, wantMatching(t, src, lo, hi, matchers...),
					pointsOf(t, rc.selectMatching(t, ctx, src, lo, hi, matchers...)))
				return src.takeReads()
			}
			require.Len(t, read(mint, maxt, testMetricMatcher), 1)
			rc.takeStatuses()

			// a narrower selector within the broader one's blocks reads nothing from the member
			require.Empty(t, read(5*rawTestHour, 8*rawTestHour, testMetricMatcher, podA))
			require.Equal(t, 1, subsumed)
			require.Equal(t, []status.LookupStatus{status.LookupStatusHit}, rc.takeStatuses())
			// it keeps nothing of its own, so it is answered the same way again
			require.Empty(t, read(5*rawTestHour, 8*rawTestHour, testMetricMatcher, podA))
			require.Equal(t, 2, subsumed)

			// reaching past them reads only the rest, under its own key, and then has blocks of its own too
			require.Equal(t, [][2]int64{{3 * rawTestHour, mint - 1}},
				read(3*rawTestHour, 8*rawTestHour, testMetricMatcher, podA))
			require.Empty(t, read(3*rawTestHour, 8*rawTestHour, testMetricMatcher, podA))

			// a selector whose matchers aren't a superset reads the member
			require.Len(t, read(5*rawTestHour, 8*rawTestHour, testMetricMatcher, podAorB), 0,
				"a regex adds a matcher, so the metric alone still subsumes it")
			require.Len(t, read(5*rawTestHour, 8*rawTestHour, labels.MustNewMatcher(labels.MatchRegexp,
				labels.MetricName, testMetric), podB), 1, "no equality metric name, no index")
			// nor does another tenant's selector read team A's blocks
			_, _, err := rc.Select(teamRequest(rawTestTeamB), Member{Name: rawTestMember, Source: src},
				5*rawTestHour, 8*rawTestHour, nil, []*labels.Matcher{testMetricMatcher, podB})
			require.NoError(t, err)
			rc.Wait()
			require.Len(t, src.takeReads(), 1)

			// once the broader blocks are gone, the narrower selector reads the member again
			k, _ := rc.key(ctx, src, []*labels.Matcher{testMetricMatcher})
			for b := blockIndex(mint); b <= blockIndex(maxt); b++ {
				require.NoError(t, c.Remove(blockKey(k.key, b)))
			}
			require.Len(t, read(5*rawTestHour, 8*rawTestHour, testMetricMatcher, podB), 1)
		})
	}
}

func TestRawCacheCountsOnlyNarrowerSeries(t *testing.T) {
	const mint, maxt = 4 * rawTestHour, 9 * rawTestHour
	src := rawTestSource(t)
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
	rc.selectMatching(t, teamRequest(rawTestTeamA), src, mint, maxt, testMetricMatcher)
	src.takeReads()
	// the narrower selector is charged for its own series' samples in range, as an exact cold read is
	want := wantMatching(t, src, 5*rawTestHour, 8*rawTestHour, testMetricMatcher, podA)
	require.Len(t, want, 1)
	var samples int64
	for _, points := range want {
		samples += int64(len(points))
	}
	budget := &Budget{MaxSeries: 1, MaxSamples: samples}
	rc.selectMatching(t, WithBudget(teamRequest(rawTestTeamA), budget), src, 5*rawTestHour, 8*rawTestHour,
		testMetricMatcher, podA)
	require.Empty(t, src.takeReads())
	gotSeries, gotSamples := budget.Fetched()
	require.Equal(t, int64(1), gotSeries)
	require.Equal(t, samples, gotSamples)
}

func TestRawCacheIndexRefillsOnReads(t *testing.T) {
	const mint, maxt = 4 * rawTestHour, 9 * rawTestHour
	ctx := teamRequest(rawTestTeamA)
	c := &rawByteCache{}
	src := rawTestSource(t)
	newRawTestCache(c, RawCacheOptions{}).selectMatching(t, ctx, src, mint, maxt, testMetricMatcher)
	src.takeReads()
	// a restarted instance has the blocks but not the index, until it reads the broader selector's blocks
	restarted := newRawTestCache(c, RawCacheOptions{})
	restarted.selectMatching(t, ctx, src, 5*rawTestHour, 6*rawTestHour, testMetricMatcher, podA)
	require.Len(t, src.takeReads(), 1)
	restarted.selectMatching(t, ctx, src, mint, maxt, testMetricMatcher)
	require.Empty(t, src.takeReads())
	restarted.selectMatching(t, ctx, src, 6*rawTestHour, 8*rawTestHour, testMetricMatcher, podA)
	require.Empty(t, src.takeReads())
}

func TestRawCacheUsesOnlyWhatBroaderBlocksCover(t *testing.T) {
	ctx := teamRequest(rawTestTeamA)
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			src := rawTestSource(t)
			rc := newRawTestCache(newCache(), RawCacheOptions{})
			// the broader selector's block from 4h holds only what follows 4h30m
			rc.selectMatching(t, ctx, src, 4*rawTestHour+30*rawTestMinute, 9*rawTestHour, testMetricMatcher)
			src.takeReads()
			series := rc.selectMatching(t, ctx, src, 4*rawTestHour, 8*rawTestHour, testMetricMatcher, podA)
			require.Equal(t, wantMatching(t, src, 4*rawTestHour, 8*rawTestHour, testMetricMatcher, podA), pointsOf(t, series))
			require.Equal(t, [][2]int64{{4 * rawTestHour, 4*rawTestHour + 30*rawTestMinute - 1}}, src.takeReads())
		})
	}
}
