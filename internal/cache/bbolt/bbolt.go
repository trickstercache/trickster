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

package bbolt

import (
	"fmt"
	"time"

	"github.com/coreos/bbolt"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/cache/index"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/pkg/locks"
)

var lockPrefix string

// Cache describes a BBolt Cache
type Cache struct {
	Name   string
	Config *config.CachingConfig
	dbh    *bbolt.DB
	Index  *index.Index
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

// Connect instantiates the Cache mutex map and starts the Expired Entry Reaper goroutine
func (c *Cache) Connect() error {
	log.Info("bbolt cache setup", log.Pairs{"name": c.Name, "cacheFile": c.Config.BBolt.Filename})

	lockPrefix = c.Name + ".bbolt."

	var err error
	c.dbh, err = bbolt.Open(c.Config.BBolt.Filename, 0644, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return err
	}

	err = c.dbh.Update(func(tx *bbolt.Tx) error {
		_, err2 := tx.CreateBucketIfNotExists([]byte(c.Config.BBolt.Bucket))
		if err2 != nil {
			return fmt.Errorf("create bucket: %s", err2)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Load Index here and pass bytes as param2
	indexData, _ := c.retrieve(index.IndexKey, false, false)
	c.Index = index.NewIndex(c.Name, c.Config.CacheType, indexData, c.Config.Index, c.BulkRemove, c.storeNoIndex)
	return nil
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return c.store(cacheKey, data, ttl, true)
}

func (c *Cache) storeNoIndex(cacheKey string, data []byte) {
	err := c.store(cacheKey, data, 31536000*time.Second, false)
	if err != nil {
		log.Error("cache failed to write non-indexed object", log.Pairs{"cacheName": c.Name, "cacheType": "bbolt", "cacheKey": cacheKey, "objectSize": len(data)})
	}
}

func (c *Cache) store(cacheKey string, data []byte, ttl time.Duration, updateIndex bool) error {

	locks.Acquire(lockPrefix + cacheKey)
	cache.ObserveCacheOperation(c.Name, c.Config.CacheType, "set", "none", float64(len(data)))

	o := &index.Object{Key: cacheKey, Value: data, Expiration: time.Now().Add(ttl)}
	err := writeToBBolt(c.dbh, c.Config.BBolt.Bucket, cacheKey, o.ToBytes())
	if err != nil {
		locks.Release(lockPrefix + cacheKey)
		return err
	}
	log.Debug("bbolt cache store", log.Pairs{"key": cacheKey, "ttl": ttl, "indexed": updateIndex})
	if updateIndex {
		c.Index.UpdateObject(o)
	}
	locks.Release(lockPrefix + cacheKey)
	return nil
}

func writeToBBolt(dbh *bbolt.DB, bucketName, cacheKey string, data []byte) error {
	err := dbh.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		err2 := b.Put([]byte(cacheKey), data)
		locks.Release(lockPrefix + cacheKey)
		return err2
	})
	return err
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string, allowExpired bool) ([]byte, error) {
	return c.retrieve(cacheKey, allowExpired, true)
}

func (c *Cache) retrieve(cacheKey string, allowExpired bool, atime bool) ([]byte, error) {

	locks.Acquire(lockPrefix + cacheKey)

	var data []byte
	err := c.dbh.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		data = b.Get([]byte(cacheKey))
		if data == nil {
			log.Debug("bbolt cache miss", log.Pairs{"key": cacheKey})
			_, cme := cache.ObserveCacheMiss(cacheKey, c.Name, c.Config.CacheType)
			locks.Release(lockPrefix + cacheKey)
			return cme
		}
		locks.Release(lockPrefix + cacheKey)
		return nil
	})
	if err != nil {
		locks.Release(lockPrefix + cacheKey)
		return nil, err
	}

	o, err := index.ObjectFromBytes(data)
	if err != nil {
		locks.Release(lockPrefix + cacheKey)
		return cache.CacheError(cacheKey, c.Name, c.Config.CacheType, "value for key [%s] could not be deserialized from cache")
	}
	o.Expiration = c.Index.GetExpiration(cacheKey)

	if allowExpired || o.Expiration.IsZero() || o.Expiration.After(time.Now()) {
		log.Debug("bbolt cache retrieve", log.Pairs{"cacheKey": cacheKey})
		if atime {
			c.Index.UpdateObjectAccessTime(cacheKey)
		}
		cache.ObserveCacheOperation(c.Name, c.Config.CacheType, "get", "hit", float64(len(data)))
		locks.Release(lockPrefix + cacheKey)
		return o.Value, nil
	}
	// Cache Object has been expired but not reaped, go ahead and delete it
	c.remove(cacheKey, false)
	b, err := cache.ObserveCacheMiss(cacheKey, c.Name, c.Config.CacheType)
	locks.Release(lockPrefix + cacheKey)

	return b, err
}

// SetTTL updates the TTL for the provided cache object
func (c *Cache) SetTTL(cacheKey string, ttl time.Duration) {
	locks.Acquire(lockPrefix + cacheKey)
	c.Index.UpdateObjectTTL(cacheKey, ttl)
	locks.Release(lockPrefix + cacheKey)
}

// Remove removes an object in cache, if present
func (c *Cache) Remove(cacheKey string) {
	locks.Acquire(lockPrefix + cacheKey)
	c.remove(cacheKey, false)
	locks.Release(lockPrefix + cacheKey)
}

func (c *Cache) remove(cacheKey string, noLock bool) error {

	err := c.dbh.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		return b.Delete([]byte(cacheKey))
	})
	if err != nil {
		log.Error("bbolt cache key delete failure", log.Pairs{"cacheKey": cacheKey, "reason": err.Error()})
		return err
	}
	c.Index.RemoveObject(cacheKey, noLock)
	cache.ObserveCacheDel(c.Name, c.Config.CacheType, 0)
	log.Debug("bbolt cache key delete", log.Pairs{"key": cacheKey})
	return nil
}

// BulkRemove removes a list of objects from the cache
func (c *Cache) BulkRemove(cacheKeys []string, noLock bool) {
	for _, cacheKey := range cacheKeys {
		c.remove(cacheKey, noLock)
	}
}

// Close closes the Cache
func (c *Cache) Close() error {
	return c.dbh.Close()
}
