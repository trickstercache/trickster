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

// Package bbolt is the bbolt implementation of the Trickster Cache
package bbolt

import (
	"fmt"
	"sync"
	"time"

	"github.com/tricksterproxy/trickster/pkg/cache/index"
	"github.com/tricksterproxy/trickster/pkg/cache/metrics"
	"github.com/tricksterproxy/trickster/pkg/cache/options"
	"github.com/tricksterproxy/trickster/pkg/cache/status"
	"github.com/tricksterproxy/trickster/pkg/locks"
	"github.com/tricksterproxy/trickster/pkg/util/log"

	"github.com/coreos/bbolt"
)

var lockPrefix string

// Cache describes a BBolt Cache
type Cache struct {
	Name   string
	Config *options.Options
	Logger *log.Logger
	Index  *index.Index
	locker locks.NamedLocker

	dbh *bbolt.DB
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

// Connect instantiates the Cache mutex map and starts the Expired Entry Reaper goroutine
func (c *Cache) Connect() error {
	c.Logger.Info("bbolt cache setup", log.Pairs{"name": c.Name, "cacheFile": c.Config.BBolt.Filename})

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
	indexData, _, _ := c.retrieve(index.IndexKey, false, false)
	c.Index = index.NewIndex(c.Name, c.Config.CacheType, indexData,
		c.Config.Index, c.BulkRemove, c.storeNoIndex, c.Logger)
	return nil
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return c.store(cacheKey, data, ttl, true)
}

func (c *Cache) storeNoIndex(cacheKey string, data []byte) {
	err := c.store(cacheKey, data, 31536000*time.Second, false)
	if err != nil {
		c.Logger.Error("cache failed to write non-indexed object", log.Pairs{"cacheName": c.Name, "cacheType": "bbolt", "cacheKey": cacheKey, "objectSize": len(data)})
	}
}

func (c *Cache) store(cacheKey string, data []byte, ttl time.Duration, updateIndex bool) error {

	metrics.ObserveCacheOperation(c.Name, c.Config.CacheType, "set", "none", float64(len(data)))

	o := &index.Object{Key: cacheKey, Value: data, Expiration: time.Now().Add(ttl)}
	nl, _ := c.locker.Acquire(lockPrefix + cacheKey)
	err := writeToBBolt(c.dbh, c.Config.BBolt.Bucket, cacheKey, o.ToBytes())
	nl.Release()
	if err != nil {
		return err
	}
	c.Logger.Debug("bbolt cache store", log.Pairs{"key": cacheKey, "ttl": ttl, "indexed": updateIndex})
	if updateIndex {
		c.Index.UpdateObject(o)
	}
	return nil
}

func writeToBBolt(dbh *bbolt.DB, bucketName, cacheKey string, data []byte) error {
	err := dbh.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		err2 := b.Put([]byte(cacheKey), data)
		return err2
	})
	return err
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string, allowExpired bool) ([]byte, status.LookupStatus, error) {
	return c.retrieve(cacheKey, allowExpired, true)
}

func (c *Cache) retrieve(cacheKey string, allowExpired bool, atime bool) ([]byte, status.LookupStatus, error) {

	nl, _ := c.locker.RAcquire(lockPrefix + cacheKey)
	var data []byte
	err := c.dbh.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		data = b.Get([]byte(cacheKey))
		if data == nil {
			c.Logger.Debug("bbolt cache miss", log.Pairs{"key": cacheKey})
			_, cme := metrics.ObserveCacheMiss(cacheKey, c.Name, c.Config.CacheType)
			return cme
		}
		return nil
	})
	nl.RRelease()
	if err != nil {
		return nil, status.LookupStatusKeyMiss, err
	}

	o, err := index.ObjectFromBytes(data)
	if err != nil {
		_, err = metrics.CacheError(cacheKey, c.Name, c.Config.CacheType, "value for key [%s] could not be deserialized from cache")
		return nil, status.LookupStatusError, err
	}

	// if retrieve() is being called to load the index, the index will be nil, so just return the value
	// so as to instantiate the index
	if c.Index == nil {
		return o.Value, status.LookupStatusHit, nil
	}

	o.Expiration = c.Index.GetExpiration(cacheKey)

	if allowExpired || o.Expiration.IsZero() || o.Expiration.After(time.Now()) {
		c.Logger.Debug("bbolt cache retrieve", log.Pairs{"cacheKey": cacheKey})
		if atime {
			go c.Index.UpdateObjectAccessTime(cacheKey)
		}
		metrics.ObserveCacheOperation(c.Name, c.Config.CacheType, "get", "hit", float64(len(data)))
		return o.Value, status.LookupStatusHit, nil
	}
	// Cache Object has been expired but not reaped, go ahead and delete it
	go c.remove(cacheKey, false)
	b, err := metrics.ObserveCacheMiss(cacheKey, c.Name, c.Config.CacheType)
	return b, status.LookupStatusKeyMiss, err
}

// SetTTL updates the TTL for the provided cache object
func (c *Cache) SetTTL(cacheKey string, ttl time.Duration) {
	go c.Index.UpdateObjectTTL(cacheKey, ttl)
}

// Remove removes an object in cache, if present
func (c *Cache) Remove(cacheKey string) {
	c.remove(cacheKey, false)
}

func (c *Cache) remove(cacheKey string, isBulk bool) error {
	nl, _ := c.locker.Acquire(lockPrefix + cacheKey)
	err := c.dbh.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		return b.Delete([]byte(cacheKey))
	})
	nl.Release()
	if err != nil {
		c.Logger.Error("bbolt cache key delete failure", log.Pairs{"cacheKey": cacheKey, "reason": err.Error()})
		return err
	}
	if !isBulk {
		go c.Index.RemoveObject(cacheKey)
	}
	metrics.ObserveCacheDel(c.Name, c.Config.CacheType, 0)
	c.Logger.Debug("bbolt cache key delete", log.Pairs{"key": cacheKey})
	return nil
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

// Close closes the Cache
func (c *Cache) Close() error {
	return c.dbh.Close()
}
