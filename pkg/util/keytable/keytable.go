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

// Package keytable is a bounded, goroutine-free table of values by 64-bit key, whose entries expire
// a fixed time after they are stored or once unread long enough.
package keytable

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultMaxEntries is how many entries a table holds when no bound is given.
	DefaultMaxEntries = 100_000
	// maxShards is how many independently locked parts a large table is split into
	maxShards = 64
	// minShardEntries is the fewest entries a shard is sized for, so a small table has fewer shards
	minShardEntries = 8
	// evictSample is how many of a full shard's entries are looked at to choose one to drop
	evictSample = 8
	// sweepEvery is how many new entries a shard takes between removals of its expired ones
	sweepEvery = 256
	// maxStampGap is the longest a read may leave an entry's last-read time stale, so a hot entry
	// is not written on every read
	maxStampGap = int64(time.Second)
)

// Options configure a table.
type Options struct {
	// TTL is how long after it is stored an entry expires; 0 is never.
	TTL time.Duration
	// Idle is how long an entry may go unread before it expires, to within a sixteenth of it or a
	// second, whichever is less; 0 is forever.
	Idle time.Duration
	// MaxEntries bounds the table (default DefaultMaxEntries). A new key in a full shard drops a
	// sample's expired entries, or else the sample's least recently read.
	MaxEntries int
	// RefuseWhenFull refuses a new key in a full part of the table unless the sample holds an
	// expired entry, for entries that must not be lost while they live, such as counts.
	RefuseWhenFull bool
}

// Table is a bounded table of values by key. It is safe for concurrent use.
type Table[V any] struct {
	shards    []shard[V]
	mask      uint64
	perShard  int
	ttl, idle int64
	stampGap  int64
	refuse    bool
	count     atomic.Int64
}

type shard[V any] struct {
	mtx     sync.RWMutex
	entries map[uint64]*entry[V]
	arrived uint32
	// keeps each shard's lock off the cache line of its neighbor's
	_ [64]byte
}

type entry[V any] struct {
	value   V
	expires int64
	read    atomic.Int64
}

// New returns an empty table.
func New[V any](o Options) *Table[V] {
	maxEntries := o.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	shards, mask := 1, uint64(0)
	for shards < maxShards && shards*2*minShardEntries <= maxEntries {
		shards, mask = shards*2, mask<<1|1
	}
	t := &Table[V]{
		shards:   make([]shard[V], shards),
		mask:     mask,
		perShard: (maxEntries + shards - 1) / shards,
		ttl:      int64(o.TTL),
		idle:     int64(o.Idle),
		stampGap: maxStampGap,
		refuse:   o.RefuseWhenFull,
	}
	if t.idle > 0 {
		t.stampGap = min(t.stampGap, t.idle/16)
	}
	for i := range t.shards {
		t.shards[i].entries = make(map[uint64]*entry[V])
	}
	return t
}

// Get returns the value stored under the key, unless it has expired, and marks it read. Now is
// the current time in nanoseconds, from the clock that every call to the table uses.
func (t *Table[V]) Get(key uint64, now int64) (V, bool) {
	s := &t.shards[key&t.mask]
	s.mtx.RLock()
	e := s.entries[key]
	s.mtx.RUnlock()
	if e == nil || t.expired(e, now) {
		var zero V
		return zero, false
	}
	t.markRead(e, now)
	return e.value, true
}

// Put stores the value under the key, replacing what was there, as newly stored and read. It
// reports false when the key is new and a table that refuses when full has no room for it.
func (t *Table[V]) Put(key uint64, v V, now int64) bool {
	s := &t.shards[key&t.mask]
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if _, ok := s.entries[key]; !ok && !t.admit(s, now) {
		return false
	}
	s.entries[key] = t.newEntry(v, now)
	return true
}

// GetOrPut returns the key's unexpired value, marking it read, or stores and returns v; false means
// no value and a full, refusing table. It write-locks, so try Get first.
func (t *Table[V]) GetOrPut(key uint64, v V, now int64) (V, bool) {
	s := &t.shards[key&t.mask]
	s.mtx.Lock()
	defer s.mtx.Unlock()
	e, ok := s.entries[key]
	if ok && !t.expired(e, now) {
		t.markRead(e, now)
		return e.value, true
	}
	if !ok && !t.admit(s, now) {
		var zero V
		return zero, false
	}
	s.entries[key] = t.newEntry(v, now)
	return v, true
}

func (t *Table[V]) newEntry(v V, now int64) *entry[V] {
	e := &entry[V]{value: v}
	if t.ttl > 0 {
		e.expires = now + t.ttl
	}
	e.read.Store(now)
	return e
}

// markRead stamps the entry read, unless it was stamped too recently to matter; the stamp only
// moves forward, so a read that resumes after a later one cannot shorten the entry's life
func (t *Table[V]) markRead(e *entry[V], now int64) {
	for {
		read := e.read.Load()
		if now-read < t.stampGap || e.read.CompareAndSwap(read, now) {
			return
		}
	}
}

// admit makes room for a new key in the write-locked shard, or reports none; every new key counts
// toward the next sweep, refused ones too, so a refusing table still reclaims.
func (t *Table[V]) admit(s *shard[V], now int64) bool {
	s.arrived++
	if s.arrived%sweepEvery == 0 {
		t.sweep(s, now)
	}
	if len(s.entries) >= t.perShard && !t.evict(s, now) {
		return false
	}
	t.count.Add(1)
	return true
}

// Len returns how many entries the table holds, expired ones not yet removed included.
func (t *Table[V]) Len() int {
	return int(t.count.Load())
}

// MaxEntries returns the most entries the table holds: its bound, rounded up to a multiple of
// how many parts it is split into.
func (t *Table[V]) MaxEntries() int {
	return t.perShard * len(t.shards)
}

func (t *Table[V]) expired(e *entry[V], now int64) bool {
	return (e.expires != 0 && now >= e.expires) || (t.idle > 0 && now-e.read.Load() >= t.idle)
}

// sweep removes the shard's expired entries; the caller holds its write lock
func (t *Table[V]) sweep(s *shard[V], now int64) {
	for k, e := range s.entries {
		if t.expired(e, now) {
			delete(s.entries, k)
			t.count.Add(-1)
		}
	}
}

// evict drops a sample's expired entries, else (unless refusing) its least recently read, and
// reports if any went; map range order makes the sample. The shard is write-locked.
func (t *Table[V]) evict(s *shard[V], now int64) bool {
	var (
		victim  uint64
		oldest  int64 = math.MaxInt64
		sampled int
		dropped bool
	)
	for k, e := range s.entries {
		if t.expired(e, now) {
			delete(s.entries, k)
			t.count.Add(-1)
			dropped = true
		} else if read := e.read.Load(); read < oldest {
			victim, oldest = k, read
		}
		if sampled++; sampled == evictSample {
			break
		}
	}
	if dropped {
		return true
	}
	if t.refuse {
		return false
	}
	delete(s.entries, victim)
	t.count.Add(-1)
	return true
}
