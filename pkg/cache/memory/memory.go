/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

// Package memory is the memory implementation of the Trickster Cache
// and uses a sync.Map to manage cache objects
package memory

import (
	"sync"
	"time"

	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/cache/index"
	"github.com/tricksterproxy/trickster/pkg/cache/metrics"
	"github.com/tricksterproxy/trickster/pkg/cache/options"
	"github.com/tricksterproxy/trickster/pkg/cache/status"
	"github.com/tricksterproxy/trickster/pkg/locks"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
)

var lockPrefix string

// Cache defines a a Memory Cache client that conforms to the Cache interface
type Cache struct {
	Name   string
	client sync.Map
	Config *options.Options
	Index  *index.Index
	Logger *tl.Logger
	locker locks.NamedLocker
}

func (c *Cache) Locker() locks.NamedLocker {
	return c.locker
}

func (c *Cache) SetLocker(l locks.NamedLocker) {
	c.locker = l
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *options.Options {
	return c.Config
}

// Connect initializes the Cache
func (c *Cache) Connect() error {
	c.Logger.Info("memorycache setup", tl.Pairs{"name": c.Name, "maxSizeBytes": c.Config.Index.MaxSizeBytes, "maxSizeObjects": c.Config.Index.MaxSizeObjects})
	lockPrefix = c.Name + ".memory."
	c.client = sync.Map{}
	c.Index = index.NewIndex(c.Name, c.Config.CacheType, nil, c.Config.Index, c.BulkRemove, nil, c.Logger)
	return nil
}

// StoreReference stores an object directly to the memory cache without requiring serialization
func (c *Cache) StoreReference(cacheKey string, data cache.ReferenceObject, ttl time.Duration) error {
	return c.store(cacheKey, nil, data, ttl, true)
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return c.store(cacheKey, data, nil, ttl, true)
}

func (c *Cache) store(cacheKey string, byteData []byte, refData cache.ReferenceObject, ttl time.Duration, updateIndex bool) error {

	var o1, o2 *index.Object
	var l int
	isDirect := byteData == nil && refData != nil
	if byteData != nil {
		l = len(byteData)
		metrics.ObserveCacheOperation(c.Name, c.Config.CacheType, "set", "none", float64(l))
		o1 = &index.Object{Key: cacheKey, Value: byteData, Expiration: time.Now().Add(ttl)}
		o2 = &index.Object{Key: cacheKey, Value: byteData, Expiration: time.Now().Add(ttl)}
	} else if refData != nil {
		metrics.ObserveCacheOperation(c.Name, c.Config.CacheType, "setDirect", "none", 0)
		o1 = &index.Object{Key: cacheKey, ReferenceValue: refData, Expiration: time.Now().Add(ttl)}
		o2 = &index.Object{Key: cacheKey, ReferenceValue: refData, Expiration: time.Now().Add(ttl)}
	}

	if o1 != nil && o2 != nil {
		nl, _ := c.locker.Acquire(lockPrefix + cacheKey)
		go c.Logger.Debug("memorycache cache store", tl.Pairs{"cacheKey": cacheKey, "length": l, "ttl": ttl, "is_direct": isDirect})
		c.client.Store(cacheKey, o1)
		if updateIndex {
			c.Index.UpdateObject(o2)
		}
		nl.Release()
	}

	return nil
}

// RetrieveReference looks for an object in cache and returns it (or an error if not found)
func (c *Cache) RetrieveReference(cacheKey string, allowExpired bool) (interface{}, status.LookupStatus, error) {
	o, s, err := c.retrieve(cacheKey, allowExpired, true)
	if err != nil {
		return nil, s, err
	}
	if o != nil {
		return o.ReferenceValue, s, nil
	}
	return nil, s, nil
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string, allowExpired bool) ([]byte, status.LookupStatus, error) {
	o, s, err := c.retrieve(cacheKey, allowExpired, true)
	if err != nil {
		return nil, s, err
	}
	if o != nil {
		return o.Value, s, nil
	}
	return nil, s, nil
}

func (c *Cache) retrieve(cacheKey string, allowExpired bool, atime bool) (*index.Object, status.LookupStatus, error) {

	nl, _ := c.locker.RAcquire(lockPrefix + cacheKey)
	record, ok := c.client.Load(cacheKey)
	nl.RRelease()

	if ok {
		o := record.(*index.Object)
		o.Expiration = c.Index.GetExpiration(cacheKey)

		if allowExpired || o.Expiration.IsZero() || o.Expiration.After(time.Now()) {
			c.Logger.Debug("memory cache retrieve", tl.Pairs{"cacheKey": cacheKey})
			if atime {
				go c.Index.UpdateObjectAccessTime(cacheKey)
			}
			metrics.ObserveCacheOperation(c.Name, c.Config.CacheType, "get", "hit", float64(len(o.Value)))
			return o, status.LookupStatusHit, nil
		}
		// Cache Object has been expired but not reaped, go ahead and delete it
		go c.remove(cacheKey, false)
	}
	_, err := metrics.ObserveCacheMiss(cacheKey, c.Name, c.Config.CacheType)
	return nil, status.LookupStatusKeyMiss, err
}

// SetTTL updates the TTL for the provided cache object
func (c *Cache) SetTTL(cacheKey string, ttl time.Duration) {
	go c.Index.UpdateObjectTTL(cacheKey, ttl)
}

// Remove removes an object from the cache
func (c *Cache) Remove(cacheKey string) {
	c.remove(cacheKey, false)
}

func (c *Cache) remove(cacheKey string, isBulk bool) {
	nl, _ := c.locker.Acquire(lockPrefix + cacheKey)
	c.client.Delete(cacheKey)
	nl.Release()
	if !isBulk {
		go c.Index.RemoveObject(cacheKey)
	}
	metrics.ObserveCacheDel(c.Name, c.Config.CacheType, 0)
}

// BulkRemove removes a list of objects from the cache
func (c *Cache) BulkRemove(cacheKeys []string) {
	wg := &sync.WaitGroup{}
	for _, cacheKey := range cacheKeys {
		wg.Add(1)
		go func(key string) {
			c.remove(key, true)
			wg.Done()
		}(cacheKey)
	}
	wg.Wait()
}

// Close is not used for Cache, and is here to fully prototype the Cache Interface
func (c *Cache) Close() error {
	return nil
}
