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
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tinylib/msgp/msgp"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	gm "github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/util/atomicx"
)

//go:generate go tool msgp

// IndexKey is the key under which the index will write itself to its associated cache
const IndexKey = "cache.index"

// IndexObjects are the set of objects held by an IndexCache, represented as a map for encoding and decoding purposes
type IndexObjects map[string]*Object

//msgp:ignore IndexCache

// IndexCache is a cache of Index Objects, backed by a sync.Map, that supports msgp encoding and decoding
type IndexCache struct {
	sync.Map
	keys atomic.Int64
}

func (i *IndexCache) Store(key, value any) {
	i.Map.Store(key, value)
	i.keys.Add(1)
}

func (i *IndexCache) Delete(key any) {
	i.Map.Delete(key)
	i.keys.Add(-1)
}

func (i *IndexCache) FromIndexObjects(in IndexObjects) {
	for k, v := range in {
		i.Store(k, v)
	}
}

func (i *IndexCache) ToIndexObjects() IndexObjects {
	out := make(IndexObjects, i.keys.Load())
	i.Range(func(k, v any) bool {
		out[k.(string)] = v.(*Object)
		return true
	})
	return out
}

func (i *IndexCache) EncodeMsg(en *msgp.Writer) (err error) {
	return i.ToIndexObjects().EncodeMsg(en)
}

func (i *IndexCache) DecodeMsg(dc *msgp.Reader) (err error) {
	objects := &IndexObjects{}
	if err := objects.DecodeMsg(dc); err != nil {
		return err
	}
	i.FromIndexObjects(*objects)
	return
}

func (i *IndexCache) MarshalMsg(b []byte) (o []byte, err error) {
	return i.ToIndexObjects().MarshalMsg(b)
}

func (i *IndexCache) UnmarshalMsg(bts []byte) (o []byte, err error) {
	objects := &IndexObjects{}
	o, err = objects.UnmarshalMsg(bts)
	if err != nil {
		return o, err
	}
	i.FromIndexObjects(*objects)
	return
}

func (i *IndexCache) Msgsize() (s int) {
	return i.ToIndexObjects().Msgsize()
}

// Index maintains metadata about a Cache when Retention enforcement is managed internally,
// like memory or bbolt. It is not used for independently managed caches like Redis.
type Index struct {
	// CacheSize represents the size of the cache in bytes
	CacheSize int64 `msg:"cache_size"`
	// ObjectCount represents the count of objects in the Cache
	ObjectCount int64 `msg:"object_count"`
	// Objects is a map of Objects in the Cache
	Objects IndexCache `msg:"objects"`

	name           string                             `msg:"-"`
	cacheProvider  string                             `msg:"-"`
	options        atomic.Value                       `msg:"-"`
	bulkRemoveFunc func([]string)                     `msg:"-"`
	flushFunc      func(cacheKey string, data []byte) `msg:"-"`
	lastWrite      atomicx.AtomicTime                 `msg:"-"`

	isClosing     atomic.Bool
	cancel        context.CancelFunc
	flusherExited atomic.Bool
	reaperExited  atomic.Bool
}

// Close is called to signal the index to shut down any subroutines
func (idx *Index) Close() {
	idx.cancel()
	idx.isClosing.Store(true)
}

// ToBytes returns a serialized byte slice representing the Index
func (idx *Index) ToBytes() []byte {
	bytes, _ := idx.MarshalMsg(nil)
	return bytes
}

func NewObject(key string, expiration time.Time, value []byte) *Object {
	return &Object{
		Key:        key,
		Expiration: atomicx.NewAtomicTime(expiration),
		LastWrite:  atomicx.NewAtomicTime(time.Time{}),
		LastAccess: atomicx.NewAtomicTime(time.Time{}),
		Value:      value,
		Size:       int64(len(value)),
	}
}

func NewObjectFromReference(key string, expiration time.Time, value cache.ReferenceObject) *Object {
	return &Object{
		Key:            key,
		Expiration:     atomicx.NewAtomicTime(expiration),
		LastWrite:      atomicx.NewAtomicTime(time.Time{}),
		LastAccess:     atomicx.NewAtomicTime(time.Time{}),
		ReferenceValue: value,
		Size:           int64(value.Size()),
	}
}

