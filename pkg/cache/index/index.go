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

// Package index defines the Trickster Cache Index, which tracks what a cache that keeps
// whatever it is given holds, and enforces the cache's retention for it
package index

import (
	"sync/atomic"
	"time"
)

// IndexKey is a cache key reserved to the index, which no object may be stored under
const IndexKey = "cache.index"

// Object is what the index knows of one object in the cache. Times are Unix nanoseconds.
type Object struct {
	// Key is the cache key the object is stored under
	Key string

	size       atomic.Int64
	expiration atomic.Int64
	lastWrite  atomic.Int64
	lastAccess atomic.Int64
	// sweep is the last sweep of the cache to have found the object there
	sweep atomic.Uint64
	// evicting marks an object chosen for eviction, so that it is not chosen twice
	evicting atomic.Bool

	// transient marks an object that will be gone too soon for the index to persist it
	transient atomic.Bool

	// the rest belong to the object's shard, and are guarded by its mutex

	// slot is where the object sits among its shard's objects, and is negative when it does not
	slot int
	// due is the expiry bucket the object is in, and dueSlot its place there
	due     int64
	dueSlot int
	// counted marks an object whose size is part of the index's totals
	counted bool
	// removed marks an object that has left the index
	removed bool
}

// zero for the zero Time, which the atomic fields hold for no time at all
func unixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromUnixNano(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// Size returns the length in bytes of the object's content
func (o *Object) Size() int64 {
	return o.size.Load()
}

// Expiration returns when the object expires, which is the zero Time when it never does
func (o *Object) Expiration() time.Time {
	return fromUnixNano(o.expiration.Load())
}

// LastWrite returns when the object was last written
func (o *Object) LastWrite() time.Time {
	return fromUnixNano(o.lastWrite.Load())
}

// LastAccess returns when the object was last written or retrieved
func (o *Object) LastAccess() time.Time {
	return fromUnixNano(o.lastAccess.Load())
}

func (o *Object) expired(now int64) bool {
	e := o.expiration.Load()
	return e != 0 && e <= now
}
