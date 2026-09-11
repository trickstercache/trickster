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

package poller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// settleTime is how long a test waits before asserting that something has
// NOT happened. Negative assertions cannot be made deterministic; this is
// long enough to be reliable and short enough to keep the suite quick.
const settleTime = 150 * time.Millisecond

// waitTime bounds positive assertions, which fail fast in practice and only
// hit this bound when genuinely broken.
const waitTime = 5 * time.Second

// noJitter disables startup jitter so tests observe the first iteration
// immediately rather than up to a second later.
const noJitter = -1 * time.Nanosecond

// recorder is a Source that reports each call on a channel and returns
// scripted results.
type recorder struct {
	calls chan time.Time
	fn    func(ctx context.Context, n int) (time.Duration, error)
	count atomic.Int64
}

func newRecorder(fn func(ctx context.Context, n int) (time.Duration, error)) *recorder {
	return &recorder{calls: make(chan time.Time, 128), fn: fn}
}

func (r *recorder) Poll(ctx context.Context) (time.Duration, error) {
	n := int(r.count.Add(1))
	select {
	case r.calls <- time.Now():
	default:
	}
	if r.fn == nil {
		return 0, nil
	}
	return r.fn(ctx, n)
}

// awaitCall blocks until the next recorded call or fails the test.
func (r *recorder) awaitCall(t *testing.T) time.Time {
	t.Helper()
	select {
	case ts := <-r.calls:
		return ts
	case <-time.After(waitTime):
		t.Fatal("timed out waiting for a poll iteration")
		return time.Time{}
	}
}

// mustStart constructs a poller, starts it, and registers Stop for cleanup
// so a failing assertion cannot leak the loop past the test.
func mustStart(t *testing.T, o Options, src Source) *Poller {
	t.Helper()
	p, err := New(o, src)
	require.NoError(t, err)
	p.Start(t.Context())
	t.Cleanup(p.Stop)
	return p
}

func TestNewValidation(t *testing.T) {
	noop := Func(func(context.Context) (time.Duration, error) { return 0, nil })
	tests := map[string]struct {
		o    Options
		src  Source
		want error
	}{
		"nil source":        {Options{Interval: time.Second}, nil, ErrNilSource},
		"zero interval":     {Options{}, noop, ErrInvalidInterval},
		"negative interval": {Options{Interval: -time.Second}, noop, ErrInvalidInterval},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			p, err := New(test.o, test.src)
			require.ErrorIs(t, err, test.want)
			require.Nil(t, p)
		})
	}
	p, err := New(Options{Name: "ok", Interval: time.Second}, noop)
	require.NoError(t, err)
	require.Equal(t, "ok", p.Name())
}

// A poller that has just started should not wait out its interval before
// producing an answer; the caller started it because it wants one now.
func TestFirstIterationIsImmediate(t *testing.T) {
	r := newRecorder(nil)
	start := time.Now()
	mustStart(t, Options{Interval: time.Hour, Jitter: noJitter}, r)
	first := r.awaitCall(t)
	require.Less(t, first.Sub(start), waitTime,
		"the first iteration waited for the interval instead of running immediately")
}

// Jitter must delay the first iteration, not skip it.
func TestJitterDelaysButDoesNotSkipTheFirstIteration(t *testing.T) {
	r := newRecorder(nil)
	mustStart(t, Options{Interval: time.Hour, Jitter: 50 * time.Millisecond}, r)
	r.awaitCall(t)
	require.EqualValues(t, 1, r.count.Load())
}

// A Source returning a positive next overrides the configured interval for
// that one iteration -- the DNS TTL-floor case.
func TestSourceNextOverridesInterval(t *testing.T) {
	r := newRecorder(func(context.Context, int) (time.Duration, error) {
		return time.Millisecond, nil
	})
	mustStart(t, Options{Interval: time.Hour, Jitter: noJitter}, r)
	for range 3 {
		r.awaitCall(t)
	}
}

// PollNow re-issues immediately: the blocking-query case, where the server
// has already done the waiting.
func TestPollNowIteratesImmediately(t *testing.T) {
	r := newRecorder(func(context.Context, int) (time.Duration, error) {
		return PollNow, nil
	})
	mustStart(t, Options{Interval: time.Hour, Jitter: noJitter}, r)
	for range 3 {
		r.awaitCall(t)
	}
}

// A zero next means "use the interval", which a long interval makes
// observable: exactly one iteration, then quiet.
func TestZeroNextUsesInterval(t *testing.T) {
	r := newRecorder(nil)
	mustStart(t, Options{Interval: time.Hour, Jitter: noJitter}, r)
	r.awaitCall(t)
	time.Sleep(settleTime)
	require.EqualValues(t, 1, r.count.Load(),
		"a zero next should fall back to the interval, not re-poll")
}

