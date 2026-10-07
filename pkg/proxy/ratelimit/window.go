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
	"time"
)

// bucket is one key's estimator. The table records last use for expiry, so the bucket does not.
// prev and cur are the previous and current window counts. idx is the window they belong to.
type bucket struct {
	mu    sync.Mutex
	idx   uint64
	prev  uint32
	cur   uint32
	last  int64
	ready bool
}

type snap struct {
	idx       uint64
	prev, cur uint32
}

func (b *bucket) judge(now, window int64, limit, cost uint32, countLimited bool) Decision {
	b.mu.Lock()
	defer b.mu.Unlock()
	// The common case is another event in the current window with nothing carried forward.
	// Integer math there matches the weighted estimate, and it skips the refusal search.
	if !countLimited {
		if d, ok := b.allowFast(now, window, limit, cost); ok {
			return d
		}
	}
	now = b.roll(now, window)
	frac := fraction(now, window)
	est := estimate(b.prev, b.cur, frac)
	if over(est, cost, limit) {
		return b.refused(now, window, limit, cost, est, countLimited)
	}
	b.cur = satAdd(b.cur, cost)
	return Decision{
		Result:     ResultAllowed,
		Allowed:    true,
		Remaining:  remainingOf(limit, estimate(b.prev, b.cur, frac)),
		RetryKnown: true,
		Reset:      AgeOut(b.cur, now, window),
	}
}

// retry is the wait until cost could pass, after the counts have rolled forward, without adding it.
func (b *bucket) retry(now, window int64, limit, cost uint32) (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now = b.roll(now, window)
	delay, ok := earliest(snap{idx: b.idx, prev: b.prev, cur: b.cur}, now, window, limit, cost)
	return time.Duration(delay), ok
}

func (b *bucket) charge(now, window int64, cost uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.roll(now, window)
	b.cur = satAdd(b.cur, cost)
}

func (b *bucket) refused(now, window int64, limit, cost uint32, est float64, countLimited bool) Decision {
	d := Decision{
		Result:    ResultLimited,
		Remaining: remainingOf(limit, est),
		Reset:     AgeOut(b.cur, now, window),
	}
	if uint64(cost) > uint64(limit) {
		return d
	}
	s := snap{idx: b.idx, prev: b.prev, cur: b.cur}
	if countLimited {
		b.cur = satAdd(b.cur, cost)
		s.cur = b.cur
		d.Result = ResultCounted
		d.Allowed = true
		d.Remaining = remainingOf(limit, estimate(b.prev, b.cur, fraction(now, window)))
		d.Reset = AgeOut(b.cur, now, window)
	}
	delay, ok := earliest(s, now, window, limit, cost)
	d.RetryAfter = time.Duration(delay)
	d.RetryKnown = ok
	return d
}

// allowFast counts an event that stays in the current window with an empty previous window.
// The weighted estimate is then just the current count, so no floating point is required.
func (b *bucket) allowFast(now, window int64, limit, cost uint32) (Decision, bool) {
	if !b.ready || now < b.last || window <= 0 || b.prev != 0 || uint64(now/window) != b.idx {
		return Decision{}, false
	}
	sum := uint64(b.cur) + uint64(cost)
	if sum > uint64(limit) {
		return Decision{}, false
	}
	b.cur = uint32(sum)
	b.last = now
	return Decision{
		Result: ResultAllowed, Allowed: true, Remaining: limit - b.cur, RetryKnown: true,
		Reset: AgeOut(b.cur, now, window),
	}, true
}

// AgeOut is how long the previous and current window counts stay in the estimate. A current
// count needs the rest of this window and one more; an empty current count leaves at the boundary.
func AgeOut(cur uint32, now, window int64) time.Duration {
	if window <= 0 {
		return time.Second
	}
	elapsed := now % window
	if elapsed < 0 {
		elapsed += window
	}
	until := window - elapsed
	if until <= 0 {
		until = window
	}
	if cur > 0 {
		until += window
	}
	return time.Duration(until)
}

// roll moves the counts into now's window. An older sample is clamped to the last processed
// time, so a later lock holder cannot roll the counts backwards.
func (b *bucket) roll(now, window int64) int64 {
	if window <= 0 {
		return now
	}
	if now < b.last {
		now = b.last
	}
	i := uint64(now / window)
	switch {
	case !b.ready:
		b.idx = i
		b.ready = true
	case i > b.idx+1:
		b.prev, b.cur = 0, 0
		b.idx = i
	case i == b.idx+1:
		b.prev, b.cur = b.cur, 0
		b.idx = i
	}
	b.last = now
	return now
}

func fraction(now, window int64) float64 {
	if window <= 0 {
		return 0
	}
	return float64(now%window) / float64(window)
}

func estimate(prev, cur uint32, frac float64) float64 {
	return float64(prev)*(1-frac) + float64(cur)
}

func over(est float64, cost, limit uint32) bool {
	return est+float64(cost) > float64(limit)
}

func remainingOf(limit uint32, est float64) uint32 {
	if est >= float64(limit) {
		return 0
	}
	return uint32(math.Floor(float64(limit) - est))
}

func satAdd(cur, cost uint32) uint32 {
	sum := uint64(cur) + uint64(cost)
	if sum > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(sum)
}

// earliest is the soonest a fresh event of this cost could pass. It is at most two windows
// ahead when the cost is within the limit, because both counts have rolled away by then.
func earliest(s snap, now, window int64, limit, cost uint32) (int64, bool) {
	if uint64(cost) > uint64(limit) {
		return 0, false
	}
	if admits(s, now, window, limit, cost) {
		return 0, true
	}
	hi := window
	if window <= math.MaxInt64/2 {
		hi = window * 2
	}
	if !admits(s, now+hi, window, limit, cost) {
		return 0, false
	}
	lo := int64(0)
	for lo+1 < hi {
		mid := lo + (hi-lo)/2
		if admits(s, now+mid, window, limit, cost) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi, true
}

func admits(s snap, t, window int64, limit, cost uint32) bool {
	prev, cur, frac := s.at(t, window)
	return !over(estimate(prev, cur, frac), cost, limit)
}

func (s snap) at(t, window int64) (prev, cur uint32, frac float64) {
	i := uint64(t / window)
	switch {
	case i <= s.idx:
		prev, cur = s.prev, s.cur
	case i == s.idx+1:
		prev, cur = s.cur, 0
	default:
		prev, cur = 0, 0
	}
	return prev, cur, fraction(t, window)
}
