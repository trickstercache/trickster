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

package index

import (
	"container/heap"
	"hash/maphash"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/util/weak/compat"
)

const (
	// shardCount is how many parts the index's bookkeeping is split into, so that
	// writers rarely wait on one another. It must be a power of two.
	shardCount = 64
	shardMask  = shardCount - 1

	noSlot = -1

	// an expiry within fineBucketWindow is bucketed to the second, and a later one to
	// the minute, which keeps the buckets of long-lived objects few
	fineBucketWindow = int64(time.Minute)
	fineBucket       = int64(time.Second)
	coarseBucket     = int64(time.Minute)
)

var shardSeed = maphash.MakeSeed()

// a min-heap of the times of a shard's expiry buckets
type bucketTimes []int64

func (b bucketTimes) Len() int           { return len(b) }
func (b bucketTimes) Less(i, j int) bool { return b[i] < b[j] }
func (b bucketTimes) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }
func (b *bucketTimes) Push(x any)        { *b = append(*b, x.(int64)) }

func (b *bucketTimes) Pop() any {
	old := *b
	x := old[len(old)-1]
	*b = old[:len(old)-1]
	return x
}

// the objects that can be sampled for eviction, and those that expire, bucketed by when
type shard struct {
	mu      sync.Mutex
	objects []*Object
	buckets map[int64][]*Object
	times   bucketTimes
	// stale counts the times in the heap whose buckets were emptied; the heap is rebuilt
	// without them once they are most of it
	stale int
}

type shards [shardCount]shard

func (s *shards) of(key string) *shard {
	return &s[maphash.String(shardSeed, key)&shardMask]
}

// a bucket is never due before its objects expire
func bucketOf(expiration, now int64) int64 {
	width := coarseBucket
	if expiration-now <= fineBucketWindow {
		width = fineBucket
	}
	return (expiration + width - 1) / width * width
}

// returns the size to count for the object, and false for one that left the index before it
// could be inserted
func (s *shard) insert(o *Object, now int64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.removed {
		return 0, false
	}
	o.counted = true
	o.slot = len(s.objects)
	s.objects = append(s.objects, o)
	s.schedule(o, o.expiration.Load(), now)
	return o.size.Load(), true
}

// returns the change to count in the object's size, and false for one that has left the index
func (s *shard) update(o *Object, size, expiration, now int64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.removed {
		return 0, false
	}
	delta := size - o.size.Swap(size)
	// written again, the object is no longer the one that was chosen for eviction
	o.evicting.Store(false)
	o.expiration.Store(expiration)
	o.lastWrite.Store(now)
	o.lastAccess.Store(now)
	s.schedule(o, expiration, now)
	if !o.counted {
		// whoever is inserting the object counts the size it finds
		delta = 0
	}
	return delta, true
}

// returns the size that was counted for the object
func (s *shard) remove(o *Object) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeLocked(o)
}

func (s *shard) removeLocked(o *Object) int64 {
	if o.removed {
		return 0
	}
	o.removed = true
	s.unschedule(o)
	if o.slot != noSlot {
		last := len(s.objects) - 1
		moved := s.objects[last]
		s.objects[o.slot] = moved
		moved.slot = o.slot
		s.objects[last] = nil
		s.objects = s.objects[:last]
		o.slot = noSlot
	}
	if !o.counted {
		return 0
	}
	return o.size.Load()
}

// the bucket's last object fills the place that is left; the shard's mutex must be held
func (s *shard) unschedule(o *Object) {
	if o.due == 0 {
		return
	}
	bucket := s.buckets[o.due]
	last := len(bucket) - 1
	moved := bucket[last]
	bucket[o.dueSlot] = moved
	moved.dueSlot = o.dueSlot
	bucket[last] = nil
	if last == 0 {
		delete(s.buckets, o.due)
		s.stale++
		if s.stale > len(s.times)/2 {
			s.rebuildTimes()
		}
	} else {
		s.buckets[o.due] = bucket[:last]
	}
	o.due = 0
}

// the heap is made anew from the buckets that still have objects
func (s *shard) rebuildTimes() {
	s.times = s.times[:0]
	for due := range s.buckets {
		s.times = append(s.times, due)
	}
	heap.Init(&s.times)
	s.stale = 0
}

// the shard's mutex must be held
func (s *shard) schedule(o *Object, expiration, now int64) {
	if expiration == 0 {
		s.unschedule(o)
		return
	}
	due := bucketOf(expiration, now)
	if o.due == due {
		return
	}
	s.unschedule(o)
	if s.buckets == nil {
		s.buckets = make(map[int64][]*Object)
	}
	bucket, ok := s.buckets[due]
	if !ok {
		heap.Push(&s.times, due)
	}
	o.due, o.dueSlot = due, len(bucket)
	s.buckets[due] = append(bucket, o)
}

// for an object taken as due that is not expired, which goes back to a bucket
func (s *shard) reschedule(o *Object, now int64) {
	s.mu.Lock()
	if !o.removed {
		s.schedule(o, o.expiration.Load(), now)
	}
	s.mu.Unlock()
}

// appends the objects of every bucket whose time has come, and drops those buckets
func (s *shard) takeDue(dst []*Object, now int64) []*Object {
	s.mu.Lock()
	for len(s.times) > 0 && s.times[0] <= now {
		due := heap.Pop(&s.times).(int64)
		bucket, ok := s.buckets[due]
		if !ok {
			// a time whose bucket was emptied before it came
			s.stale--
			continue
		}
		for _, o := range bucket {
			o.due = 0
			if !o.removed {
				dst = append(dst, o)
			}
		}
		delete(s.buckets, due)
	}
	s.mu.Unlock()
	return dst
}

func (s *shard) sample() *Object {
	var o *Object
	s.mu.Lock()
	if n := len(s.objects); n > 0 {
		o = s.objects[compat.IntN(n)]
	}
	s.mu.Unlock()
	return o
}

func (s *shard) appendAll(dst []*Object) []*Object {
	s.mu.Lock()
	dst = append(dst, s.objects...)
	s.mu.Unlock()
	return dst
}

func (s *shard) clear() {
	s.mu.Lock()
	for _, o := range s.objects {
		o.removed, o.slot = true, noSlot
	}
	s.objects, s.buckets, s.times, s.stale = nil, nil, nil, 0
	s.mu.Unlock()
}
