/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
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
	"time"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
)

//go:generate msgp

// IndexKey is the key under which the index will write itself to its associated cache
const IndexKey = "cache.index"

var indexLock = sync.Mutex{}

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
	cacheType      string                             `msg:"-"`
	config         config.CacheIndexConfig            `msg:"-"`
	bulkRemoveFunc func([]string)                     `msg:"-"`
	reapInterval   time.Duration                      `msg:"-"`
	flushInterval  time.Duration                      `msg:"-"`
	flushFunc      func(cacheKey string, data []byte) `msg:"-"`
	lastWrite      time.Time                          `msg:"-"`
}

// ToBytes returns a serialized byte slice representing the Index
func (idx *Index) ToBytes() []byte {
	bytes, _ := idx.MarshalMsg(nil)
	return bytes
}

// Object contains metadata about an item in the Cache
type Object struct {
	// Key represents the name of the Object and is the accessor in a hashed collection of Cache Objects
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

// ObjectFromBytes returns a deserialized Cache Object from a seralized byte slice
func ObjectFromBytes(data []byte) (*Object, error) {
	o := &Object{}
	_, err := o.UnmarshalMsg(data)
	return o, err
}

// NewIndex returns a new Index based on the provided inputs
func NewIndex(cacheName, cacheType string, indexData []byte, cfg config.CacheIndexConfig, bulkRemoveFunc func([]string), flushFunc func(cacheKey string, data []byte)) *Index {
	i := &Index{}

	if len(indexData) > 0 {
		i.UnmarshalMsg(indexData)
	} else {
		i.Objects = make(map[string]*Object)
	}

	i.name = cacheName
	i.cacheType = cacheType
	i.flushInterval = cfg.FlushInterval
	i.flushFunc = flushFunc
	i.reapInterval = cfg.ReapInterval
	i.bulkRemoveFunc = bulkRemoveFunc
	i.config = cfg

	if flushFunc != nil {
		if i.flushInterval > 0 {
			go i.flusher()
		} else {
			log.Warn("cache index flusher did not start", log.Pairs{"cacheName": i.name, "flushInterval": i.flushInterval})
		}
	}

	if i.reapInterval > 0 {
		go i.reaper()
	} else {
		log.Warn("cache reaper did not start", log.Pairs{"cacheName": i.name, "reapInterval": i.reapInterval})
	}

	metrics.CacheMaxObjects.WithLabelValues(cacheName, cacheType).Set(float64(cfg.MaxSizeObjects))
	metrics.CacheMaxBytes.WithLabelValues(cacheName, cacheType).Set(float64(cfg.MaxSizeBytes))

	return i
}

// UpdateObjectAccessTime updates the LastAccess for the object with the provided key
func (idx *Index) UpdateObjectAccessTime(key string) {
	indexLock.Lock()
	if _, ok := idx.Objects[key]; ok {
		idx.Objects[key].LastAccess = time.Now()
	}
	indexLock.Unlock()

}

// UpdateObjectTTL updates the Expiration for the object with the provided key
func (idx *Index) UpdateObjectTTL(key string, ttl time.Duration) {
	indexLock.Lock()
	if _, ok := idx.Objects[key]; ok {
		idx.Objects[key].Expiration = time.Now().Add(ttl)
	}
	indexLock.Unlock()
}

// UpdateObject writes or updates the Index Metadata for the provided Object
func (idx *Index) UpdateObject(obj *Object) {

	key := obj.Key
	if key == "" {
		return
	}

	indexLock.Lock()

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
		idx.CacheSize += o.Size - idx.Objects[key].Size
	} else {
		idx.CacheSize += obj.Size
		idx.ObjectCount++
	}

	cache.ObserveCacheSizeChange(idx.name, idx.cacheType, idx.CacheSize, idx.ObjectCount)

	idx.Objects[key] = obj
	indexLock.Unlock()
}

// RemoveObject removes an Object's Metadata from the Index
func (idx *Index) RemoveObject(key string) {
	indexLock.Lock()
	idx.lastWrite = time.Now()
	if o, ok := idx.Objects[key]; ok {
		idx.CacheSize -= o.Size
		idx.ObjectCount--
		cache.ObserveCacheOperation(idx.name, idx.cacheType, "del", "none", float64(o.Size))
		delete(idx.Objects, key)
		cache.ObserveCacheSizeChange(idx.name, idx.cacheType, idx.CacheSize, idx.ObjectCount)
	}
	indexLock.Unlock()
}

// RemoveObjects removes a list of Objects' Metadata from the Index
func (idx *Index) RemoveObjects(keys []string, noLock bool) {
	if !noLock {
		indexLock.Lock()
	}
	for _, key := range keys {
		if o, ok := idx.Objects[key]; ok {
			idx.CacheSize -= o.Size
			idx.ObjectCount--
			cache.ObserveCacheOperation(idx.name, idx.cacheType, "del", "none", float64(o.Size))
			delete(idx.Objects, key)
			cache.ObserveCacheSizeChange(idx.name, idx.cacheType, idx.CacheSize, idx.ObjectCount)
		}
	}
	idx.lastWrite = time.Now()
	if !noLock {
		indexLock.Unlock()
	}
}

