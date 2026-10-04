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

package keytable

import (
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

const sec = int64(time.Second)

type path struct {
	hashes [2]uint64
	depth  uint8
}

// held counts the entries that the shards' maps hold
func held[V any](t *Table[V]) int {
	var n int
	for i := range t.shards {
		t.shards[i].mtx.RLock()
		n += len(t.shards[i].entries)
		t.shards[i].mtx.RUnlock()
	}
	return n
}

func TestNewSizesTheTable(t *testing.T) {
	for _, tc := range []struct{ max, shards, bound int }{
		{0, 64, 100_032},
		{-1, 64, 100_032},
		{10, 1, 10},
		{15, 1, 15},
		{16, 2, 16},
		{100, 8, 104},
		{1000, 64, 1024},
	} {
		tbl := New[int](Options{MaxEntries: tc.max})
		if len(tbl.shards) != tc.shards || tbl.MaxEntries() != tc.bound {
			t.Errorf("max_entries %d: %d shards holding %d; want %d holding %d",
				tc.max, len(tbl.shards), tbl.MaxEntries(), tc.shards, tc.bound)
		}
	}
}

func TestGetAndPut(t *testing.T) {
	tbl := New[path](Options{})
	if _, ok := tbl.Get(1, 0); ok {
		t.Error("an empty table found a value")
	}
	want := path{hashes: [2]uint64{7, 9}, depth: 2}
	tbl.Put(1, want, 0)
	if got, ok := tbl.Get(1, 0); !ok || got != want {
		t.Errorf("Get = %+v, %v", got, ok)
	}
	if _, ok := tbl.Get(2, 0); ok {
		t.Error("another key found the value")
	}
	replaced := path{hashes: [2]uint64{8}, depth: 1}
	tbl.Put(1, replaced, 0)
	if got, _ := tbl.Get(1, 0); got != replaced || tbl.Len() != 1 {
		t.Errorf("after a replacement Get = %+v and Len = %d", got, tbl.Len())
	}
	// without a ttl or an idle timeout nothing expires
	if _, ok := tbl.Get(1, 1<<62); !ok {
		t.Error("an entry expired with no ttl or idle timeout")
	}
}

func TestTTL(t *testing.T) {
	tbl := New[int](Options{TTL: 10 * time.Second})
	tbl.Put(1, 1, 0)
	for now := int64(0); now < 10*sec; now += sec {
		if _, ok := tbl.Get(1, now); !ok {
			t.Fatalf("expired at %ds", now/sec)
		}
	}
	if _, ok := tbl.Get(1, 10*sec); ok {
		t.Error("reading an entry extended its ttl")
	}
	// storing again starts a new lifetime
	tbl.Put(1, 2, 10*sec)
	if v, ok := tbl.Get(1, 19*sec); !ok || v != 2 {
		t.Error("a replaced entry kept its predecessor's ttl")
	}
}

func TestIdle(t *testing.T) {
	tbl := New[int](Options{Idle: 10 * time.Second})
	tbl.Put(1, 1, 0)
	if _, ok := tbl.Get(1, 9*sec); !ok {
		t.Fatal("expired before its idle timeout")
	}
	// the read at 9s keeps it alive until 19s
	if _, ok := tbl.Get(1, 18*sec); !ok {
		t.Fatal("a read did not refresh the idle timeout")
	}
	if _, ok := tbl.Get(1, 27*sec); !ok {
		t.Fatal("the second read did not refresh it")
	}
	if _, ok := tbl.Get(1, 38*sec); ok {
		t.Error("outlived its idle timeout")
	}
	// an expired entry stays expired, even when read again later
	if _, ok := tbl.Get(1, 39*sec); ok {
		t.Error("a read revived an expired entry")
	}
}

// Reads close together do not each write the entry, so a hot entry's last-read time may lag by up
// to a sixteenth of the idle timeout, and never more.
func TestIdleStampGap(t *testing.T) {
	idle := 16 * time.Second
	tbl := New[int](Options{Idle: idle})
	if tbl.stampGap != sec {
		t.Fatalf("stamp gap = %v", time.Duration(tbl.stampGap))
	}
	tbl.Put(1, 1, 0)
	tbl.Get(1, sec-1)
	if _, ok := tbl.Get(1, int64(idle)); ok {
		t.Error("a read within the stamp gap refreshed the entry")
	}
	tbl.Put(1, 1, 0)
	tbl.Get(1, sec)
	if _, ok := tbl.Get(1, int64(idle)); !ok {
		t.Error("a read at the stamp gap did not refresh the entry")
	}
	if short := New[int](Options{Idle: 160 * time.Millisecond}); short.stampGap != int64(10*time.Millisecond) {
		t.Errorf("a short idle timeout's stamp gap = %v", time.Duration(short.stampGap))
	}
}

func TestReadStampOnlyMovesForward(t *testing.T) {
	tbl := New[int](Options{Idle: time.Hour})
	for round := range 200 {
		e := tbl.newEntry(0, 0)
		var (
			start sync.WaitGroup
			done  sync.WaitGroup
		)
		start.Add(1)
		for g := range int64(16) {
			done.Go(func() {
				start.Wait()
				tbl.markRead(e, (g+1)*tbl.stampGap)
			})
		}
		start.Done()
		done.Wait()
		if got := e.read.Load(); got != 16*tbl.stampGap {
			t.Fatalf("round %d: the stamp ended at %v, not the latest read", round, time.Duration(got))
		}
	}
}

func TestTTLAndIdle(t *testing.T) {
	tbl := New[int](Options{TTL: 20 * time.Second, Idle: 10 * time.Second})
	tbl.Put(1, 1, 0)
	for _, now := range []int64{8, 16} {
		if _, ok := tbl.Get(1, now*sec); !ok {
			t.Fatalf("expired at %ds", now)
		}
	}
	if _, ok := tbl.Get(1, 20*sec); ok {
		t.Error("reads kept an entry past its ttl")
	}
}

// A full table drops the least recently read entry of its sample; with a shard no bigger than the
// sample, that is the least recently read of all.
func TestEvictsTheLeastRecentlyRead(t *testing.T) {
	tbl := New[int](Options{MaxEntries: evictSample})
	for k := range uint64(evictSample) {
		tbl.Put(k, int(k), int64(k)*sec)
	}
	tbl.Get(0, 100*sec)
	tbl.Put(100, 100, 101*sec)
	if _, ok := tbl.Get(1, 101*sec); ok {
		t.Error("the least recently read entry was kept")
	}
	for _, k := range []uint64{0, 2, 100} {
		if _, ok := tbl.Get(k, 101*sec); !ok {
			t.Errorf("entry %d was dropped", k)
		}
	}
	if tbl.Len() != evictSample || held(tbl) != evictSample {
		t.Errorf("Len = %d, held = %d", tbl.Len(), held(tbl))
	}
	// replacing an entry in a full table drops nothing
	tbl.Put(100, 101, 102*sec)
	if tbl.Len() != evictSample {
		t.Errorf("a replacement changed Len to %d", tbl.Len())
	}
}

func TestEvictsExpiredEntriesFirst(t *testing.T) {
	tbl := New[int](Options{MaxEntries: evictSample, TTL: 10 * time.Second})
	for k := range uint64(evictSample) {
		// the first two are stored early enough to expire
		at := 20 * sec
		if k < 2 {
			at = 0
		}
		tbl.Put(k, int(k), at)
	}
	tbl.Put(100, 100, 25*sec)
	if tbl.Len() != evictSample-1 || held(tbl) != evictSample-1 {
		t.Errorf("Len = %d, held = %d; want both expired entries gone and nothing else", tbl.Len(), held(tbl))
	}
	for k := uint64(2); k < evictSample; k++ {
		if _, ok := tbl.Get(k, 25*sec); !ok {
			t.Errorf("live entry %d was dropped while expired ones remained", k)
		}
	}
}

func TestSweepRemovesExpiredEntries(t *testing.T) {
	tbl := New[int](Options{TTL: time.Second})
	shards := uint64(len(tbl.shards))
	// every key a multiple of the shard count lands in the first shard
	for i := range uint64(200) {
		tbl.Put(i*shards, 0, 0)
	}
	for i := uint64(200); i < sweepEvery-1; i++ {
		tbl.Put(i*shards, 0, 2*sec)
	}
	if tbl.Len() != sweepEvery-1 {
		t.Fatalf("Len = %d before the sweep", tbl.Len())
	}
	tbl.Put(sweepEvery*shards, 0, 2*sec)
	if want := sweepEvery - 200; tbl.Len() != want || held(tbl) != want {
		t.Errorf("Len = %d, held = %d after the sweep; want %d", tbl.Len(), held(tbl), want)
	}
}

func TestGetOrPut(t *testing.T) {
	tbl := New[int](Options{TTL: 10 * time.Second, Idle: 4 * time.Second})
	if v, ok := tbl.GetOrPut(1, 1, 0); !ok || v != 1 {
		t.Fatalf("a new key returned %d, %v", v, ok)
	}
	if v, ok := tbl.GetOrPut(1, 2, 3*sec); !ok || v != 1 {
		t.Errorf("a stored key returned %d, %v; want the stored value", v, ok)
	}
	// that read at 3s keeps it alive past its first idle deadline
	if v, ok := tbl.Get(1, 6*sec); !ok || v != 1 {
		t.Error("GetOrPut did not mark the entry read")
	}
	// once it expires, the given value takes its place, with a lifetime of its own
	if v, ok := tbl.GetOrPut(1, 3, 10*sec); !ok || v != 3 {
		t.Errorf("an expired key returned %d, %v", v, ok)
	}
	if v, ok := tbl.Get(1, 13*sec); !ok || v != 3 || tbl.Len() != 1 {
		t.Errorf("after replacing an expired entry Get = %d, %v and Len = %d", v, ok, tbl.Len())
	}
}

// every caller that races to create a key's value gets the one that was stored
func TestGetOrPutStoresOneValue(t *testing.T) {
	tbl := New[*int](Options{})
	var (
		wg  sync.WaitGroup
		mtx sync.Mutex
		got = make(map[*int]bool)
	)
	for range 16 {
		wg.Go(func() {
			v, ok := tbl.GetOrPut(1, new(int), 0)
			mtx.Lock()
			got[v] = ok
			mtx.Unlock()
		})
	}
	wg.Wait()
	if len(got) != 1 {
		t.Errorf("racing callers got %d different values", len(got))
	}
}

// A table that refuses when full never drops a live entry for a new key.
func TestRefuseWhenFull(t *testing.T) {
	tbl := New[int](Options{MaxEntries: evictSample, TTL: 10 * time.Second, RefuseWhenFull: true})
	for k := range uint64(evictSample) {
		if !tbl.Put(k, int(k), int64(k)*sec) {
			t.Fatalf("key %d was refused with room left", k)
		}
	}
	if tbl.Put(100, 100, 9*sec) {
		t.Error("a full table took a new key")
	}
	if v, ok := tbl.GetOrPut(100, 100, 9*sec); ok || v != 0 {
		t.Errorf("GetOrPut on a full table returned %d, %v", v, ok)
	}
	for k := range uint64(evictSample) {
		if _, ok := tbl.Get(k, 9*sec); !ok {
			t.Errorf("live entry %d was dropped", k)
		}
	}
	// a key already stored can still be replaced and read
	if !tbl.Put(3, 33, 9*sec) {
		t.Error("replacing a stored key was refused")
	}
	if v, ok := tbl.GetOrPut(3, 0, 9*sec); !ok || v != 33 {
		t.Errorf("GetOrPut of a stored key in a full table returned %d, %v", v, ok)
	}
	// an expired entry makes room: the first key, stored at 0s, is gone at 10s
	if v, ok := tbl.GetOrPut(100, 100, 10*sec); !ok || v != 100 {
		t.Errorf("a new key was refused when an entry had expired: %d, %v", v, ok)
	}
	if tbl.Len() != evictSample || held(tbl) != evictSample {
		t.Errorf("Len = %d, held = %d", tbl.Len(), held(tbl))
	}
}

// refused keys count toward a shard's sweep, so a full table flooded with new keys still
// reclaims expired entries that its samples miss
func TestRefusedKeysLeadToASweep(t *testing.T) {
	tbl := New[int](Options{MaxEntries: 15, TTL: 10 * time.Second, RefuseWhenFull: true})
	for k := range uint64(15) {
		tbl.Put(k, 0, 0)
	}
	// at 5s nothing has expired, so every new key is refused
	for k := uint64(100); k < 100+sweepEvery-15-1; k++ {
		if tbl.Put(k, 0, 5*sec) {
			t.Fatal("a full table with nothing expired took a new key")
		}
	}
	if tbl.Len() != 15 {
		t.Fatalf("Len = %d before the sweep", tbl.Len())
	}
	// the next arrival sweeps the shard, at a time when all fifteen have expired
	if !tbl.Put(1000, 1, 20*sec) || tbl.Len() != 1 || held(tbl) != 1 {
		t.Errorf("after the sweep Len = %d, held = %d", tbl.Len(), held(tbl))
	}
}

func TestConcurrentUse(t *testing.T) {
	tbl := New[path](Options{MaxEntries: 1000, TTL: time.Hour, Idle: time.Minute})
	var wg sync.WaitGroup
	for g := range uint64(8) {
		wg.Go(func() {
			for i := range uint64(5000) {
				key := (i*31 + g) % 3000
				now := int64(i) * int64(time.Millisecond)
				if v, ok := tbl.Get(key, now); ok && v.hashes[0] != key {
					t.Errorf("key %d holds %+v", key, v)
					return
				}
				tbl.Put(key, path{hashes: [2]uint64{key}, depth: 1}, now)
			}
		})
	}
	wg.Wait()
	if n := tbl.Len(); n != held(tbl) || n > tbl.MaxEntries() {
		t.Errorf("Len = %d, held = %d, bound %d", n, held(tbl), tbl.MaxEntries())
	}
}

func TestRunsNoGoroutine(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	tbl := New[int](Options{TTL: time.Second, Idle: time.Second})
	for k := range uint64(1000) {
		tbl.Put(k, 0, 0)
	}
}

func TestGetDoesNotAllocate(t *testing.T) {
	tbl := New[path](Options{Idle: time.Minute})
	tbl.Put(1, path{depth: 1}, 0)
	var now int64
	if n := testing.AllocsPerRun(100, func() { now += sec; tbl.Get(1, now) }); n != 0 {
		t.Errorf("Get allocated %v times", n)
	}
}

func BenchmarkGet(b *testing.B) {
	tbl := New[path](Options{TTL: time.Hour, Idle: time.Minute})
	for k := range uint64(10_000) {
		tbl.Put(k, path{hashes: [2]uint64{k}, depth: 1}, 0)
	}
	b.Run("serial", func(b *testing.B) {
		var k uint64
		for b.Loop() {
			k = (k + 7919) % 10_000
			tbl.Get(k, 1)
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			var k uint64
			for pb.Next() {
				k = (k + 7919) % 10_000
				tbl.Get(k, 1)
			}
		})
	})
}

func BenchmarkPut(b *testing.B) {
	tbl := New[path](Options{TTL: time.Hour, MaxEntries: 10_000})
	var k uint64
	for b.Loop() {
		k++
		tbl.Put(k, path{hashes: [2]uint64{k}, depth: 1}, int64(k))
	}
}
