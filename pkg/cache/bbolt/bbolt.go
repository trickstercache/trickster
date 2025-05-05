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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	"github.com/trickstercache/trickster/v2/pkg/cache/internal"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/util/atomicx"

	"go.etcd.io/bbolt"
)

// Cache describes a BBolt Cache
type Cache struct {
	internal.Cache

	dbh *bbolt.DB
}

// New returns a new bbolt cache as a Trickster Cache Interface type
func New(cacheName, fileName, bucketName string, opts *options.Options) cache.Cache {

	c := &Cache{}
	if opts == nil {
		opts = options.New()
	}

	if bucketName != "" {
		opts.BBolt.Bucket = bucketName
	}
	if fileName != "" {
		opts.BBolt.Filename = fileName
	}

	lp := fmt.Sprintf("%s.bbolt.", cacheName)
	c.Cache = *internal.NewCache(cacheName, lp, &internal.CacheOptions{
		Options:  opts,
		Connect:  c.connect,
		Store:    c.store,
		Retrieve: c.retrieve,
		Delete: func(cacheKey string) error {
			return c.remove(cacheKey, false)
		},
		Close: func() error {
			if c.dbh == nil {
				return nil
			}
			return c.dbh.Close()
		},
	})

	c.SetLocker(locks.NewNamedLocker())
	return c
}

// Connect instantiates the Cache mutex map and starts the Expired Entry Reaper goroutine
func (c *Cache) connect() error {
	logger.Info("bbolt cache setup",
		logging.Pairs{"name": c.Name, "cacheFile": c.Config.BBolt.Filename})

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
	var b []byte
	if indexData != nil {
		b = indexData.Value
	}
	c.Index = index.NewIndex(c.Name, c.Config.Provider, b,
		c.Config.Index, c.BulkRemove, c.storeNoIndex)
	c.Cache.Index = c.Index
	return nil
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return c.store(cacheKey, data, nil, ttl, true)
}

func (c *Cache) storeNoIndex(cacheKey string, data []byte) {
	err := c.store(cacheKey, data, nil, 31536000*time.Second, false)
	if err != nil {
		logger.Error("cache failed to write non-indexed object",
			logging.Pairs{"cacheName": c.Name, "cacheProvider": "bbolt",
				"cacheKey": cacheKey, "objectSize": len(data)})
	}
}

func (c *Cache) store(cacheKey string, data []byte, refData cache.ReferenceObject, ttl time.Duration, updateIndex bool) error {

	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}

	metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "set", "none", float64(len(data)))

	o := &index.Object{Key: cacheKey, Value: data, Expiration: *atomicx.NewTime(exp)}
	nl, _ := c.Locker().Acquire(c.Cache.LockPrefix + cacheKey)
	err := writeToBBolt(c.dbh, c.Config.BBolt.Bucket, cacheKey, o.ToBytes())
	nl.Release()
	if err != nil {
		return err
	}
	logger.Debug("bbolt cache store",
		logging.Pairs{"key": cacheKey, "ttl": ttl, "indexed": updateIndex})
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

func (c *Cache) retrieve(cacheKey string, allowExpired bool,
	atime bool) (*index.Object, status.LookupStatus, error) {

	nl, _ := c.Locker().RAcquire(c.Cache.LockPrefix + cacheKey)
	var data []byte
	err := c.dbh.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		data = b.Get([]byte(cacheKey))
		if data == nil {
			logger.Debug("bbolt cache miss", logging.Pairs{"key": cacheKey})
			metrics.ObserveCacheMiss(c.Name, c.Config.Provider)
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
		return o, status.LookupStatusHit, nil
	}

	o.Expiration.Store(c.Index.GetExpiration(cacheKey))

	if allowExpired || o.Expiration.Load().IsZero() || o.Expiration.Load().After(time.Now()) {
		logger.Debug("bbolt cache retrieve", logging.Pairs{"cacheKey": cacheKey})
		if atime {
			go c.Index.UpdateObjectAccessTime(cacheKey)
		}
		metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "get", "hit", float64(len(data)))
		return o, status.LookupStatusHit, nil
	}
	// Cache Object has been expired but not reaped, go ahead and delete it
	go c.remove(cacheKey, false)
	metrics.ObserveCacheMiss(c.Name, c.Config.Provider)
	return nil, status.LookupStatusKeyMiss, cache.ErrKNF
}

func (c *Cache) remove(cacheKey string, isBulk bool) error {
	fmt.Println("acquired lock", cacheKey)
	err := c.dbh.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		return b.Delete([]byte(cacheKey))
	})
	if err != nil {
		return err
	}
	if !isBulk {
		go c.Index.RemoveObject(cacheKey)
	}
	metrics.ObserveCacheDel(c.Name, c.Config.Provider, 0)
	logger.Debug("bbolt cache key delete", logging.Pairs{"key": cacheKey})
	return nil
}
