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

package dataset

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// makeNsPoints constructs Points using nanosecond epochs.
func makeNsPoints(vals ...ev) Points {
	p := make(Points, len(vals))
	for i, v := range vals {
		p[i] = Point{
			Epoch:  epoch.Epoch(v.epoch),
			Size:   32,
			Values: []any{v.value},
		}
	}
	return p
}

func TestSortAndDedupeWithinTolerance(t *testing.T) {
	// Two shards sampled the same series at slightly skewed nanosecond
	// timestamps; without tolerance both points survive and produce a
	// double-density series. With a 5s tolerance, sub-step duplicates cluster
	// and the first-seen point per cluster wins.
	const tol = int64(5 * 1_000_000_000) // 5s in ns
	p := makeNsPoints(
		ev{1_000_000_000, "1.0"},
		ev{1_007_000_000, "2.0"},
		ev{60_000_000_000, "10.0"},
		ev{60_004_000_000, "20.0"},
		ev{120_000_000_000, "30.0"},
	)
	out := sortAndDedupeTolerant(p, tol)
	require.Len(t, out, 3)
	require.Equal(t, epoch.Epoch(1_000_000_000), out[0].Epoch)
	require.Equal(t, "1.0", out[0].Values[0])
	require.Equal(t, epoch.Epoch(60_000_000_000), out[1].Epoch)
	require.Equal(t, "10.0", out[1].Values[0])
	require.Equal(t, epoch.Epoch(120_000_000_000), out[2].Epoch)
	require.Equal(t, "30.0", out[2].Values[0])
}

func TestSortAndDedupeToleranceZeroExactMatch(t *testing.T) {
	// Regression: tolerance=0 must preserve the original exact-epoch dedup
	// semantics (only epochs that match bit-for-bit collapse; highest-index
	// wins).
	p := makeNsPoints(
		ev{100, "1.0"},
		ev{107, "2.0"},
		ev{100, "3.0"},
		ev{200, "4.0"},
	)
	out := sortAndDedupeTolerant(p, 0)
	require.Len(t, out, 3)
	require.Equal(t, epoch.Epoch(100), out[0].Epoch)
	require.Equal(t, "3.0", out[0].Values[0])
	require.Equal(t, epoch.Epoch(107), out[1].Epoch)
	require.Equal(t, "2.0", out[1].Values[0])
	require.Equal(t, epoch.Epoch(200), out[2].Epoch)
	require.Equal(t, "4.0", out[2].Values[0])
}

func TestSortAndDedupeToleranceClusterChain(t *testing.T) {
	// Clusters are anchored at the surviving point: 100 absorbs 108 (delta 8
	// <= 10), but 115 exits the cluster (115-100=15 > 10) and becomes a new
	// anchor. This anchor-based rule prevents an unbounded chain of small
	// gaps from collapsing widely-separated samples.
	const tol = int64(10)
	p := makeNsPoints(
		ev{100, "a"},
		ev{108, "b"},
		ev{115, "c"},
		ev{200, "d"},
	)
	out := sortAndDedupeTolerant(p, tol)
	require.Len(t, out, 3)
	require.Equal(t, epoch.Epoch(100), out[0].Epoch)
	require.Equal(t, "a", out[0].Values[0])
	require.Equal(t, epoch.Epoch(115), out[1].Epoch)
	require.Equal(t, "c", out[1].Values[0])
	require.Equal(t, epoch.Epoch(200), out[2].Epoch)
	require.Equal(t, "d", out[2].Values[0])
}

func TestMergePointsWithOptsTolerance(t *testing.T) {
	const tol = int64(5_000_000_000)
	p1 := makeNsPoints(
		ev{1_000_000_000, "1"},
		ev{60_000_000_000, "2"},
	)
	p2 := makeNsPoints(
		ev{1_007_000_000, "3"},
		ev{60_004_000_000, "4"},
	)
	out := MergePointsWithOpts(p1, p2, MergeOpts{
		SortPoints:     true,
		Strategy:       MergeStrategyDedup,
		ToleranceNanos: tol,
	})
	require.Len(t, out, 2)
	require.Equal(t, epoch.Epoch(1_000_000_000), out[0].Epoch)
	require.Equal(t, "1", out[0].Values[0])
	require.Equal(t, epoch.Epoch(60_000_000_000), out[1].Epoch)
	require.Equal(t, "2", out[1].Values[0])
}

func TestMergePointsWithOptsToleranceZeroParity(t *testing.T) {
	// MergeOpts with tolerance=0 must match legacy MergePoints output
	// (current Trickster default).
	p1 := makeNsPoints(ev{100, "1"}, ev{200, "2"})
	p2 := makeNsPoints(ev{100, "3"}, ev{300, "4"})
	out := MergePointsWithOpts(p1, p2, MergeOpts{
		SortPoints:     true,
		Strategy:       MergeStrategyDedup,
		ToleranceNanos: 0,
	})
	legacy := MergePoints(p1.Clone(), p2.Clone(), true)
	require.Equal(t, legacy, out)
}
