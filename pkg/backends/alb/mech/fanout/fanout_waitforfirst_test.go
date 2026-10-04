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

package fanout

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func loserDrainCount(t testing.TB, mech, variant string) uint64 {
	t.Helper()
	m := &dto.Metric{}
	require.NoError(t, metrics.ALBFanoutLoserDrain.WithLabelValues(mech, variant).(prometheus.Metric).Write(m))
	return m.GetHistogram().GetSampleCount()
}

func TestWaitForFirstMatchingWinsAndCancelsLosers(t *testing.T) {
	started := make(chan struct{}, 2)
	var slowCancelled atomic.Int32
	slowHandler := func(_ http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		slowCancelled.Add(1)
	}
	winner := func(w http.ResponseWriter, r *http.Request) {
		for range 2 {
			select {
			case <-started:
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("winner"))
	}
	t0, _ := albpool.Target(http.HandlerFunc(slowHandler))
	t1, _ := albpool.Target(http.HandlerFunc(winner))
	t2, _ := albpool.Target(http.HandlerFunc(slowHandler))
	parent := albpool.NewParentGET(t)
	matches202 := func(r *Result) bool { return r.Capture.StatusCode() == http.StatusAccepted }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	idx, results, err := WaitForFirst(ctx, parent, pool.Targets{t0, t1, t2},
		Config{Mechanism: "test-waitforfirst"}, matches202)
	require.NoError(t, err)
	require.Equal(t, 1, idx)
	require.Len(t, results, 3)
	require.NotNil(t, results[1].Capture)
	require.Equal(t, "winner", string(results[1].Capture.Body()))
	require.Eventually(t, func() bool { return slowCancelled.Load() == 2 },
		5*time.Second, 5*time.Millisecond, "both slow slots must observe cancellation")
}

func TestWaitForFirstReturnsBeforeLoserDrains(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var loserDone atomic.Bool
	loser := func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		loserDone.Store(true)
	}
	winner := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("winner"))
	}
	t0, _ := albpool.Target(http.HandlerFunc(loser))
	t1, _ := albpool.Target(http.HandlerFunc(winner))
	parent := albpool.NewParentGET(t)
	matches202 := func(r *Result) bool { return r.Capture.StatusCode() == http.StatusAccepted }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	var idx int
	var results []Result
	var err error
	go func() {
		defer close(done)
		idx, results, err = WaitForFirst(ctx, parent, pool.Targets{t0, t1},
			Config{Mechanism: "test-waitforfirst"}, matches202)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("WaitForFirst waited for the blocked loser")
	}
	require.NoError(t, err)
	require.Equal(t, 1, idx)
	require.NotNil(t, results[1].Capture)
	require.Equal(t, "winner", string(results[1].Capture.Body()))
	require.False(t, loserDone.Load(), "the loser must still be held when the winner returns")
	unblock()
	require.Eventually(t, loserDone.Load, 5*time.Second, 5*time.Millisecond, "loser did not finish")
}

// TestWaitForFirstNoMatchReturnsMinusOne asserts that when predicate never
// matches, WaitForFirst returns winnerIdx = -1 and all results are populated.
func TestWaitForFirstNoMatchReturnsMinusOne(t *testing.T) {
	t0, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("a"))
	}))
	t1, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("b"))
	}))
	targets := pool.Targets{t0, t1}
	parent := albpool.NewParentGET(t)
	never := func(_ *Result) bool { return false }

	idx, results, err := WaitForFirst(context.Background(), parent, targets, Config{Mechanism: "test-waitforfirst"}, never)
	require.NoError(t, err)
	require.Equal(t, -1, idx)
	require.Len(t, results, 2)
	for i := range results {
		require.NotNil(t, results[i].Capture, "slot %d should still have a capture", i)
	}
}

