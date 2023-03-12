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

// Package index defines the Trickster Cache Index
package index

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	gm "github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

//go:generate msgp

// IndexKey is the key under which the index will write itself to its associated cache
const IndexKey = "cache.index"

// Index maintains metadata about a Cache when Retention enforcement is managed internally,
// like memory or bbolt. It is not used for independently managed caches like Redis.
type Index struct {
	// CacheSize represents the size of the cache in bytes
	CacheSize int64 `msg:"cache_size"`
	// ObjectCount represents the count of objects in the Cache
	ObjectCount int64 `msg:"object_count"`
	// Objects is a map of Objects in the Cache
	Objects map[string]*Object `msg:"objects"`

	name           string                             `msg:"-"`
	cacheProvider  string                             `msg:"-"`
	options        *options.Options                   `msg:"-"`
	bulkRemoveFunc func([]string)                     `msg:"-"`
	flushFunc      func(cacheKey string, data []byte) `msg:"-"`
	lastWrite      time.Time                          `msg:"-"`

	isClosing     bool
	flusherExited bool
	reaperExited  bool

	mtx sync.Mutex
}

// Close is called to signal the index to shut down any subroutines
func (idx *Index) Close() {
	idx.isClosing = true
}

// ToBytes returns a serialized byte slice representing the Index
func (idx *Index) ToBytes() []byte {
	bytes, _ := idx.MarshalMsg(nil)
	return bytes
}

// Object contains metadata about an item in the Cache
type Object struct {
	// Key represents the name of the Object and is the
	// accessor in a hashed collection of Cache Objects
	Key string `msg:"key"`
	// Expiration represents the time that the Object expires from Cache
	Expiration time.Time `msg:"expiration"`
	// LastWrite is the time the object was last Written
	LastWrite time.Time `msg:"lastwrite"`
	// LastAccess is the time the object was last Accessed
	LastAccess time.Time `msg:"lastaccess"`
	// Size the size of the Object in bytes
	Size int64 `msg:"size"`
	// Value is the value of the Object stored in the Cache
	// It is used by Caches but not by the Index
	Value []byte `msg:"value,omitempty"`
	// DirectValue is an interface value for storing objects by reference to a memory cache
	// Since we'd never recover a memory cache index from memory on startup, no need to msgpk
	ReferenceValue cache.ReferenceObject `msg:"-"`
}

// ToBytes returns a serialized byte slice representing the Object
func (o *Object) ToBytes() []byte {
	bytes, _ := o.MarshalMsg(nil)
	return bytes
}

// ObjectFromBytes returns a deserialized Cache Object from a serialized byte slice
func ObjectFromBytes(data []byte) (*Object, error) {
	o := &Object{}
	_, err := o.UnmarshalMsg(data)
	return o, err
}

// NewIndex returns a new Index based on the provided inputs
func NewIndex(cacheName, cacheProvider string, indexData []byte, o *options.Options,
	bulkRemoveFunc func([]string), flushFunc func(cacheKey string, data []byte),
	logger interface{}) *Index {
	i := &Index{}

	if len(indexData) > 0 {
		i.UnmarshalMsg(indexData)
	} else {
		i.Objects = make(map[string]*Object)
	}

	i.name = cacheName
	i.cacheProvider = cacheProvider
	i.flushFunc = flushFunc
	i.bulkRemoveFunc = bulkRemoveFunc
	i.options = o

	if flushFunc != nil {
		if o.FlushInterval > 0 {
			go i.flusher(logger)
		} else {
			tl.Warn(logger, "cache index flusher did not start",
				tl.Pairs{"cacheName": i.name, "flushInterval": o.FlushInterval})
		}
	}

	if o.ReapInterval > 0 {
		go i.reaper(logger)
	} else {
		tl.Warn(logger, "cache reaper did not start",
			tl.Pairs{"cacheName": i.name, "reapInterval": o.ReapInterval})
	}

	gm.CacheMaxObjects.WithLabelValues(cacheName, cacheProvider).Set(float64(o.MaxSizeObjects))
	gm.CacheMaxBytes.WithLabelValues(cacheName, cacheProvider).Set(float64(o.MaxSizeBytes))

	return i
}

// UpdateOptions updates the existing Index with a new Options reference
func (idx *Index) UpdateOptions(o *options.Options) {
	idx.mtx.Lock()
	idx.options = o
	idx.mtx.Unlock()
}

