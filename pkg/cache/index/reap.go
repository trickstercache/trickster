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
	"context"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/compat"
)

const (
	// pressureInterval is how often the reaper looks for a cache that has gone over its size
	pressureInterval = time.Second
	// evictionSamples is how many objects are looked at, at random, to evict the one
	// among them that was used longest ago
	evictionSamples = 32
	// evictionBatch is how many objects are removed from the cache at a time
	evictionBatch = 256
	// exactEvictionLimit is the count of objects up to which a cache is ranked whole, and
	// the one used longest ago evicted, which costs little for so few
	exactEvictionLimit = 1024
)

func (idx *IndexedClient) reaper(ctx context.Context) {
	defer idx.wg.Done()
	safego.Run(idx.workerPanicHandler(workerReaper, &idx.reaperExited), func() {
		last := time.Now()
	REAPER:
		for {
			ri := time.Duration(idx.options.Load().ReapInterval)
			select {
			case <-ctx.Done():
				break REAPER
			case <-time.After(min(ri, pressureInterval)):
				// between its intervals, the reaper acts only for a cache that has gone over its size
				if time.Since(last) < ri && !idx.pressure.Load() {
					continue
				}
			case <-idx.forceReap:
			}
			last = time.Now()
			idx.reap()
			signal(idx.hasReaped)
		}
		idx.reaperExited.Store(true)
	})
}

func (idx *IndexedClient) reap() {
	idx.reapAt(time.Now().UnixNano())
}

func (idx *IndexedClient) reapAt(now int64) {
	idx.pressure.Store(false)
	// whatever is written from here on is not what this pass chooses to remove
	since := time.Now()
	var due []*Object
	for i := range idx.shards {
		due = idx.shards[i].takeDue(due, now)
	}
	removals := make([]string, 0, min(len(due), evictionBatch))
	for _, o := range due {
		if !o.expired(now) {
			// written again since it was due, to expire at a time its bucket also covers
			idx.shards.of(o.Key).reschedule(o, now)
			continue
		}
		removals = append(removals, o.Key)
		if len(removals) == evictionBatch {
			idx.evict(reasonTTL, since, removals)
			removals = removals[:0]
		}
	}
	if len(removals) > 0 {
		idx.evict(reasonTTL, since, removals)
	}
	idx.evictOverage(idx.options.Load(), since)
}

// how many bytes and objects are to be evicted, and why
func (idx *IndexedClient) overage(o *options.Options) (bytes, objects int64, reason string) {
	if size := idx.cacheSize.Load(); o.MaxSizeBytes > 0 && size > o.MaxSizeBytes {
		bytes, reason = size-o.MaxSizeBytes, reasonBytes
		if o.MaxSizeBytes > o.MaxSizeBackoffBytes {
			bytes += o.MaxSizeBackoffBytes
		}
	}
	if count := idx.objectCount.Load(); o.MaxSizeObjects > 0 && count > o.MaxSizeObjects {
		objects = count - o.MaxSizeObjects
		if o.MaxSizeObjects > o.MaxSizeBackoffObjects {
			objects += o.MaxSizeBackoffObjects
		}
		if reason == "" {
			reason = reasonObjects
		}
	}
	if idx.ico.MinFreeBytes > 0 && idx.free != nil {
		if free, ok := idx.free.FreeBytes(); ok && free < idx.ico.MinFreeBytes {
			if short := idx.ico.MinFreeBytes - free + o.MaxSizeBackoffBytes; short > bytes {
				bytes = short
			}
			if reason == "" {
				reason = reasonFreeSpace
			}
		}
	}
	return bytes, objects, reason
}

func byLastAccess(a, b *Object) int {
	switch x, y := a.lastAccess.Load(), b.lastAccess.Load(); {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// the object used longest ago of a few taken at random; nil when none is found, as in a cache
// of few objects
func (idx *IndexedClient) victim() *Object {
	var oldest *Object
	for range evictionSamples {
		o := idx.shards[compat.IntN(shardCount)].sample()
		if o == nil || o.evicting.Load() {
			continue
		}
		if oldest == nil || byLastAccess(o, oldest) < 0 {
			oldest = o
		}
	}
	return oldest
}

// those used longest ago go first, until the cache is within its size
func (idx *IndexedClient) evictOverage(opts *options.Options, since time.Time) {
	bytes, objects, reason := idx.overage(opts)
	if reason == "" {
		return
	}
	logger.Debug("max cache size reached. evicting least-recently-accessed records",
		logging.Pairs{
			"reason":         reason,
			"cacheSizeBytes": idx.cacheSize.Load(), "maxSizeBytes": opts.MaxSizeBytes,
			"cacheSizeObjects": idx.objectCount.Load(), "maxSizeObjects": opts.MaxSizeObjects,
		})
	var ranked []*Object
	if idx.objectCount.Load() <= exactEvictionLimit {
		idx.each(func(o *Object) bool {
			ranked = append(ranked, o)
			return true
		})
		slices.SortFunc(ranked, byLastAccess)
	}
	removals := make([]string, 0, evictionBatch)
	for bytes > 0 || objects > 0 {
		var o *Object
		if ranked != nil {
			if len(ranked) == 0 {
				break
			}
			o, ranked = ranked[0], ranked[1:]
		} else if o = idx.victim(); o == nil {
			break
		}
		o.evicting.Store(true)
		bytes -= o.size.Load()
		objects--
		removals = append(removals, o.Key)
		if len(removals) == evictionBatch {
			idx.evict(reason, since, removals)
			removals = removals[:0]
		}
	}
	if len(removals) > 0 {
		idx.evict(reason, since, removals)
	}
}
