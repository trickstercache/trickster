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
	"errors"
	"sort"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	gm "github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/util/atomicx"
)

//go:generate go tool msgp

var (
	// IndexedClient implements the cache.Client and cache.MemoryCache interfaces
	_ cache.Client      = &IndexedClient{}
	_ cache.MemoryCache = &IndexedClient{}
)

var (
	ErrIndexInvalidCacheKey = errors.New("cannot store index")
	ErrInvalidCacheBackend  = errors.New("invalid cache backend for reference access")
)

// IndexedClientOptions modify an IndexedClient's behavior.
type IndexedClientOptions struct {
	NeedsFlushInterval bool
	NeedsReapInterval  bool
}

func NewIndexedClient(
	cacheName, cacheProvider string,
	indexData []byte,
	o *options.Options,
	client cache.Client,
	opts ...func(*IndexedClientOptions),
) *IndexedClient {
	ctx, cancel := context.WithCancel(context.Background())
	idx := &IndexedClient{
		Client:        client,
		name:          cacheName,
		cacheProvider: cacheProvider,
		cancel:        cancel,
	}
	idx.options.Store(o)

	options := &IndexedClientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	if options.NeedsFlushInterval {
		// check to see if an index was cached already from a previous run
		b, s, err := client.Retrieve(IndexKey)
		if err != nil {
			logger.Warn("cache index was not loaded",
				logging.Pairs{"cacheName": cacheName, "error": err.Error()})
		} else if len(b) > 0 && s == status.LookupStatusHit {
			// if an index was cached, load it
			idx.UnmarshalMsg(b)
		}
		if o.FlushInterval > 0 {
			go idx.flusher(ctx)
		} else {
			logger.Warn("cache index flusher was not started",
				logging.Pairs{"cacheName": idx.name, "flushInterval": o.FlushInterval})
		}
	}

	// FIXME: this triggers a failure in pkg/proxy/engines -- need to investigate
	// if options.NeedsReapInterval {
	if o.ReapInterval > 0 {
		go idx.reaper(ctx)
	} else {
		logger.Warn("cache reaper was not started",
			logging.Pairs{"cacheName": idx.name, "reapInterval": o.ReapInterval})
	}
	// }

	gm.CacheMaxObjects.WithLabelValues(cacheName, cacheProvider).Set(float64(o.MaxSizeObjects))
	gm.CacheMaxBytes.WithLabelValues(cacheName, cacheProvider).Set(float64(o.MaxSizeBytes))
	return idx
}

// The IndexedClient maintains metadata about a cache.Client when Retention enforcement is managed internally,
// like memory or bbolt. It is not used for independently managed caches like Redis.
type IndexedClient struct {
	// Client is the underlying cache client used by the Index
	Client cache.Client `msg:"-"`
	// CacheSize represents the size of the cache in bytes
	CacheSize int64 `msg:"cache_size"`
	// ObjectCount represents the count of objects in the Cache
	ObjectCount int64 `msg:"object_count"`
	// Objects is a map of Objects in the Cache
	Objects SyncObjects `msg:"objects"`

	// internal index configuration
	name          string               `msg:"-"`
	cacheProvider string               `msg:"-"`
	options       atomic.Value         `msg:"-"`
	ico           IndexedClientOptions `msg:"-"`
	lastWrite     atomicx.Time         `msg:"-"`
	isClosing     atomic.Bool
	cancel        context.CancelFunc
	flusherExited atomic.Bool
	reaperExited  atomic.Bool
}

// Clear the index from its currently tracked cache objects
func (idx *IndexedClient) Clear() {
	idx.Objects.Clear()
	atomic.StoreInt64(&idx.CacheSize, 0)
	atomic.StoreInt64(&idx.ObjectCount, 0)
}

// UpdateOptions updates the existing IndexedClient with a new Options reference
func (idx *IndexedClient) UpdateOptions(o *options.Options) {
	idx.options.Store(o)
}

// No-op -- implements the cache.Client interface
func (idx *IndexedClient) Connect() error {
	return nil
}

