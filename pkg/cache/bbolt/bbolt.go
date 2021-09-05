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

// Package bbolt is the bbolt implementation of the Trickster Cache
package bbolt

import (
	"fmt"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"

	"go.etcd.io/bbolt"
)

// Cache describes a BBolt Cache
type Cache struct {
	Name       string
	Config     *options.Options
	Logger     interface{}
	Index      *index.Index
	locker     locks.NamedLocker
	lockPrefix string

	dbh *bbolt.DB
}

// New returns a new bbolt cache as a Trickster Cache Interface type
func New(fileName, bucketName string) (cache.Cache, error) {

	c := &Cache{}

	c.SetLocker(locks.NewNamedLocker())
	c.Config = options.New()

	c.Config.BBolt.Bucket = bucketName
	c.Config.BBolt.Filename = fileName

	err := c.Connect()
	if err != nil {
		return nil, err
	}

	return c, nil
}

// Locker returns the cache's locker
func (c *Cache) Locker() locks.NamedLocker {
	return c.locker
}

// SetLocker sets the cache's locker
func (c *Cache) SetLocker(l locks.NamedLocker) {
	c.locker = l
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *options.Options {
	return c.Config
}

// Connect instantiates the Cache mutex map and starts the Expired Entry Reaper goroutine
func (c *Cache) Connect() error {
	tl.Info(c.Logger, "bbolt cache setup", tl.Pairs{"name": c.Name, "cacheFile": c.Config.BBolt.Filename})

	c.lockPrefix = c.Name + ".bbolt."

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
	c.Index = index.NewIndex(c.Name, c.Config.Provider, indexData,
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
		tl.Error(c.Logger, "cache failed to write non-indexed object",
			tl.Pairs{"cacheName": c.Name, "cacheProvider": "bbolt",
				"cacheKey": cacheKey, "objectSize": len(data)})
	}
}

func (c *Cache) store(cacheKey string, data []byte, ttl time.Duration, updateIndex bool) error {

	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}

	metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "set", "none", float64(len(data)))

	o := &index.Object{Key: cacheKey, Value: data, Expiration: exp}
	nl, _ := c.locker.Acquire(c.lockPrefix + cacheKey)
	err := writeToBBolt(c.dbh, c.Config.BBolt.Bucket, cacheKey, o.ToBytes())
	nl.Release()
	if err != nil {
		return err
	}
	tl.Debug(c.Logger, "bbolt cache store", tl.Pairs{"key": cacheKey, "ttl": ttl, "indexed": updateIndex})
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

func (c *Cache) retrieve(cacheKey string, allowExpired bool,
	atime bool) ([]byte, status.LookupStatus, error) {

	nl, _ := c.locker.RAcquire(c.lockPrefix + cacheKey)
	var data []byte
	err := c.dbh.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		data = b.Get([]byte(cacheKey))
		if data == nil {
			tl.Debug(c.Logger, "bbolt cache miss", tl.Pairs{"key": cacheKey})
			metrics.ObserveCacheMiss(cacheKey, c.Name, c.Config.Provider)
			return cache.ErrKNF
		}
		return nil
	})
	nl.RRelease()
	if err != nil {
		return nil, status.LookupStatusKeyMiss, err
	}

	o, err := index.ObjectFromBytes(data)
	if err != nil {
		_, err = metrics.CacheError(cacheKey, c.Name, c.Config.Provider,
			"value for key [%s] could not be deserialized from cache")
		return nil, status.LookupStatusError, err
	}

	// if retrieve() is being called to load the index, the index will be nil, so just return the value
	// so as to instantiate the index
	if c.Index == nil {
		return o.Value, status.LookupStatusHit, nil
	}

	o.Expiration = c.Index.GetExpiration(cacheKey)

	if allowExpired || o.Expiration.IsZero() || o.Expiration.After(time.Now()) {
		tl.Debug(c.Logger, "bbolt cache retrieve", tl.Pairs{"cacheKey": cacheKey})
		if atime {
			go c.Index.UpdateObjectAccessTime(cacheKey)
		}
		metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "get", "hit", float64(len(data)))
		return o.Value, status.LookupStatusHit, nil
	}
	// Cache Object has been expired but not reaped, go ahead and delete it
	go c.remove(cacheKey, false)
	metrics.ObserveCacheMiss(cacheKey, c.Name, c.Config.Provider)
	return nil, status.LookupStatusKeyMiss, cache.ErrKNF
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
	nl, _ := c.locker.Acquire(c.lockPrefix + cacheKey)
	err := c.dbh.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		return b.Delete([]byte(cacheKey))
	})
	nl.Release()
	if err != nil {
		tl.Error(c.Logger, "bbolt cache key delete failure",
			tl.Pairs{"cacheKey": cacheKey, "reason": err.Error()})
		return err
	}
	if !isBulk {
		go c.Index.RemoveObject(cacheKey)
	}
	metrics.ObserveCacheDel(c.Name, c.Config.Provider, 0)
	tl.Debug(c.Logger, "bbolt cache key delete", tl.Pairs{"key": cacheKey})
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
	if c.Index != nil {
		c.Index.Close()
	}
	if c.dbh != nil {
		return c.dbh.Close()
	}
	return nil
}