// UpdateObjectAccessTime updates the LastAccess for the object with the provided key
func (idx *Index) UpdateObjectAccessTime(key string) {
	idx.mtx.Lock()
	if _, ok := idx.Objects[key]; ok {
		idx.Objects[key].LastAccess = time.Now()
	}
	idx.mtx.Unlock()

}

// UpdateObjectTTL updates the Expiration for the object with the provided key
func (idx *Index) UpdateObjectTTL(key string, ttl time.Duration) {
	idx.mtx.Lock()
	if _, ok := idx.Objects[key]; ok {
		idx.Objects[key].Expiration = time.Now().Add(ttl)
	}
	idx.mtx.Unlock()
}

// UpdateObject writes or updates the Index Metadata for the provided Object
func (idx *Index) UpdateObject(obj *Object) {

	key := obj.Key
	if key == "" {
		return
	}

	idx.mtx.Lock()

	idx.lastWrite = time.Now()

	if obj.ReferenceValue != nil {
		obj.Size = int64(obj.ReferenceValue.Size())
	} else {
		obj.Size = int64(len(obj.Value))
	}
	obj.Value = nil
	obj.LastAccess = time.Now()
	obj.LastWrite = obj.LastAccess

	if o, ok := idx.Objects[key]; ok {
		atomic.AddInt64(&idx.CacheSize, obj.Size-o.Size)
	} else {
		atomic.AddInt64(&idx.CacheSize, obj.Size)
		atomic.AddInt64(&idx.ObjectCount, 1)
	}

	metrics.ObserveCacheSizeChange(idx.name, idx.cacheProvider, idx.CacheSize, idx.ObjectCount)

	idx.Objects[key] = obj
	idx.mtx.Unlock()
}

// RemoveObject removes an Object's Metadata from the Index
func (idx *Index) RemoveObject(key string) {
	idx.mtx.Lock()
	idx.lastWrite = time.Now()
	if o, ok := idx.Objects[key]; ok {
		atomic.AddInt64(&idx.CacheSize, -o.Size)
		atomic.AddInt64(&idx.ObjectCount, -1)

		metrics.ObserveCacheOperation(idx.name, idx.cacheProvider, "del", "none", float64(o.Size))

		delete(idx.Objects, key)
		metrics.ObserveCacheSizeChange(idx.name, idx.cacheProvider, idx.CacheSize, idx.ObjectCount)
	}
	idx.mtx.Unlock()
}

// RemoveObjects removes a list of Objects' Metadata from the Index
func (idx *Index) RemoveObjects(keys []string, noLock bool) {
	if !noLock {
		idx.mtx.Lock()
	}
	for _, key := range keys {
		if o, ok := idx.Objects[key]; ok {
			atomic.AddInt64(&idx.CacheSize, -o.Size)
			atomic.AddInt64(&idx.ObjectCount, -1)
			metrics.ObserveCacheOperation(idx.name, idx.cacheProvider, "del", "none", float64(o.Size))
			delete(idx.Objects, key)
			metrics.ObserveCacheSizeChange(idx.name, idx.cacheProvider, idx.CacheSize, idx.ObjectCount)
		}
	}
	idx.lastWrite = time.Now()
	if !noLock {
		idx.mtx.Unlock()
	}
}

// GetExpiration returns the cache index's expiration for the object of the given key
func (idx *Index) GetExpiration(cacheKey string) time.Time {
	idx.mtx.Lock()
	if o, ok := idx.Objects[cacheKey]; ok {
		idx.mtx.Unlock()
		return o.Expiration
	}
	idx.mtx.Unlock()
	return time.Time{}
}

// flusher periodically calls the cache's index flush func that writes the cache index to disk
func (idx *Index) flusher(logger interface{}) {
	var lastFlush time.Time
	for !idx.isClosing {
		time.Sleep(idx.options.FlushInterval)
		if idx.lastWrite.Before(lastFlush) {
			continue
		}
		idx.flushOnce(logger)
		lastFlush = time.Now()
	}
	idx.flusherExited = true
}

func (idx *Index) flushOnce(logger interface{}) {
	idx.mtx.Lock()
	bytes, err := idx.MarshalMsg(nil)
	idx.mtx.Unlock()
	if err != nil {
		tl.Warn(logger, "unable to serialize index for flushing",
			tl.Pairs{"cacheName": idx.name, "detail": err.Error()})
		return
	}
	idx.flushFunc(IndexKey, bytes)
}