// The whole point of the package: a panicking Source must not kill the loop.
func TestPanicIsRecoveredAndLoopContinues(t *testing.T) {
	var panics atomic.Int64
	r := newRecorder(func(_ context.Context, n int) (time.Duration, error) {
		if n == 1 {
			panic("boom")
		}
		return time.Millisecond, nil
	})
	mustStart(t, Options{
		Interval: time.Millisecond,
		Jitter:   noJitter,
		OnPanic:  func(any, []byte) { panics.Add(1) },
	}, r)
	for range 3 {
		r.awaitCall(t)
	}
	require.EqualValues(t, 1, panics.Load(), "OnPanic should fire once, for the one panic")
}

// A nil OnPanic must still recover -- the default handler logs rather than
// leaving the panic to kill the goroutine.
func TestNilPanicHandlerStillRecovers(t *testing.T) {
	r := newRecorder(func(_ context.Context, n int) (time.Duration, error) {
		if n == 1 {
			panic("boom")
		}
		return time.Millisecond, nil
	})
	mustStart(t, Options{Interval: time.Millisecond, Jitter: noJitter}, r)
	for range 2 {
		r.awaitCall(t)
	}
}

// A panicking source is a failing source: it must take the backoff path
// rather than spinning at the interval.
func TestPanicCountsAsFailureForBackoff(t *testing.T) {
	r := newRecorder(func(context.Context, int) (time.Duration, error) {
		panic("always")
	})
	mustStart(t, Options{
		Interval:   time.Millisecond,
		Jitter:     noJitter,
		MaxBackoff: time.Hour,
		OnPanic:    func(any, []byte) {},
	}, r)
	r.awaitCall(t)
	r.awaitCall(t)
	// backoff has now reached at least 2ms and keeps doubling toward an
	// hour, so the count must stall well short of what 1ms spinning gives
	time.Sleep(settleTime)
	require.Less(t, r.count.Load(), int64(20),
		"a persistently panicking source spun instead of backing off")
}

// Errors must not stop the loop, and must not be reported as anything the
// poller acts on beyond backoff.
func TestErrorDoesNotStopLoop(t *testing.T) {
	errBoom := errors.New("boom")
	r := newRecorder(func(context.Context, int) (time.Duration, error) {
		return 0, errBoom
	})
	mustStart(t, Options{Interval: time.Millisecond, Jitter: noJitter}, r)
	for range 3 {
		r.awaitCall(t)
	}
}

// Without DetachIterations, Stop cancels the in-flight iteration so that
// shutdown is prompt even when the Source is mid-blocking-query.
func TestStopCancelsInFlightIterationByDefault(t *testing.T) {
	entered := make(chan struct{})
	var observedCancel atomic.Bool
	var once sync.Once
	r := newRecorder(func(ctx context.Context, _ int) (time.Duration, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		observedCancel.Store(true)
		return 0, ctx.Err()
	})
	p, err := New(Options{Interval: time.Hour, Jitter: noJitter}, r)
	require.NoError(t, err)
	p.Start(t.Context())
	<-entered
	p.Stop() // must not hang: the iteration is cancelled, not awaited
	require.True(t, observedCancel.Load(), "the in-flight iteration was not cancelled")
}

// With DetachIterations, Stop waits for the in-flight iteration instead of
// cancelling it -- the health-check re-registration guarantee.
func TestDetachedStopWaitsForInFlightIteration(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var finished atomic.Bool
	var once sync.Once
	r := newRecorder(func(ctx context.Context, _ int) (time.Duration, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
			t.Error("detached iteration observed cancellation; it should not have")
		}
		finished.Store(true)
		return 0, nil
	})
	p, err := New(Options{
		Interval: time.Hour, Jitter: noJitter, DetachIterations: true,
	}, r)
	require.NoError(t, err)
	p.Start(t.Context())
	<-entered

	stopped := make(chan struct{})
	go func() {
		p.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a detached iteration was still running")
	case <-time.After(settleTime):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(waitTime):
		t.Fatal("Stop did not return after the iteration finished")
	}
	require.True(t, finished.Load())
}

// Timeout deadlines a single iteration without ending the loop.
func TestTimeoutBoundsOneIteration(t *testing.T) {
	deadlines := make(chan bool, 4)
	r := newRecorder(func(ctx context.Context, _ int) (time.Duration, error) {
		_, ok := ctx.Deadline()
		select {
		case deadlines <- ok:
		default:
		}
		<-ctx.Done()
		return 0, ctx.Err()
	})
	mustStart(t, Options{
		Interval: time.Millisecond, Jitter: noJitter, Timeout: 10 * time.Millisecond,
	}, r)
	require.True(t, <-deadlines, "iteration context carried no deadline")
	require.True(t, <-deadlines, "loop did not survive a timed-out iteration")
}

func TestTriggerRunsAnIterationEarly(t *testing.T) {
	r := newRecorder(nil)
	p := mustStart(t, Options{Interval: time.Hour, Jitter: noJitter}, r)
	r.awaitCall(t) // the immediate first iteration
	p.Trigger()
	r.awaitCall(t) // would otherwise be an hour away
	require.EqualValues(t, 2, r.count.Load())
}

