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

package memory

import (
	"sync"
	"time"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/cache/index"
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

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return c.store(cacheKey, data, ttl, true)
}

func (c *Cache) store(cacheKey string, data []byte, ttl time.Duration, updateIndex bool) error {

	locks.Acquire(lockPrefix + cacheKey)

	cache.ObserveCacheOperation(c.Name, c.Config.CacheType, "set", "none", float64(len(data)))

	o1 := &index.Object{Key: cacheKey, Value: data, Expiration: time.Now().Add(ttl)}
	o2 := &index.Object{Key: cacheKey, Value: data, Expiration: time.Now().Add(ttl)}
	log.Debug("memorycache cache store", log.Pairs{"cacheKey": cacheKey, "length": len(data), "ttl": ttl})
	c.client.Store(cacheKey, o1)

	if updateIndex {
		c.Index.UpdateObject(o2)
	}
	locks.Release(lockPrefix + cacheKey)
	return nil
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string, allowExpired bool) ([]byte, error) {
	return c.retrieve(cacheKey, allowExpired, true)
}

func (c *Cache) retrieve(cacheKey string, allowExpired bool, atime bool) ([]byte, error) {

	locks.Acquire(lockPrefix + cacheKey)

	record, ok := c.client.Load(cacheKey)

	if ok {
		o := record.(*index.Object)
		o.Expiration = c.Index.GetExpiration(cacheKey)

		if allowExpired || o.Expiration.IsZero() || o.Expiration.After(time.Now()) {
			log.Debug("bbolt cache retrieve", log.Pairs{"cacheKey": cacheKey})
			if atime {
				c.Index.UpdateObjectAccessTime(cacheKey)
			}
			cache.ObserveCacheOperation(c.Name, c.Config.CacheType, "get", "hit", float64(len(o.Value)))
			locks.Release(lockPrefix + cacheKey)
			return o.Value, nil
		}
		// Cache Object has been expired but not reaped, go ahead and delete it
		c.remove(cacheKey, false)
	}
	locks.Release(lockPrefix + cacheKey)
	return cache.ObserveCacheMiss(cacheKey, c.Name, c.Config.CacheType)

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
