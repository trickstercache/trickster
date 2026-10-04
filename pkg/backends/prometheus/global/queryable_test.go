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
	"errors"
	"slices"
	"sync"
	"testing"

	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/stretchr/testify/require"
)

// MemorySource is a test source serving fixed series that match a select's matchers
type MemorySource struct {
	Series []storage.Series
	Err    error

	mu    sync.Mutex
	calls [][2]int64
}

func (*MemorySource) SeriesAPI() string { return prop.SeriesAPIRemoteRead }

func (m *MemorySource) Select(_ context.Context, mint, maxt int64, _ *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, error) {
	m.mu.Lock()
	m.calls = append(m.calls, [2]int64{mint, maxt})
	m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	var out []storage.Series
	for _, s := range m.Series {
		lset := s.Labels()
		if !slices.ContainsFunc(matchers, func(m *labels.Matcher) bool { return !m.Matches(lset.Get(m.Name)) }) {
			out = append(out, s)
		}
	}
	return out, nil
}

func selectAll(t *testing.T, q *Queryable, ctx context.Context, hints *storage.SelectHints) storage.SeriesSet {
	t.Helper()
	qr, err := q.Querier(0, 100)
	require.NoError(t, err)
	t.Cleanup(func() { _ = qr.Close() })
	return qr.Select(ctx, true, hints, labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric))
}

func TestQueryableSelect(t *testing.T) {
	boom := errors.New("boom")
	a := &MemorySource{Series: []storage.Series{
		listSeries(podLabels("b", testReplicaLabel, "1"), testPoint{t: 1, f: 1}),
		listSeries(podLabels("a", testReplicaLabel, "1"), testPoint{t: 1, f: 1}),
		listSeries(labels.FromStrings(labels.MetricName, "other"), testPoint{t: 1, f: 1}),
	}}
	b := &MemorySource{Series: []storage.Series{listSeries(podLabels("a", testReplicaLabel, "2"), testPoint{t: 2, f: 2})}}
	failing := &MemorySource{Err: boom}
	members := func(sources ...Source) []Member {
		out := make([]Member, len(sources))
		for i, s := range sources {
			out[i] = Member{Name: string(rune('a' + i)), Source: s}
		}
		return out
	}
	t.Run("deduplicates every member", func(t *testing.T) {
		set := selectAll(t, NewQueryable(members(a, b), []string{testReplicaLabel}, merge.TSMDeduplicationChain, false, nil),
			context.Background(), &storage.SelectHints{Start: 10, End: 20})
		var got []labels.Labels
		for set.Next() {
			got = append(got, set.At().Labels())
		}
		require.NoError(t, set.Err())
		require.Empty(t, set.Warnings())
		require.Equal(t, []labels.Labels{podLabels("a"), podLabels("b")}, got)
		require.Contains(t, b.calls, [2]int64{10, 20}, "select hints bound the read")
	})
	t.Run("querier bounds apply without hints", func(t *testing.T) {
		set := selectAll(t, NewQueryable(members(b), nil, merge.TSMDeduplicationPenalty, false, nil), context.Background(), nil)
		require.True(t, set.Next())
		require.Contains(t, b.calls, [2]int64{0, 100})
	})
	t.Run("a failed member fails the select", func(t *testing.T) {
		set := selectAll(t, NewQueryable(members(a, failing), nil, merge.TSMDeduplicationPenalty, false, nil),
			context.Background(), nil)
		require.False(t, set.Next())
		require.ErrorIs(t, set.Err(), ErrMemberUnavailable)
		require.ErrorIs(t, set.Err(), boom)
	})
	t.Run("a failed member is a warning in a partial response", func(t *testing.T) {
		set := selectAll(t, NewQueryable(members(a, failing), nil, merge.TSMDeduplicationPenalty, true, nil),
			context.Background(), nil)
		n := 0
		for set.Next() {
			n++
		}
		require.Equal(t, 2, n)
		require.Len(t, set.Warnings(), 1)
		require.ErrorIs(t, set.Warnings().AsErrors()[0], boom)
	})
	t.Run("a limit fails even a partial response", func(t *testing.T) {
		limited := &MemorySource{Err: ErrFetchLimit}
		set := selectAll(t, NewQueryable(members(a, limited), nil, merge.TSMDeduplicationPenalty, true, nil),
			context.Background(), nil)
		require.False(t, set.Next())
		require.ErrorIs(t, set.Err(), ErrFetchLimit)
	})
	t.Run("cancellation fails even a partial response", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		set := selectAll(t, NewQueryable(members(a, failing), nil, merge.TSMDeduplicationPenalty, true, nil), ctx, nil)
		require.Error(t, set.Err())
	})
}

func TestQuerierServesNoLabels(t *testing.T) {
	qr, err := NewQueryable(nil, nil, "", false, nil).Querier(0, 1)
	require.NoError(t, err)
	values, warnings, err := qr.LabelValues(context.Background(), testPod, nil)
	require.NoError(t, err)
	require.Empty(t, values)
	require.Empty(t, warnings)
	names, warnings, err := qr.LabelNames(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, names)
	require.Empty(t, warnings)
	require.NoError(t, qr.Close())
}