func (idx *IndexedClient) updateIndex(cacheKey string, size int64, la, lw, e time.Time) {
	// store the object (except for the data) in the index
	obj := &Object{
		Key:  cacheKey,
		Size: size,
	}
	obj.LastAccess.Store(la)
	obj.LastWrite.Store(lw)
	if !e.IsZero() {
		obj.Expiration.Store(e)
	}

	// update the index totals
	var cacheSize, count int64
	if o, ok := idx.Objects.Load(cacheKey); ok {
		oldObj := o.(*Object)
		cacheSize = atomic.AddInt64(&idx.CacheSize, obj.Size-oldObj.Size)
		count = atomic.LoadInt64(&idx.ObjectCount)
	} else {
		cacheSize = atomic.AddInt64(&idx.CacheSize, obj.Size)
		count = atomic.AddInt64(&idx.ObjectCount, 1)
	}
	metrics.ObserveCacheSizeChange(idx.name, idx.cacheProvider, cacheSize, count)
	idx.lastWrite.Store(time.Now())
	idx.Objects.Store(cacheKey, obj)
}

func (idx *IndexedClient) StoreReference(cacheKey string, data cache.ReferenceObject, ttl time.Duration) error {
	if cacheKey == IndexKey {
		return ErrIndexInvalidCacheKey
	}
	mc, ok := idx.Client.(cache.MemoryCache)
	if !ok {
		return ErrInvalidCacheBackend
	}
	if err := mc.StoreReference(cacheKey, data, ttl); err != nil {
		return err
	}
	now := time.Now()
	var expiry time.Time
	if ttl > 0 {
		expiry = now.Add(ttl)
	}
	idx.updateIndex(cacheKey, int64(data.Size()), now, now, expiry)
	return nil
}

func (idx *IndexedClient) Store(cacheKey string, byteData []byte, ttl time.Duration) error {
	if cacheKey == IndexKey {
		return ErrIndexInvalidCacheKey
	}
	// wrap input value with Object + timing/size information
	obj := &Object{
		Key:   cacheKey,
		Value: byteData,
		Size:  int64(len(byteData)),
	}
	now := time.Now()
	obj.LastAccess.Store(now)
	obj.LastWrite.Store(now)
	var expiry time.Time
	if ttl > 0 {
		expiry = now.Add(ttl)
		obj.Expiration.Store(expiry)
	}
	// store the object in the cache
	if err := idx.Client.Store(cacheKey, obj.ToBytes(), ttl); err != nil {
		return err
	}
	idx.updateIndex(cacheKey, obj.Size, now, now, expiry)
	return nil
}

func (idx *IndexedClient) updateAccessTime(cacheKey string) {
	o, ok := idx.Objects.Load(cacheKey)
	if !ok {
		return
	}
	obj := o.(*Object)
	now := time.Now()
	obj.LastAccess.Store(now)
}

func (idx *IndexedClient) RetrieveReference(cacheKey string) (any, status.LookupStatus, error) {
	if cacheKey == IndexKey {
		return nil, status.LookupStatusError, ErrIndexInvalidCacheKey
	}
	mc, ok := idx.Client.(cache.MemoryCache)
	if !ok {
		return nil, status.LookupStatusError, ErrInvalidCacheBackend
	}
	go idx.updateAccessTime(cacheKey)
	return mc.RetrieveReference(cacheKey)
}

// Retrieve implements the cache.Client interface, looking up the object and updating the index last access time
func (idx *IndexedClient) Retrieve(cacheKey string) ([]byte, status.LookupStatus, error) {
	if cacheKey == IndexKey {
		return nil, status.LookupStatusError, ErrIndexInvalidCacheKey
	}
	data, s, err := idx.Client.Retrieve(cacheKey)
	if err != nil {
		return nil, s, err
	}
	if s != status.LookupStatusHit {
		return nil, s, err
	}
	o, err := ObjectFromBytes(data)
	if err != nil {
		return nil, status.LookupStatusError, err
	}
	go idx.updateAccessTime(cacheKey)
	return o.Value, s, nil
}