// Trigger coalesces: a burst produces a bounded number of extra iterations,
// not one per call. The bound is two rather than one because a trigger
// raised while an iteration is in flight must still be honored -- that
// iteration may have read its data before the change landed.
func TestTriggerCoalesces(t *testing.T) {
	r := newRecorder(nil)
	p := mustStart(t, Options{Interval: time.Hour, Jitter: noJitter}, r)
	r.awaitCall(t)
	for range 50 {
		p.Trigger()
	}
	r.awaitCall(t)
	time.Sleep(settleTime)
	require.LessOrEqual(t, r.count.Load(), int64(3),
		"50 triggers should coalesce into at most two extra iterations")
}

// A trigger raised while the poller is not running must not be banked and
// replayed: the immediate first iteration on the next Start already answers
// it, and replaying would spend a second iteration on the same request.
func TestTriggerOnStoppedPollerIsNoOp(t *testing.T) {
	r := newRecorder(nil)
	p, err := New(Options{Interval: time.Hour, Jitter: noJitter}, r)
	require.NoError(t, err)
	require.NotPanics(t, p.Trigger) // before ever started
	p.Start(t.Context())
	r.awaitCall(t)
	time.Sleep(settleTime)
	require.EqualValues(t, 1, r.count.Load(),
		"a trigger raised before Start was replayed as an extra iteration")
	p.Stop()
	require.NotPanics(t, p.Trigger) // after stopping
	time.Sleep(settleTime)
	require.EqualValues(t, 1, r.count.Load())
}

func TestStopIsIdempotentAndSafeBeforeStart(t *testing.T) {
	r := newRecorder(nil)
	p, err := New(Options{Interval: time.Hour, Jitter: noJitter}, r)
	require.NoError(t, err)
	require.NotPanics(t, p.Stop) // never started
	p.Start(t.Context())
	r.awaitCall(t)
	p.Stop()
	require.NotPanics(t, p.Stop)
}

// Restarting must not leave the previous loop running.
func TestRestartReplacesPreviousLoop(t *testing.T) {
	r := newRecorder(nil)
	p, err := New(Options{Interval: 5 * time.Millisecond, Jitter: noJitter}, r)
	require.NoError(t, err)
	t.Cleanup(p.Stop)
	p.Start(t.Context())
	r.awaitCall(t)
	p.Start(t.Context())
	r.awaitCall(t)
	p.Stop()
	settled := r.count.Load()
	time.Sleep(settleTime)
	require.Equal(t, settled, r.count.Load(),
		"a restarted poller left its predecessor loop running")
}

// Cancelling the context passed to Start is equivalent to Stop.
func TestContextCancellationStopsLoop(t *testing.T) {
	r := newRecorder(nil)
	p, err := New(Options{Interval: 5 * time.Millisecond, Jitter: noJitter}, r)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	p.Start(ctx)
	r.awaitCall(t)
	cancel()
	// Stop still reaps the goroutine and must not hang
	p.Stop()
	settled := r.count.Load()
	time.Sleep(settleTime)
	require.Equal(t, settled, r.count.Load(), "loop kept running after its context was cancelled")
}

func TestBackoff(t *testing.T) {
	const base = time.Second
	const ceiling = 10 * time.Second
	tests := map[string]struct {
		base     time.Duration
		failures int
		ceiling  time.Duration
		want     time.Duration
	}{
		"first failure holds at base": {base, 1, ceiling, time.Second},
		"second failure doubles":      {base, 2, ceiling, 2 * time.Second},
		"third failure doubles again": {base, 3, ceiling, 4 * time.Second},
		"clamps to ceiling":           {base, 4, ceiling, 8 * time.Second},
		"stays at ceiling":            {base, 5, ceiling, ceiling},
		"zero failures holds at base": {base, 0, ceiling, time.Second},
		"absurd failure count does not overflow": {
			base, 1000, ceiling, ceiling,
		},
		"base above ceiling clamps":        {time.Minute, 1, time.Second, time.Second},
		"non-positive base yields ceiling": {0, 3, ceiling, ceiling},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, test.want, backoff(test.base, test.failures, test.ceiling))
		})
	}
}

// Backoff must be opt-in: with MaxBackoff unset, a failing source keeps
// polling at its interval rather than silently slowing down.
func TestNoBackoffWhenMaxBackoffUnset(t *testing.T) {
	errBoom := errors.New("boom")
	r := newRecorder(func(context.Context, int) (time.Duration, error) {
		return 0, errBoom
	})
	mustStart(t, Options{Interval: time.Millisecond, Jitter: noJitter}, r)
	for range 5 {
		r.awaitCall(t)
	}
}

func TestRandomDuration(t *testing.T) {
	require.Zero(t, randomDuration(0))
	require.Zero(t, randomDuration(-time.Second))
	for range 100 {
		d := randomDuration(10 * time.Millisecond)
		require.GreaterOrEqual(t, d, time.Duration(0))
		require.Less(t, d, 10*time.Millisecond)
	}
}

func TestFuncAdaptsToSource(t *testing.T) {
	var called bool
	var f Source = Func(func(context.Context) (time.Duration, error) {
		called = true
		return time.Second, nil
	})
	next, err := f.Poll(t.Context())
	require.NoError(t, err)
	require.Equal(t, time.Second, next)
	require.True(t, called)
}
