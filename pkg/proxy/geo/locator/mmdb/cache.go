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

package mmdb

import (
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
)

const (
	cacheShards        = 64
	cacheShardCapacity = 1024
)

type recordCache struct {
	shards [cacheShards]cacheShard
}

type cacheShard struct {
	mtx     sync.RWMutex
	records map[uintptr]geo.Location
}

func (c *recordCache) get(offset uintptr) (geo.Location, bool) {
	s := &c.shards[offset%cacheShards]
	s.mtx.RLock()
	loc, ok := s.records[offset]
	s.mtx.RUnlock()
	return loc, ok
}

func (c *recordCache) put(offset uintptr, loc geo.Location) {
	// the cache is bounded: a full shard stores nothing more, and its records are decoded on each lookup
	s := &c.shards[offset%cacheShards]
	s.mtx.Lock()
	if s.records == nil {
		s.records = make(map[uintptr]geo.Location, cacheShardCapacity)
	}
	if len(s.records) < cacheShardCapacity {
		s.records[offset] = loc
	}
	s.mtx.Unlock()
}