// Object contains metadata about an item in the Cache
type Object struct {
	// Key represents the name of the Object and is the
	// accessor in a hashed collection of Cache Objects
	Key string `msg:"key"`
	// Expiration represents the time that the Object expires from Cache
	Expiration *atomicx.AtomicTime `msg:"expiration"`
	// LastWrite is the time the object was last Written
	LastWrite *atomicx.AtomicTime `msg:"lastwrite"`
	// LastAccess is the time the object was last Accessed
	LastAccess *atomicx.AtomicTime `msg:"lastaccess"`
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
func NewIndex(cacheName, cacheProvider string, indexData []byte,
	o *options.Options, bulkRemoveFunc func([]string),
	flushFunc func(cacheKey string, data []byte)) *Index {
	i := &Index{}

	if len(indexData) > 0 {
		i.UnmarshalMsg(indexData)
	} else {
		i.Objects = IndexCache{}
	}

	i.name = cacheName
	i.cacheProvider = cacheProvider
	i.flushFunc = flushFunc
	i.bulkRemoveFunc = bulkRemoveFunc
	i.options.Store(o)
	ctx, cancel := context.WithCancel(context.Background())
	i.cancel = cancel
	if flushFunc != nil {
		if o.FlushInterval > 0 {
			go i.flusher(ctx)
		} else {
			logger.Warn("cache index flusher did not start",
				logging.Pairs{"cacheName": i.name, "flushInterval": o.FlushInterval})
		}
	}

	if o.ReapInterval > 0 {
		go i.reaper(ctx)
	} else {
		logger.Warn("cache reaper did not start",
			logging.Pairs{"cacheName": i.name, "reapInterval": o.ReapInterval})
	}

	gm.CacheMaxObjects.WithLabelValues(cacheName, cacheProvider).Set(float64(o.MaxSizeObjects))
	gm.CacheMaxBytes.WithLabelValues(cacheName, cacheProvider).Set(float64(o.MaxSizeBytes))

	return i
}

// UpdateOptions updates the existing Index with a new Options reference
func (idx *Index) UpdateOptions(o *options.Options) {
	idx.options.Store(o)
}

// UpdateObjectAccessTime updates the LastAccess for the object with the provided key
func (idx *Index) UpdateObjectAccessTime(key string) {
	v, ok := idx.Objects.Load(key)
	if !ok {
		return
	}
	updated := v.(*Object)
	updated.LastAccess.StoreTime(time.Now())
}

// UpdateObjectTTL updates the Expiration for the object with the provided key
func (idx *Index) UpdateObjectTTL(key string, ttl time.Duration) {
	v, ok := idx.Objects.Load(key)
	if !ok {
		return
	}
	updated := v.(*Object)
	updated.Expiration.StoreTime(time.Now().Add(ttl))

}

// UpdateObject writes or updates the Index Metadata for the provided Object
func (idx *Index) UpdateObject(obj *Object) {

	key := obj.Key
	if key == "" {
		return
	}

	if obj.ReferenceValue != nil {
		obj.Size = int64(obj.ReferenceValue.Size())
	} else {
		obj.Size = int64(len(obj.Value))
	}
	obj.Value = nil
	now := time.Now()
	obj.LastAccess = atomicx.NewAtomicTime(now)
	obj.LastWrite = atomicx.NewAtomicTime(now)
	if obj.Expiration == nil {
		obj.Expiration = atomicx.NewAtomicTime(time.Time{})
	}

	var size, count int64
	if o, ok := idx.Objects.Load(key); ok {
		oldObj := o.(*Object)
		size = atomic.AddInt64(&idx.CacheSize, obj.Size-oldObj.Size)
		count = atomic.LoadInt64(&idx.ObjectCount)
	} else {
		size = atomic.AddInt64(&idx.CacheSize, obj.Size)
		count = atomic.AddInt64(&idx.ObjectCount, 1)
	}

	metrics.ObserveCacheSizeChange(idx.name, idx.cacheProvider, size, count)
	idx.lastWrite.StoreTime(time.Now())
	idx.Objects.Store(key, obj)
}

// RemoveObject removes an Object's Metadata from the Index
func (idx *Index) RemoveObject(key string) {
	if o, ok := idx.Objects.Load(key); ok {
		obj := o.(*Object)
		size := atomic.AddInt64(&idx.CacheSize, -obj.Size)
		count := atomic.AddInt64(&idx.ObjectCount, -1)

		metrics.ObserveCacheOperation(idx.name, idx.cacheProvider, "del", "none", float64(obj.Size))

		idx.Objects.Delete(key)
		metrics.ObserveCacheSizeChange(idx.name, idx.cacheProvider, size, count)
	}
	idx.lastWrite.StoreTime(time.Now())
}

// RemoveObjects removes a list of Objects' Metadata from the Index
func (idx *Index) RemoveObjects(keys []string, noLock bool) {
	for _, key := range keys {
		if o, ok := idx.Objects.Load(key); ok {
			obj := o.(*Object)
			size := atomic.AddInt64(&idx.CacheSize, -obj.Size)
			count := atomic.AddInt64(&idx.ObjectCount, -1)
			metrics.ObserveCacheOperation(idx.name, idx.cacheProvider, "del", "none", float64(obj.Size))
			idx.Objects.Delete(key)
			metrics.ObserveCacheSizeChange(idx.name, idx.cacheProvider, size, count)
		}
	}
	idx.lastWrite.StoreTime(time.Now())
}

// GetExpiration returns the cache index's expiration for the object of the given key
func (idx *Index) GetExpiration(cacheKey string) time.Time {
	if o, ok := idx.Objects.Load(cacheKey); ok {
		obj := o.(*Object)
		return obj.Expiration.LoadTime()
	}
	return time.Time{}
}

// flusher periodically calls the cache's index flush func that writes the cache index to disk
func (idx *Index) flusher(ctx context.Context) {
	var lastFlush time.Time
FLUSHER:
	for {
		fi := idx.options.Load().(*options.Options).FlushInterval
		select {
		case <-ctx.Done():
			break FLUSHER
		case <-time.After(fi):
			if idx.lastWrite.LoadTime().Before(lastFlush) {
				continue
			}
			idx.flushOnce()
			lastFlush = time.Now()
		}
	}
	idx.flusherExited.Store(true)
}

func (idx *Index) flushOnce() {
	bytes, err := idx.MarshalMsg(nil)
	if err != nil {
		logger.Warn("unable to serialize index for flushing",
			logging.Pairs{"cacheName": idx.name, "detail": err.Error()})
		return
	}
	idx.flushFunc(IndexKey, bytes)
}

// reaper continually iterates through the cache to find expired elements and removes them
func (idx *Index) reaper(ctx context.Context) {
REAPER:
	for {
		ri := idx.options.Load().(*options.Options).ReapInterval
		select {
		case <-ctx.Done():
			break REAPER
		case <-time.After(ri):
			idx.reap()
		}
	}
	idx.reaperExited.Store(true)
}

type objectsAtime []*Object

// reap makes a single iteration through the cache index to to find and remove expired elements
// and evict least-recently-accessed elements to maintain the Maximum allowed Cache Size
func (idx *Index) reap() {

	cacheSize := atomic.LoadInt64(&idx.CacheSize)
	objectCount := atomic.LoadInt64(&idx.ObjectCount)
	removals := make([]string, 0)
	remainders := make(objectsAtime, 0, cacheSize)

	var cacheChanged bool

	now := time.Now()

	idx.Objects.Range(func(key, value any) bool {
		o := value.(*Object)
		if o.Key == IndexKey {
			return true
		}
		if e := o.Expiration.LoadTime(); e.Before(now) && !e.IsZero() {
			removals = append(removals, o.Key)
		} else {
			remainders = append(remainders, o)
		}
		return true
	})

	if len(removals) > 0 {
		metrics.ObserveCacheEvent(idx.name, idx.cacheProvider, "eviction", "ttl")
		go idx.bulkRemoveFunc(removals)
		idx.RemoveObjects(removals, true)
		cacheChanged = true
	}

	opts := idx.options.Load().(*options.Options)

	if ((opts.MaxSizeBytes > 0 && cacheSize > opts.MaxSizeBytes) ||
		(opts.MaxSizeObjects > 0 && objectCount > opts.MaxSizeObjects)) &&
		len(remainders) > 0 {

		var evictionType string
		switch {
		case opts.MaxSizeBytes > 0 && cacheSize > opts.MaxSizeBytes:
			evictionType = "size_bytes"
		case opts.MaxSizeObjects > 0 && objectCount > opts.MaxSizeObjects:
			evictionType = "size_objects"
		default:
			return
		}

		logger.Debug(
			"max cache size reached. evicting least-recently-accessed records",
			logging.Pairs{
				"reason":         evictionType,
				"cacheSizeBytes": cacheSize, "maxSizeBytes": opts.MaxSizeBytes,
				"cacheSizeObjects": objectCount, "maxSizeObjects": opts.MaxSizeObjects,
			},
		)

		removals = make([]string, 0)

		sort.Sort(remainders)

		i := 0
		j := len(remainders)

		if evictionType == "size_bytes" {
			bytesNeeded := (cacheSize - opts.MaxSizeBytes)
			if opts.MaxSizeBytes > opts.MaxSizeBackoffBytes {
				bytesNeeded += opts.MaxSizeBackoffBytes
			}
			bytesSelected := int64(0)
			for bytesSelected < bytesNeeded && i < j {
				removals = append(removals, remainders[i].Key)
				bytesSelected += remainders[i].Size
				i++
			}
		} else {
			objectsNeeded := (objectCount - opts.MaxSizeObjects)
			if opts.MaxSizeObjects > opts.MaxSizeBackoffObjects {
				objectsNeeded += opts.MaxSizeBackoffObjects
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

		logger.Debug("size-based cache eviction exercise completed",
			logging.Pairs{
				"reason":         evictionType,
				"cacheSizeBytes": idx.CacheSize, "maxSizeBytes": opts.MaxSizeBytes,
				"cacheSizeObjects": idx.ObjectCount, "maxSizeObjects": opts.MaxSizeObjects,
			})

	}
	if cacheChanged {
		idx.lastWrite.StoreTime(time.Now())
	}
}

// Len returns the number of elements in the subject slice
func (o objectsAtime) Len() int {
	return len(o)
}

// Less returns true if i comes before j
func (o objectsAtime) Less(i, j int) bool {
	return o[i].LastAccess.LoadTime().Before(o[j].LastAccess.LoadTime())
}

// Swap modifies the subject slice by swapping the values in indexes i and j
func (o objectsAtime) Swap(i, j int) {
	o[i], o[j] = o[j], o[i]
}