// GetExpiration returns the cache index's expiration for the object of the given key
func (idx *Index) GetExpiration(cacheKey string) time.Time {
	indexLock.Lock()
	if o, ok := idx.Objects[cacheKey]; ok {
		indexLock.Unlock()
		return o.Expiration
	}
	indexLock.Unlock()
	return time.Time{}
}

// flusher periodically calls the cache's index flush func that writes the cache index to disk
func (idx *Index) flusher() {
	var lastFlush time.Time
	for {
		time.Sleep(idx.flushInterval)
		if idx.lastWrite.Before(lastFlush) {
			continue
		}
		idx.flushOnce()
		lastFlush = time.Now()
	}
}

func (idx *Index) flushOnce() {
	indexLock.Lock()
	bytes, err := idx.MarshalMsg(nil)
	indexLock.Unlock()
	if err != nil {
		log.Warn("unable to serialize index for flushing", log.Pairs{"cacheName": idx.name, "detail": err.Error()})
		return
	}
	idx.flushFunc(IndexKey, bytes)
}

// reaper continually iterates through the cache to find expired elements and removes them
func (idx *Index) reaper() {
	for {
		idx.reap()
		time.Sleep(idx.reapInterval)
	}
}

type objectsAtime []*Object

// reap makes a single iteration through the cache index to to find and remove expired elements
// and evict least-recently-accessed elements to maintain the Maximum allowed Cache Size
func (idx *Index) reap() {

	indexLock.Lock()
	defer indexLock.Unlock()

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
		cache.ObserveCacheEvent(idx.name, idx.cacheType, "eviction", "ttl")
		idx.bulkRemoveFunc(removals)
		idx.RemoveObjects(removals, true)
		cacheChanged = true
	}

	if ((idx.config.MaxSizeBytes > 0 && idx.CacheSize > idx.config.MaxSizeBytes) || (idx.config.MaxSizeObjects > 0 && idx.ObjectCount > idx.config.MaxSizeObjects)) && len(remainders) > 0 {

		var evictionType string
		if idx.config.MaxSizeBytes > 0 && idx.CacheSize > idx.config.MaxSizeBytes {
			evictionType = "size_bytes"
		} else if idx.config.MaxSizeObjects > 0 && idx.ObjectCount > idx.config.MaxSizeObjects {
			evictionType = "size_objects"
		} else {
			return
		}

		log.Debug("max cache size reached. evicting least-recently-accessed records",
			log.Pairs{
				"reason":         evictionType,
				"cacheSizeBytes": idx.CacheSize, "maxSizeBytes": idx.config.MaxSizeBytes,
				"cacheSizeObjects": idx.ObjectCount, "maxSizeObjects": idx.config.MaxSizeObjects,
			},
		)

		removals = make([]string, 0)

		sort.Sort(remainders)

		i := 0
		j := len(remainders)

		if evictionType == "size_bytes" {
			bytesNeeded := (idx.CacheSize - idx.config.MaxSizeBytes)
			if idx.config.MaxSizeBytes > idx.config.MaxSizeBackoffBytes {
				bytesNeeded += idx.config.MaxSizeBackoffBytes
			}
			bytesSelected := int64(0)
			for bytesSelected < bytesNeeded && i < j {
				removals = append(removals, remainders[i].Key)
				bytesSelected += remainders[i].Size
				i++
			}
		} else {
			objectsNeeded := (idx.ObjectCount - idx.config.MaxSizeObjects)
			if idx.config.MaxSizeObjects > idx.config.MaxSizeBackoffObjects {
				objectsNeeded += idx.config.MaxSizeBackoffObjects
			}
			objectsSelected := int64(0)
			for objectsSelected < objectsNeeded && i < j {
				removals = append(removals, remainders[i].Key)
				objectsSelected++
				i++
			}
		}

		if len(removals) > 0 {
			cache.ObserveCacheEvent(idx.name, idx.cacheType, "eviction", evictionType)
			idx.bulkRemoveFunc(removals)
			idx.RemoveObjects(removals, true)
			cacheChanged = true
		}

		log.Debug("size-based cache eviction exercise completed",
			log.Pairs{
				"reason":         evictionType,
				"cacheSizeBytes": idx.CacheSize, "maxSizeBytes": idx.config.MaxSizeBytes,
				"cacheSizeObjects": idx.ObjectCount, "maxSizeObjects": idx.config.MaxSizeObjects,
			})

	}
	if cacheChanged {
		idx.lastWrite = time.Now()
	}
}

// Len returns the length of an array of Prometheus model.Times
func (o objectsAtime) Len() int {
	return len(o)
}

// Less returns true if i comes before j
func (o objectsAtime) Less(i, j int) bool {
	return o[i].LastAccess.Before(o[j].LastAccess)
}

// Swap modifies an array by of Prometheus model.Times swapping the values in indexes i and j
func (o objectsAtime) Swap(i, j int) {
	o[i], o[j] = o[j], o[i]
}
