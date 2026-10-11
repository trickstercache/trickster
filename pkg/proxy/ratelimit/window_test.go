/*
 * Copyright 2026 The Trickster Authors
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

package ratelimit

import (
	"math"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func newTest(limit uint32, window time.Duration, maxKeys int) *Limiter {
	return New(Config{Limit: limit, Window: window, MaxKeys: maxKeys})
}

func TestTakeCountsUntilTheEstimate(t *testing.T) {
	const window = time.Second
	l := newTest(3, window, 10)
	now := int64(window / 2)
	for i := range 3 {
		d := l.Take(1, true, now, 1)
		if !d.Allowed || d.Result != ResultAllowed {
			t.Fatalf("event %d: %+v", i, d)
		}
	}
	d := l.Take(1, true, now, 1)
	if d.Allowed || d.Result != ResultLimited || !d.RetryKnown {
		t.Fatalf("over: %+v", d)
	}
	if d.RetryAfter <= 0 || d.RetryAfter > 2*window {
		t.Fatalf("retry = %v", d.RetryAfter)
	}
	if got := l.Take(1, true, now+int64(d.RetryAfter), 1); !got.Allowed {
		t.Fatalf("retry did not admit: %+v", got)
	}
}

func TestRetryAfterIsNotJustTheNextBoundary(t *testing.T) {
	// A full current count still weighs as the previous window at the next boundary, so a cost
	// equal to the limit is not admissible until both counts have rolled away.
	const window = time.Second
	l := newTest(10, window, 10)
	now := int64(0)
	l.Charge(1, true, now, 10)
	d := l.Take(1, true, now, 10)
	if d.Allowed || !d.RetryKnown {
		t.Fatalf("expected a finite refusal, got %+v", d)
	}
	if d.RetryAfter <= window {
		t.Fatalf("retry %v stops at the next boundary; the carried count still fills it", d.RetryAfter)
	}
	if d.RetryAfter > 2*window {
		t.Fatalf("retry %v exceeds two windows", d.RetryAfter)
	}
	if got := l.Take(1, true, now+int64(d.RetryAfter)-1, 10); got.Allowed {
		t.Fatal("admitted before the returned wait")
	}
	fresh := newTest(10, window, 10)
	fresh.Charge(1, true, now, 10)
	if got := fresh.Take(1, true, now+int64(d.RetryAfter), 10); !got.Allowed {
		t.Fatalf("not admissible at the returned wait: %+v", got)
	}
	if sec := RetryAfterSeconds(d.RetryAfter); sec < 1 || time.Duration(sec)*time.Second < d.RetryAfter {
		t.Fatalf("seconds %d does not round %v up", sec, d.RetryAfter)
	}
}

func TestCostAboveTheLimitNeverAdmits(t *testing.T) {
	l := newTest(5, time.Second, 10)
	d := l.Take(1, true, 0, 6)
	if d.Allowed || d.Result != ResultLimited || d.RetryKnown {
		t.Fatalf("cost above the limit: %+v", d)
	}
}

func TestChargePushesTheNextTakeOver(t *testing.T) {
	l := newTest(10, time.Second, 10)
	l.Charge(1, true, 0, 10)
	d := l.Take(1, true, 0, 1)
	if d.Allowed || d.Result != ResultLimited {
		t.Fatalf("after charge: %+v", d)
	}
}

func TestCountChargesALimitedEventOnce(t *testing.T) {
	l := newTest(2, time.Second, 10)
	if d := l.Count(1, true, 0, 2); !d.Allowed || d.Result != ResultAllowed {
		t.Fatalf("under: %+v", d)
	}
	d := l.Count(1, true, 0, 1)
	if !d.Allowed || d.Result != ResultCounted {
		t.Fatalf("count mode: %+v", d)
	}
	again := l.Take(1, true, 0, 1)
	if again.Allowed {
		t.Fatal("count mode charged twice or not at all")
	}
}

func TestCostGreaterThanOne(t *testing.T) {
	l := newTest(10, time.Second, 10)
	if d := l.Take(1, true, 0, 4); !d.Allowed || d.Remaining != 6 {
		t.Fatalf("first: %+v", d)
	}
	if d := l.Take(1, true, 0, 4); !d.Allowed || d.Remaining != 2 {
		t.Fatalf("second: %+v", d)
	}
	if d := l.Take(1, true, 0, 4); d.Allowed {
		t.Fatalf("third: %+v", d)
	}
}

func TestCountersSaturate(t *testing.T) {
	l := newTest(math.MaxUint32, time.Second, 10)
	l.Charge(1, true, 0, math.MaxUint32)
	l.Charge(1, true, 0, 10)
	d := l.Take(1, true, 0, 1)
	if d.Allowed {
		t.Fatal("a saturated counter accepted another event")
	}
}

func TestOlderSampleDoesNotRollBackward(t *testing.T) {
	const window = time.Second
	l := newTest(10, window, 10)
	later := int64(3 * window)
	l.Charge(1, true, later, 10)
	l.Take(1, true, 0, 1)
	d := l.Take(1, true, later, 1)
	if d.Allowed {
		t.Fatal("an older sample cleared the count")
	}
}

func TestOlderSampleUnderContention(t *testing.T) {
	const window = time.Second
	l := newTest(100, window, 10)
	// Stay inside the idle lifetime. A later timestamp past it would expire the bucket and
	// look like a lost count.
	later := int64(window)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range 20 {
			l.Charge(1, true, later, 1)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range 20 {
			l.Charge(1, true, 0, 1)
		}
	}()
	close(start)
	wg.Wait()
	d := l.Take(1, true, later, 1)
	if !d.Allowed || d.Remaining != 59 {
		t.Fatalf("remaining %d; an older sample rolled a newer count off: %+v", d.Remaining, d)
	}
}

func TestWindowRolloverKeepsThePreviousCount(t *testing.T) {
	const window = time.Second
	l := newTest(10, window, 10)
	l.Charge(1, true, int64(window)-1, 10)
	// At the next boundary the whole previous count is still weighted, so nothing new fits.
	d := l.Take(1, true, int64(window), 1)
	if d.Allowed {
		t.Fatal("rollover dropped the previous window")
	}
}

func TestClusteredArrivalsDivergeFromAnExactLog(t *testing.T) {
	// Ten events at the start of a window have left the exact trailing window by the next
	// boundary, but the estimator still weighs them as the previous window's full count.
	const (
		window = 10 * time.Second
		limit  = 10
	)
	l := newTest(limit, window, 10)
	l.Charge(1, true, 0, limit)
	at := int64(window)
	est := l.Take(1, true, at, 1)
	if est.Allowed {
		t.Fatal("estimator allowed a burst the previous window still weighs")
	}
	if exactLogAllows(at, int64(window), limit, []int64{0}) {
		return
	}
	t.Fatal("the exact log refused as well; the scenario does not show the approximation")
}

func exactLogAllows(now, window int64, limit int, events []int64) bool {
	n := 0
	for _, ts := range events {
		if ts > now-window && ts <= now {
			n++
		}
	}
	return n < limit
}

func TestEstimatorMatchesAReference(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		window := time.Duration(rapid.Int64Range(1, 10_000).Draw(t, "window"))
		limit := rapid.Uint32Range(1, 40).Draw(t, "limit")
		l := newTest(limit, window, 100)
		ref := newRef(limit, int64(window))
		now := int64(0)
		for range rapid.IntRange(1, 30).Draw(t, "n") {
			step := rapid.Int64Range(0, int64(window)*3).Draw(t, "step")
			cost := rapid.Uint32Range(1, 5).Draw(t, "cost")
			now += step
			got := l.Take(1, true, now, cost)
			want := ref.take(now, cost)
			if got.Allowed != want {
				t.Fatalf("at %d cost %d: allowed %v, reference %v", now, cost, got.Allowed, want)
			}
		}
	})
}

func TestFirstEventsShareOneBucket(t *testing.T) {
	l := newTest(2, time.Second, 10)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			<-start
			if d := l.Take(9, true, 1, 1); !d.Allowed {
				t.Error("first events did not share a bucket")
			}
		}()
	}
	close(start)
	wg.Wait()
	if l.Take(9, true, 1, 1).Allowed {
		t.Fatal("the shared bucket lost a count")
	}
}

func TestHammerOneKey(t *testing.T) {
	const (
		n = 50
		g = 16
	)
	l := newTest(n*g, time.Minute, 10)
	now := int64(time.Minute)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(g)
	for range g {
		go func() {
			defer wg.Done()
			<-start
			for range n {
				if d := l.Take(1, true, now, 1); !d.Allowed {
					t.Error("lost a count")
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if l.Take(1, true, now, 1).Allowed {
		t.Fatal("the hammer stored more or less than one count per event")
	}
}

func TestTakeHitDoesNotAllocate(t *testing.T) {
	l := newTest(math.MaxUint32, time.Minute, 10)
	now := int64(time.Minute)
	l.Take(1, true, now, 1)
	allocs := testing.AllocsPerRun(1000, func() {
		l.Take(1, true, now, 1)
	})
	if allocs != 0 {
		t.Fatalf("hit allocated %v times", allocs)
	}
}

// ref is an independent copy of the estimator, kept in the test so a change to either side
// has to be explained.
type ref struct {
	limit     uint32
	window    int64
	idx       uint64
	prev, cur uint32
	ready     bool
}

func newRef(limit uint32, window int64) *ref {
	return &ref{limit: limit, window: window}
}

func (r *ref) take(now int64, cost uint32) bool {
	i := uint64(now / r.window)
	if !r.ready {
		r.idx = i
		r.ready = true
	} else if i > r.idx+1 {
		r.prev, r.cur = 0, 0
		r.idx = i
	} else if i == r.idx+1 {
		r.prev, r.cur = r.cur, 0
		r.idx = i
	}
	frac := float64(now%r.window) / float64(r.window)
	est := float64(r.prev)*(1-frac) + float64(r.cur)
	if est+float64(cost) > float64(r.limit) {
		return false
	}
	sum := uint64(r.cur) + uint64(cost)
	if sum > math.MaxUint32 {
		sum = math.MaxUint32
	}
	r.cur = uint32(sum)
	return true
}