// reaper continually iterates through the cache to find expired elements and removes them
func (idx *Index) reaper(logger interface{}) {
	for !idx.isClosing {
		idx.reap(logger)
		time.Sleep(idx.options.ReapInterval)
	}
	idx.reaperExited = true
}

type objectsAtime []*Object

// reap makes a single iteration through the cache index to to find and remove expired elements
// and evict least-recently-accessed elements to maintain the Maximum allowed Cache Size
func (idx *Index) reap(logger interface{}) {

	idx.mtx.Lock()
	defer idx.mtx.Unlock()

	removals := make([]string, 0)
	remainders := make(objectsAtime, 0, idx.ObjectCount)

	var cacheChanged bool

	now := time.Now()

	for _, o := range idx.Objects {
		if o.Key == IndexKey {
			continue
		}
		if o.Expiration.Before(now) && !o.Expiration.IsZero() {
			removals = append(removals, o.Key)
		} else {
			remainders = append(remainders, o)
		}
	}

	if len(removals) > 0 {
		metrics.ObserveCacheEvent(idx.name, idx.cacheProvider, "eviction", "ttl")
		go idx.bulkRemoveFunc(removals)
		idx.RemoveObjects(removals, true)
		cacheChanged = true
	}

	if ((idx.options.MaxSizeBytes > 0 && idx.CacheSize > idx.options.MaxSizeBytes) ||
		(idx.options.MaxSizeObjects > 0 && idx.ObjectCount > idx.options.MaxSizeObjects)) &&
		len(remainders) > 0 {

		var evictionType string
		if idx.options.MaxSizeBytes > 0 && idx.CacheSize > idx.options.MaxSizeBytes {
			evictionType = "size_bytes"
		} else if idx.options.MaxSizeObjects > 0 && idx.ObjectCount > idx.options.MaxSizeObjects {
			evictionType = "size_objects"
		} else {
			return
		}

		tl.Debug(logger,
			"max cache size reached. evicting least-recently-accessed records",
			tl.Pairs{
				"reason":         evictionType,
				"cacheSizeBytes": idx.CacheSize, "maxSizeBytes": idx.options.MaxSizeBytes,
				"cacheSizeObjects": idx.ObjectCount, "maxSizeObjects": idx.options.MaxSizeObjects,
			},
		)

		removals = make([]string, 0)

		sort.Sort(remainders)

		i := 0
		j := len(remainders)

		if evictionType == "size_bytes" {
			bytesNeeded := (idx.CacheSize - idx.options.MaxSizeBytes)
			if idx.options.MaxSizeBytes > idx.options.MaxSizeBackoffBytes {
				bytesNeeded += idx.options.MaxSizeBackoffBytes
			}
			bytesSelected := int64(0)
			for bytesSelected < bytesNeeded && i < j {
				removals = append(removals, remainders[i].Key)
				bytesSelected += remainders[i].Size
				i++
			}
		} else {
			objectsNeeded := (idx.ObjectCount - idx.options.MaxSizeObjects)
			if idx.options.MaxSizeObjects > idx.options.MaxSizeBackoffObjects {
				objectsNeeded += idx.options.MaxSizeBackoffObjects
			}
			objectsSelected := int64(0)
			for objectsSelected < objectsNeeded && i < j {
				removals = append(removals, remainders[i].Key)
				objectsSelected++
				i++
			}
		}

		if len(removals) > 0 {
			metrics.ObserveCacheEvent(idx.name, idx.cacheProvider, "eviction", evictionType)
			go idx.bulkRemoveFunc(removals)
			idx.RemoveObjects(removals, true)
			cacheChanged = true
		}

		tl.Debug(logger, "size-based cache eviction exercise completed",
			tl.Pairs{
				"reason":         evictionType,
				"cacheSizeBytes": idx.CacheSize, "maxSizeBytes": idx.options.MaxSizeBytes,
				"cacheSizeObjects": idx.ObjectCount, "maxSizeObjects": idx.options.MaxSizeObjects,
			})

	}
	if cacheChanged {
		idx.lastWrite = time.Now()
	}
}

// Len returns the number of elements in the subject slice
func (o objectsAtime) Len() int {
	return len(o)
}

// Less returns true if i comes before j
func (o objectsAtime) Less(i, j int) bool {
	return o[i].LastAccess.Before(o[j].LastAccess)
}

// Swap modifies the subject slice by swapping the values in indexes i and j
func (o objectsAtime) Swap(i, j int) {
	o[i], o[j] = o[j], o[i]
}
