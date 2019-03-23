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

	bbolt "github.com/coreos/bbolt"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

// Cache describes a BBolt Cache
type Cache struct {
	Name   string
	Config *config.CachingConfig
	dbh    *bbolt.DB
	Index  *cache.Index
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

// Connect instantiates the Cache mutex map and starts the Expired Entry Reaper goroutine
func (c *Cache) Connect() error {
	log.Info("bbolt cache setup", log.Pairs{"cacheFile": c.Config.BBolt.Filename})

	var err error
	c.dbh, err = bbolt.Open(c.Config.BBolt.Filename, 0644, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return err
	}

	err = c.dbh.Update(func(tx *bbolt.Tx) error {
		tx.CreateBucketIfNotExists([]byte(c.Config.BBolt.Bucket))
		if err != nil {
			return fmt.Errorf("create bucket: %s", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	c.Index = cache.NewIndex(nil, c.BulkRemove, time.Duration(c.Config.ReapIntervalMS)*time.Millisecond)

	return nil
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl int64) error {

	o := cache.Object{Key: cacheKey, Value: data, Expiration: time.Now().Add(time.Duration(ttl) * time.Second)}

	err := c.dbh.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		return b.Put([]byte(cacheKey), o.ToBytes())
	})
	if err != nil {
		return err
	}
	log.Debug("bbolt cache store", log.Pairs{"key": cacheKey})
	go c.Index.UpdateObject(o)
	return nil

}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string) ([]byte, error) {

	var data []byte
	err := c.dbh.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		data = b.Get([]byte(cacheKey))
		if data == nil {
			log.Debug("bbolt cache miss", log.Pairs{"key": cacheKey})
			_, cme := cache.CacheMiss(cacheKey)
			return cme
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	o, err := cache.ObjectFromBytes(data)
	if err != nil {
		return cache.CacheError(cacheKey, "value for key [%s] could not be deserialized from cache")
	}

	if o.Expiration.After(time.Now()) {
		log.Debug("bbolt cache retrieve", log.Pairs{"cacheKey": cacheKey})
		c.Index.UpdateObjectAccessTime(cacheKey)
		return o.Value, nil
	}
	// Cache Object has been expired but not reaped, go ahead and delete it
	go c.Remove(cacheKey)
	return cache.CacheMiss(cacheKey)

}

// Remove removes an object in cache, if present
func (c *Cache) Remove(cacheKey string) {
	c.remove(cacheKey)
	c.Index.RemoveObject(cacheKey, false)
}

func (c *Cache) remove(cacheKey string) error {
	err := c.dbh.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		return b.Delete([]byte(cacheKey))
	})
	if err != nil {
		log.Error("bbolt cache key delete failure", log.Pairs{"cacheKey": cacheKey, "reason": err.Error()})
		return err
	}
	log.Debug("bbolt cache key delete", log.Pairs{"key": cacheKey})
	return nil
}

// BulkRemove removes a list of objects from the cache
func (c *Cache) BulkRemove(cacheKeys []string, noLock bool) {
	for _, cacheKey := range cacheKeys {
		c.remove(cacheKey)
		c.Index.RemoveObject(cacheKey, noLock)
	}
}

// Close closes the Cache
func (c *Cache) Close() error {
	return c.dbh.Close()
}