// TestWaitForFirstTruncatedNotEligible asserts that a slot whose capture was
// truncated is never offered to the predicate, even if the status code
// would otherwise qualify.
func TestWaitForFirstTruncatedNotEligible(t *testing.T) {
	const maxBytes = 16
	big := bytes.Repeat([]byte("x"), 1024)

	intactBody := "ok"
	var sawTruncatedCandidate atomic.Bool
	t0, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(big)
	}))
	t1, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(intactBody))
	}))
	targets := pool.Targets{t0, t1}
	parent := albpool.NewParentGET(t)

	pred := func(r *Result) bool {
		if r.Capture.StatusCode() == http.StatusCreated {
			sawTruncatedCandidate.Store(true)
		}
		return r.Capture.StatusCode() < 300
	}

	idx, results, err := WaitForFirst(context.Background(), parent, targets, Config{Mechanism: "test-waitforfirst-trunc", MaxCaptureBytes: maxBytes}, pred)
	require.NoError(t, err)
	require.Equal(t, 1, idx, "intact slot must win; truncated slot must not be eligible")
	require.Equal(t, intactBody, string(results[1].Capture.Body()))
	require.False(t, sawTruncatedCandidate.Load(), "truncated slot must not be offered to predicate")
}

// TestWaitForFirstObservesLoserDrain asserts that ALBFanoutLoserDrain records
// one observation per losing slot once the winner is claimed, and skips the
// winner itself. Loser exit timings are captured even when the loser ignores
// ctx cancel until its own sleep finishes.
func TestWaitForFirstObservesLoserDrain(t *testing.T) {
	const variant = "loser-drain-test"
	const mech = "test-loser-drain"
	const loserDelay = 75 * time.Millisecond

	release := make(chan struct{})
	loser := func(_ http.ResponseWriter, _ *http.Request) {
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
	}
	winner := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("winner"))
	}

	t0, _ := albpool.Target(http.HandlerFunc(loser))
	t1, _ := albpool.Target(http.HandlerFunc(winner))
	t2, _ := albpool.Target(http.HandlerFunc(loser))
	targets := pool.Targets{t0, t1, t2}
	parent := albpool.NewParentGET(t)

	matches202 := func(r *Result) bool {
		return r.Capture.StatusCode() == http.StatusAccepted
	}

	before := loserDrainCount(t, mech, variant)

	idx, _, err := WaitForFirst(context.Background(), parent, targets,
		Config{Mechanism: mech, Variant: variant}, matches202)
	require.NoError(t, err)
	require.Equal(t, 1, idx, "slot 1 must win")

	// Give the losers a moment past winner-claim so the delta is visibly
	// non-zero, then let them finish so onComplete can observe their drain.
	time.Sleep(loserDelay)
	close(release)

	require.Eventuallyf(t, func() bool {
		return loserDrainCount(t, mech, variant)-before == 2
	}, 2*time.Second, 10*time.Millisecond,
		"expected 2 loser-drain observations, got %d", loserDrainCount(t, mech, variant)-before)
}

// TestWaitForFirstNoMatchSkipsLoserDrain asserts that when no winner is ever
// claimed (predicate never matches), no loser-drain observations are recorded.
// The histogram is meaningless without a winner-claim reference point.
func TestWaitForFirstNoMatchSkipsLoserDrain(t *testing.T) {
	const variant = "no-winner-test"
	const mech = "test-no-winner-drain"

	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}
	t0, _ := albpool.Target(http.HandlerFunc(handler))
	t1, _ := albpool.Target(http.HandlerFunc(handler))
	targets := pool.Targets{t0, t1}
	parent := albpool.NewParentGET(t)
	never := func(_ *Result) bool { return false }

	before := loserDrainCount(t, mech, variant)
	idx, _, err := WaitForFirst(context.Background(), parent, targets,
		Config{Mechanism: mech, Variant: variant}, never)
	require.NoError(t, err)
	require.Equal(t, -1, idx)
	require.Equal(t, before, loserDrainCount(t, mech, variant),
		"no winner means no loser-drain observations")
}

// TestWaitForFirstNilPredicateBehavesLikeAll asserts that passing a nil
// predicate is equivalent to calling All (no winner, no early termination).
func TestWaitForFirstNilPredicateBehavesLikeAll(t *testing.T) {
	t0, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("a"))
	}))
	t1, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("b"))
	}))
	targets := pool.Targets{t0, t1}
	parent := albpool.NewParentGET(t)
	idx, results, err := WaitForFirst(context.Background(), parent, targets, Config{Mechanism: "test-waitforfirst-nil"}, nil)
	require.NoError(t, err)
	require.Equal(t, -1, idx)
	require.Len(t, results, 2)
	require.Equal(t, "a", string(results[0].Capture.Body()))
	require.Equal(t, "b", string(results[1].Capture.Body()))
}
