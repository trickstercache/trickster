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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/util/keytable"
)

// Store is the counted state of one limiter. Take judges and counts an event that passes.
// Charge adds a cost without judging. Count judges and, when the event would be limited,
// charges it once under the same lock.
type Store interface {
	Take(key uint64, now int64, cost uint32) Decision
	Charge(key uint64, now int64, cost uint32)
	Count(key uint64, now int64, cost uint32) Decision
	Len() int
}

var _ Store = (*tableStore)(nil)

type tableStore struct {
	table  *keytable.Table[*bucket]
	limit  uint32
	window int64
}

func newStore(limit uint32, window time.Duration, maxKeys int) *tableStore {
	idle := 2*window + time.Second
	return &tableStore{
		limit:  limit,
		window: int64(window),
		table: keytable.New[*bucket](keytable.Options{
			Idle:           idle,
			MaxEntries:     maxKeys,
			RefuseWhenFull: true,
		}),
	}
}

func (s *tableStore) Take(key uint64, now int64, cost uint32) Decision {
	return s.decide(key, now, cost, false)
}

func (s *tableStore) Count(key uint64, now int64, cost uint32) Decision {
	return s.decide(key, now, cost, true)
}

// Charge adds cost to a key that already has a bucket. A new key the full table refuses to
// create is left uncounted: there is nowhere to put the cost. A non-positive window is a no-op.
func (s *tableStore) Charge(key uint64, now int64, cost uint32) {
	if s.window <= 0 {
		return
	}
	b, ok := s.bucket(key, now)
	if !ok {
		return
	}
	b.charge(now, s.window, cost)
}

func (s *tableStore) Len() int {
	return s.table.Len()
}

// Retry is the wait until cost could pass for a key that already has a bucket, without counting
// it. found is false when the key is not stored.
func (s *tableStore) Retry(key uint64, now int64, cost uint32) (d time.Duration, known, found bool) {
	if s.window <= 0 {
		return 0, true, false
	}
	b, ok := s.table.Get(key, now)
	if !ok || b == nil {
		return 0, false, false
	}
	d, known = b.retry(now, s.window, s.limit, cost)
	return d, known, true
}

func (s *tableStore) decide(key uint64, now int64, cost uint32, countLimited bool) Decision {
	// A zero window cannot be divided into indexes. Count nothing rather than panic; validation
	// rejects that configuration before a limiter is attached.
	if s.window <= 0 {
		return Decision{Result: ResultAllowed, Allowed: true, Remaining: s.limit, RetryKnown: true}
	}
	b, ok := s.bucket(key, now)
	if !ok {
		return Decision{Result: ResultFull, RetryAfter: time.Duration(s.window), RetryKnown: true}
	}
	return b.judge(now, s.window, s.limit, cost, countLimited)
}

// bucket returns the shared counter for key. A miss stores one bucket and keeps whichever
// racer stored first, so concurrent first events cannot split the count.
func (s *tableStore) bucket(key uint64, now int64) (*bucket, bool) {
	if b, ok := s.table.Get(key, now); ok && b != nil {
		return b, true
	}
	got, ok := s.table.GetOrPut(key, &bucket{}, now)
	if !ok || got == nil {
		return nil, false
	}
	return got, true
}
