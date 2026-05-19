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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// TestWaitForFirstMatchingWinsAndCancelsLosers asserts that WaitForFirst
// returns the first result whose predicate matches, even while other slots
// are still in-flight, and that the in-flight slots observe ctx cancel.
func TestWaitForFirstMatchingWinsAndCancelsLosers(t *testing.T) {
	const fast = 5 * time.Millisecond
	const slow = 2 * time.Second

	var slowCancelled atomic.Int32
	slowHandler := func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			slowCancelled.Add(1)
		case <-time.After(slow):
		}
	}
	winner := func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(fast)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("winner"))
	}

	t0, _ := albpool.Target(http.HandlerFunc(slowHandler))
	t1, _ := albpool.Target(http.HandlerFunc(winner))
	t2, _ := albpool.Target(http.HandlerFunc(slowHandler))
	targets := pool.Targets{t0, t1, t2}
	parent := albpool.NewParentGET(t)

	matches202 := func(r *Result) bool {
		return r.Capture.StatusCode() == http.StatusAccepted
	}

	start := time.Now()
	idx, results, err := WaitForFirst(context.Background(), parent, targets, Config{Mechanism: "test-waitforfirst"}, matches202)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Equal(t, 1, idx, "winner should be slot 1")
	require.Len(t, results, 3)
	require.NotNil(t, results[1].Capture)
	require.Equal(t, "winner", string(results[1].Capture.Body()))
	require.Less(t, elapsed, slow, "WaitForFirst must not wait for slow losers")
	// WaitForFirst returns on winner-claim without draining losers, so the
	// loser goroutines observe ctx cancel asynchronously after return.
	require.Eventuallyf(t, func() bool { return slowCancelled.Load() == 2 },
		slow, 5*time.Millisecond,
		"both slow slots must observe ctx cancel (got %d)", slowCancelled.Load())
}

// TestWaitForFirstReturnsBeforeLoserDrains asserts that a winner is returned
// without waiting for a non-winning handler that ignores cancellation.
func TestWaitForFirstReturnsBeforeLoserDrains(t *testing.T) {
	const loserDelay = 250 * time.Millisecond

	var loserDone atomic.Bool
	loser := func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(loserDelay)
		loserDone.Store(true)
	}
	winner := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("winner"))
	}

	t0, _ := albpool.Target(http.HandlerFunc(loser))
	t1, _ := albpool.Target(http.HandlerFunc(winner))
	targets := pool.Targets{t0, t1}
	parent := albpool.NewParentGET(t)

	matches202 := func(r *Result) bool {
		return r.Capture.StatusCode() == http.StatusAccepted
	}

	start := time.Now()
	idx, results, err := WaitForFirst(context.Background(), parent, targets, Config{Mechanism: "test-waitforfirst"}, matches202)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Equal(t, 1, idx, "winner should be slot 1")
	require.NotNil(t, results[1].Capture)
	require.Equal(t, "winner", string(results[1].Capture.Body()))
	require.Less(t, elapsed, loserDelay/2, "WaitForFirst must not wait for a loser that ignores cancellation")
	require.Eventually(t, loserDone.Load, 2*loserDelay, 10*time.Millisecond, "loser did not finish")
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
