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

// Package filesystem is the filesystem implementation of the Trickster Cache
package filesystem

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

// Cache describes a Filesystem Cache
type Cache struct {
	Name       string
	Config     *options.Options
	Index      *index.Index
	Logger     interface{}
	locker     locks.NamedLocker
	lockPrefix string
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
	tl.Info(c.Logger, "filesystem cache setup", tl.Pairs{"name": c.Name,
		"cachePath": c.Config.Filesystem.CachePath})
	if err := makeDirectory(c.Config.Filesystem.CachePath); err != nil {
		return err
	}
	c.lockPrefix = c.Name + ".file."

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
		tl.Error(c.Logger,
			"cache failed to write non-indexed object", tl.Pairs{"cacheName": c.Name,
				"cacheProvider": "filesystem", "cacheKey": cacheKey, "objectSize": len(data)})
	}
}

func (c *Cache) store(cacheKey string, data []byte, ttl time.Duration, updateIndex bool) error {

	if ttl < 1 {
		return fmt.Errorf("invalid ttl: %d", int64(ttl.Seconds()))
	}

	if cacheKey == "" {
		return fmt.Errorf("cacheKey required")
	}

	metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "set", "none", float64(len(data)))

	dataFile := c.getFileName(cacheKey)

	nl, _ := c.locker.Acquire(c.lockPrefix + cacheKey)

	o := &index.Object{Key: cacheKey, Value: data, Expiration: time.Now().Add(ttl)}
	err := os.WriteFile(dataFile, o.ToBytes(), os.FileMode(0777))
	if err != nil {
		nl.Release()
		return err
	}
	tl.Debug(c.Logger, "filesystem cache store",
		tl.Pairs{"key": cacheKey, "dataFile": dataFile, "indexed": updateIndex})
	if updateIndex {
		c.Index.UpdateObject(o)
	}
	nl.Release()
	return nil
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string, allowExpired bool) ([]byte, status.LookupStatus, error) {
	return c.retrieve(cacheKey, allowExpired, true)
}

func (c *Cache) retrieve(cacheKey string, allowExpired bool, atime bool) ([]byte, status.LookupStatus, error) {

	dataFile := c.getFileName(cacheKey)

	nl, _ := c.locker.RAcquire(c.lockPrefix + cacheKey)
	data, err := os.ReadFile(dataFile)
	nl.RRelease()

	if err != nil {
		tl.Debug(c.Logger, "filesystem cache miss", tl.Pairs{"key": cacheKey, "dataFile": dataFile})
		metrics.ObserveCacheMiss(cacheKey, c.Name, c.Config.Provider)
		return nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}

	o, err := index.ObjectFromBytes(data)
	if err != nil {

		_, err2 := metrics.CacheError(cacheKey, c.Name, c.Config.Provider,
			"value for key [%s] could not be deserialized from cache")
		return nil, status.LookupStatusError, err2
	}

	// if retrieve() is being called to load the index, the index will be nil, so just return the value
	// so as to instantiate the index
	if c.Index == nil {
		return o.Value, status.LookupStatusHit, nil
	}

	o.Expiration = c.Index.GetExpiration(cacheKey)
	if allowExpired || o.Expiration.IsZero() || o.Expiration.After(time.Now()) {
		tl.Debug(c.Logger, "filesystem cache retrieve", tl.Pairs{"key": cacheKey, "dataFile": dataFile})
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

// Remove removes an object from the cache
func (c *Cache) Remove(cacheKey string) {
	c.remove(cacheKey, false)
}

func (c *Cache) remove(cacheKey string, isBulk bool) {
	nl, _ := c.locker.Acquire(c.lockPrefix + cacheKey)
	err := os.Remove(c.getFileName(cacheKey))
	nl.Release()
	if err == nil && !isBulk {
		go c.Index.RemoveObject(cacheKey)
	}
	metrics.ObserveCacheDel(c.Name, c.Config.Provider, 0)
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

// Close is not used for Cache
func (c *Cache) Close() error {
	if c.Index != nil {
		c.Index.Close()
	}
	return nil
}

func (c *Cache) getFileName(cacheKey string) string {
	prefix := strings.Replace(c.Config.Filesystem.CachePath+"/"+cacheKey+".", "//", "/", 1)
	return prefix + "data"
}

// makeDirectory creates a directory on the filesystem and returns the error in the event of a failure.
func makeDirectory(path string) error {
	err := os.MkdirAll(path, 0755)
	if err == nil {
		s := ""
		if !strings.HasSuffix(path, "/") {
			s = "/"
		}
		// verify writability by attempting to touch a test file in the cache path
		tf := path + s + ".test." + strconv.FormatInt(time.Now().Unix(), 10)
		err = os.WriteFile(tf, []byte(""), 0600)
		if err == nil {
			os.Remove(tf)
		}
	}
	if err != nil {
		return fmt.Errorf("[%s] directory is not writeable by trickster: %v", path, err)
	}
	return nil
}
