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
	log.Info("memorycache setup", log.Pairs{})
	lockPrefix = c.Name + ".memory."
	c.client = sync.Map{}
	c.Index = index.NewIndex(c.Name, c.Config.Type, nil, c.Config.Index, c.BulkRemove, nil)
	return nil
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {

	locks.Acquire(lockPrefix + cacheKey)
	defer locks.Release(lockPrefix + cacheKey)

	cache.ObserveCacheOperation(c.Name, c.Config.Type, "set", "none", float64(len(data)))
	log.Debug("memorycache cache store", log.Pairs{"cacheKey": cacheKey, "length": len(data), "ttl": ttl})
	o := index.Object{Key: cacheKey, Value: data, Expiration: time.Now().Add(ttl)}
	c.client.Store(cacheKey, o)
	go c.Index.UpdateObject(o)
	return nil
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string) ([]byte, error) {

	locks.Acquire(lockPrefix + cacheKey)
	defer locks.Release(lockPrefix + cacheKey)

	record, ok := c.client.Load(cacheKey)
	if ok {
		r := record.(index.Object)
		if r.Expiration.After(time.Now()) {
			log.Debug("memorycache cache retrieve", log.Pairs{"cacheKey": cacheKey})
			c.Index.UpdateObjectAccessTime(cacheKey)
			cache.ObserveCacheOperation(c.Name, c.Config.Type, "get", "hit", float64(len(r.Value)))
			return r.Value, nil
		}

		// Cache Object has been expired but not reaped, go ahead and delete it
		go c.Remove(cacheKey)
	}

	return cache.ObserveCacheMiss(cacheKey, c.Name, c.Config.Type)
}

// Remove removes an object from the cache
func (c *Cache) Remove(cacheKey string) {
	c.remove(cacheKey, false)
}

func (c *Cache) remove(cacheKey string, noLock bool) {

	locks.Acquire(lockPrefix + cacheKey)
	defer locks.Release(lockPrefix + cacheKey)

	c.client.Delete(cacheKey)
	c.Index.RemoveObject(cacheKey, noLock)
	cache.ObserveCacheDel(c.Name, c.Config.Type, 0)

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