// Remove implements the cache.Client interface and removes the object from the cache and index
func (idx *IndexedClient) Remove(cacheKeys ...string) error {
	// remove the objects from the index
	for _, key := range cacheKeys {
		if o, ok := idx.Objects.Load(key); ok {
			obj := o.(*Object)
			size := atomic.AddInt64(&idx.CacheSize, -obj.Size)
			count := atomic.AddInt64(&idx.ObjectCount, -1)
			metrics.ObserveCacheOperation(idx.name, idx.cacheProvider, "del", "none", float64(obj.Size))
			idx.Objects.Delete(key)
			metrics.ObserveCacheSizeChange(idx.name, idx.cacheProvider, size, count)
		}
	}
	idx.lastWrite.Store(time.Now())
	return idx.Client.Remove(cacheKeys...)
}

// Stop the indexed cache, flush its state, and close the underlying cache
func (idx *IndexedClient) Close() error {
	idx.cancel() // stop the reaper & flusher
	idx.isClosing.Store(true)
	if idx.ico.NeedsFlushInterval {
		idx.flushOnce()
	}
	idx.Clear()
	return idx.Client.Close()
}

// flusher periodically calls the cache's index flush func that writes the cache index to disk
func (idx *IndexedClient) flusher(ctx context.Context) {
	var lastFlush time.Time
FLUSHER:
	for {
		fi := idx.options.Load().(*options.Options).FlushInterval
		select {
		case <-ctx.Done():
			break FLUSHER
		case <-time.After(fi):
			if idx.lastWrite.Load().Before(lastFlush) {
				continue
			}
			idx.flushOnce()
			lastFlush = time.Now()
		}
	}
	idx.flusherExited.Store(true)
}

func (idx *IndexedClient) flushOnce() {
	bytes, err := idx.MarshalMsg(nil)
	if err != nil {
		logger.Warn("unable to serialize index for flushing",
			logging.Pairs{"cacheName": idx.name, "detail": err.Error()})
		return
	}
	idx.Client.Store(IndexKey, bytes, 31536000*time.Second)
}

// reaper continually iterates through the cache to find expired elements and removes them
func (idx *IndexedClient) reaper(ctx context.Context) {
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

// reap makes a single iteration through the cache index to to find and remove expired elements
// and evict least-recently-accessed elements to maintain the Maximum allowed Cache Size
func (idx *IndexedClient) reap() {
	cacheSize := atomic.LoadInt64(&idx.CacheSize)
	if cacheSize < 0 {
		cacheSize = 0
	}
	removals := make([]string, 0)
	remainders := make(objectsAtime, 0, cacheSize)

	var cacheChanged bool

	now := time.Now()

	idx.Objects.Range(func(key, value any) bool {
		o := value.(*Object)
		if o.Expiration.Load().Before(now) && !o.Expiration.Load().IsZero() {
			removals = append(removals, o.Key)
		} else {
			remainders = append(remainders, o)
		}
		return true
	})

	if len(removals) > 0 {
		metrics.ObserveCacheEvent(idx.name, idx.cacheProvider, "eviction", "ttl")
		if err := idx.Remove(removals...); err != nil {
			logger.Error("reap remove error", logging.Pairs{"cacheName": idx.name, "error": err})
		}
		cacheChanged = true
		cacheSize = atomic.LoadInt64(&idx.CacheSize)
	}
	objectCount := atomic.LoadInt64(&idx.ObjectCount)
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
			if err := idx.Remove(removals...); err != nil {
				logger.Error("reap remove error", logging.Pairs{"cacheName": idx.name, "error": err})
			}
			cacheChanged = true
		}

		logger.Debug("size-based cache eviction exercise completed",
			logging.Pairs{
				"reason":         evictionType,
				"cacheSizeBytes": cacheSize, "maxSizeBytes": opts.MaxSizeBytes,
				"cacheSizeObjects": objectCount, "maxSizeObjects": opts.MaxSizeObjects,
			})

	}
	if cacheChanged {
		idx.lastWrite.Store(time.Now())
	}
}

type objectsAtime []*Object

// Len returns the number of elements in the subject slice
func (o objectsAtime) Len() int {
	return len(o)
}

// Less returns true if i comes before j
func (o objectsAtime) Less(i, j int) bool {
	return o[i].LastAccess.Load().Before(o[j].LastAccess.Load())
}

// Swap modifies the subject slice by swapping the values in indexes i and j
func (o objectsAtime) Swap(i, j int) {
	o[i], o[j] = o[j], o[i]
}
