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

// Package memory is the memory implementation of the Trickster Cache
// and uses a sync.Map to manage cache objects
package memory

import (
	"sync"
	"time"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/cache/index"
	"github.com/Comcast/trickster/internal/cache/status"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/pkg/locks"
)

var lockPrefix string

// Cache defines a a Memory Cache client that conforms to the Cache interface
type Cache struct {
	Name   string
	client sync.Map
	Config *config.CachingConfig
	Index  *index.Index
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

// Connect initializes the Cache
func (c *Cache) Connect() error {
	log.Info("memorycache setup", log.Pairs{"name": c.Name, "maxSizeBytes": c.Config.Index.MaxSizeBytes, "maxSizeObjects": c.Config.Index.MaxSizeObjects})
	lockPrefix = c.Name + ".memory."
	c.client = sync.Map{}
	c.Index = index.NewIndex(c.Name, c.Config.CacheType, nil, c.Config.Index, c.BulkRemove, nil)
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

	locks.Acquire(lockPrefix + cacheKey)

	var o1, o2 *index.Object
	var l int
	isDirect := byteData == nil && refData != nil
	if byteData != nil {
		l = len(byteData)
		cache.ObserveCacheOperation(c.Name, c.Config.CacheType, "set", "none", float64(l))
		o1 = &index.Object{Key: cacheKey, Value: byteData, Expiration: time.Now().Add(ttl)}
		o2 = &index.Object{Key: cacheKey, Value: byteData, Expiration: time.Now().Add(ttl)}
	} else if refData != nil {
		cache.ObserveCacheOperation(c.Name, c.Config.CacheType, "setDirect", "none", 0)
		o1 = &index.Object{Key: cacheKey, ReferenceValue: refData, Expiration: time.Now().Add(ttl)}
		o2 = &index.Object{Key: cacheKey, ReferenceValue: refData, Expiration: time.Now().Add(ttl)}
	}

	go log.Debug("memorycache cache store", log.Pairs{"cacheKey": cacheKey, "length": l, "ttl": ttl, "is_direct": isDirect})

	if o1 != nil && o2 != nil {
		c.client.Store(cacheKey, o1)
		if updateIndex {
			c.Index.UpdateObject(o2)
		}
	}

	locks.Release(lockPrefix + cacheKey)
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

	locks.Acquire(lockPrefix + cacheKey)

	record, ok := c.client.Load(cacheKey)

	if ok {
		o := record.(*index.Object)
		o.Expiration = c.Index.GetExpiration(cacheKey)

		if allowExpired || o.Expiration.IsZero() || o.Expiration.After(time.Now()) {
			log.Debug("memory cache retrieve", log.Pairs{"cacheKey": cacheKey})
			if atime {
				go c.Index.UpdateObjectAccessTime(cacheKey)
			}
			cache.ObserveCacheOperation(c.Name, c.Config.CacheType, "get", "hit", float64(len(o.Value)))
			locks.Release(lockPrefix + cacheKey)
			return o, status.LookupStatusHit, nil
		}
		// Cache Object has been expired but not reaped, go ahead and delete it
		go c.remove(cacheKey, false)
	}
	locks.Release(lockPrefix + cacheKey)
	_, err := cache.ObserveCacheMiss(cacheKey, c.Name, c.Config.CacheType)
	return nil, status.LookupStatusKeyMiss, err

}

// SetTTL updates the TTL for the provided cache object
func (c *Cache) SetTTL(cacheKey string, ttl time.Duration) {
	c.Index.UpdateObjectTTL(cacheKey, ttl)
}

// Remove removes an object from the cache
func (c *Cache) Remove(cacheKey string) {
	c.remove(cacheKey, false)
}

func (c *Cache) remove(cacheKey string, noLock bool) {
	locks.Acquire(lockPrefix + cacheKey)
	c.client.Delete(cacheKey)
	c.Index.RemoveObject(cacheKey, noLock)
	cache.ObserveCacheDel(c.Name, c.Config.CacheType, 0)
	locks.Release(lockPrefix + cacheKey)
}

// BulkRemove removes a list of objects from the cache
func (c *Cache) BulkRemove(cacheKeys []string, noLock bool) {
	for _, cacheKey := range cacheKeys {
		c.remove(cacheKey, noLock)
	}
}

// Close is not used for Cache, and is here to fully prototype the Cache Interface
func (c *Cache) Close() error {
	return nil
}
