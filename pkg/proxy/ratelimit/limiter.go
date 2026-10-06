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

import "time"

// MissingKey is what an event does when a key part is absent.
type MissingKey uint8

const (
	// MissingExempt does not count an event that lacks a key part.
	MissingExempt MissingKey = iota
	// MissingShared counts events that lack the same parts in one bucket.
	MissingShared
)

// MaxKeysAction is what a new key receives when its shard is full.
type MaxKeysAction uint8

const (
	// MaxKeysAllow lets the event through without a bucket.
	MaxKeysAllow MaxKeysAction = iota
	// MaxKeysReject treats the event as limited and asks the caller to wait one window.
	MaxKeysReject
)

// oneBucketKey is the only key used when every event shares one bucket.
const oneBucketKey uint64 = 1

// Config builds a limiter that owns its store. A reload that must share buckets uses Lookup.
type Config struct {
	Limit     uint32
	Window    time.Duration
	MaxKeys   int
	Missing   MissingKey
	OnFull    MaxKeysAction
	OneBucket bool
}

// Limiter applies missing-key and full-table policy around a store. Copies may share a store,
// and each copy keeps its own policy so a reload can change it without discarding counts.
type Limiter struct {
	store     *tableStore
	limit     uint32
	window    time.Duration
	missing   MissingKey
	onFull    MaxKeysAction
	oneBucket bool
}

// New returns a limiter with a private store.
func New(c Config) *Limiter {
	return &Limiter{
		store:     newStore(c.Limit, c.Window, c.MaxKeys),
		limit:     c.Limit,
		window:    c.Window,
		missing:   c.Missing,
		onFull:    c.OnFull,
		oneBucket: c.OneBucket,
	}
}

// Take judges one event. ok is false when a key part was missing. A passing event is counted.
// A limited event is not.
func (l *Limiter) Take(key uint64, ok bool, now int64, cost uint32) Decision {
	return l.judge(key, ok, now, cost, false)
}

// Count judges one event and counts it even when it is over the estimate, once, under one lock.
func (l *Limiter) Count(key uint64, ok bool, now int64, cost uint32) Decision {
	return l.judge(key, ok, now, cost, true)
}

// Charge adds cost without judging. A missing key under the exempt policy is not stored.
func (l *Limiter) Charge(key uint64, ok bool, now int64, cost uint32) {
	key, count := l.bind(key, ok)
	if !count {
		return
	}
	l.store.Charge(key, now, cost)
}

// Len is how many buckets the store holds, expired ones not yet swept included.
func (l *Limiter) Len() int {
	if l == nil || l.store == nil {
		return 0
	}
	return l.store.Len()
}

func (l *Limiter) judge(key uint64, ok bool, now int64, cost uint32, countLimited bool) Decision {
	key, count := l.bind(key, ok)
	if !count {
		return Decision{
			Result: ResultExempt, Allowed: true, Remaining: l.limit, RetryKnown: true,
			Reset: AgeOut(0, now, int64(l.window)),
		}
	}
	var d Decision
	if countLimited {
		d = l.store.Count(key, now, cost)
	} else {
		d = l.store.Take(key, now, cost)
	}
	if d.Result != ResultFull {
		return d
	}
	reset := AgeOut(0, now, int64(l.window))
	if l.onFull == MaxKeysAllow {
		return Decision{
			Result: ResultFull, Allowed: true, Remaining: l.limit, RetryKnown: true, Reset: reset,
		}
	}
	d.Allowed = false
	d.RetryAfter = l.window
	d.RetryKnown = true
	d.Reset = reset
	return d
}

// bind maps an event onto a stored key. The bool is false when the event is exempt.
func (l *Limiter) bind(key uint64, ok bool) (uint64, bool) {
	if l.oneBucket {
		return oneBucketKey, true
	}
	if ok {
		return key, true
	}
	if l.missing == MissingShared {
		return key, true
	}
	return 0, false
}
