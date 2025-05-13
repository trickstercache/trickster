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
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	"github.com/trickstercache/trickster/v2/pkg/cache/internal"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/util/atomicx"
)

func NewCache(name string, config *options.Options) *Cache {
	c := &Cache{
		lockPrefix: fmt.Sprintf("%s.file.", name),
	}
	c.Cache = *internal.NewCache(name, c.lockPrefix, &internal.CacheOptions{
		Options:  config,
		Connect:  c.connect,
		Store:    c.store,
		Retrieve: c.retrieve,
		Delete: func(key string) error {
			return os.Remove(c.getFileName(key))
		},
	})
	return c
}

// Cache describes a Filesystem Cache
type Cache struct {
	internal.Cache
	Index      *index.Index
	lockPrefix string
}

// Connect instantiates the Cache mutex map and starts the Expired Entry Reaper goroutine
func (c *Cache) connect() error {
	logger.Info("filesystem cache setup", logging.Pairs{"name": c.Name,
		"cachePath": c.Config.Filesystem.CachePath})
	if err := makeDirectory(c.Config.Filesystem.CachePath); err != nil {
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

func (c *Cache) storeNoIndex(cacheKey string, data []byte) {
	err := c.store(cacheKey, data, nil, 31536000*time.Second, false)
	if err != nil {
		logger.Error("cache failed to write non-indexed object",
			logging.Pairs{"cacheName": c.Name, "cacheProvider": "filesystem",
				"cacheKey": cacheKey, "objectSize": len(data)})
	}
}

func (c *Cache) store(cacheKey string, data []byte, refData cache.ReferenceObject, ttl time.Duration, updateIndex bool) error {

	if ttl < 1 {
		return fmt.Errorf("invalid ttl: %d", int64(ttl.Seconds()))
	}

	if cacheKey == "" {
		return errors.New("cacheKey required")
	}

	metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "set", "none", float64(len(data)))

	dataFile := c.getFileName(cacheKey)

	nl, _ := c.Locker().Acquire(c.lockPrefix + cacheKey)

	o := &index.Object{Key: cacheKey, Value: data, Expiration: *atomicx.NewTime(time.Now().Add(ttl))}
	err := os.WriteFile(dataFile, o.ToBytes(), os.FileMode(0777))
	if err != nil {
		nl.Release()
		return err
	}
	logger.Debug("filesystem cache store",
		logging.Pairs{"key": cacheKey, "dataFile": dataFile, "indexed": updateIndex})
	if updateIndex {
		c.Index.UpdateObject(o)
	}
	nl.Release()
	return nil
}

// func (c *Cache) retrieve(cacheKey string, allowExpired bool, atime bool) ([]byte, status.LookupStatus, error) {
func (c *Cache) retrieve(cacheKey string, allowExpired bool, atime bool) (*index.Object, status.LookupStatus, error) {

	dataFile := c.getFileName(cacheKey)

	nl, _ := c.Locker().RAcquire(c.lockPrefix + cacheKey)
	data, err := os.ReadFile(dataFile)
	nl.RRelease()

	if err != nil {
		logger.Debug("filesystem cache miss",
			logging.Pairs{"key": cacheKey, "dataFile": dataFile})
		metrics.ObserveCacheMiss(c.Name, c.Config.Provider)
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
		return o, status.LookupStatusHit, nil
	}

	o.Expiration.Store(c.Index.GetExpiration(cacheKey))
	if allowExpired || o.Expiration.Load().IsZero() || o.Expiration.Load().After(time.Now()) {
		logger.Debug("filesystem cache retrieve",
			logging.Pairs{"key": cacheKey, "dataFile": dataFile})
		if atime {
			go c.Index.UpdateObjectAccessTime(cacheKey)
		}
		metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "get", "hit", float64(len(data)))
		return o, status.LookupStatusHit, nil
	}
	// Cache Object has been expired but not reaped, go ahead and delete it
	go c.Remove(cacheKey)
	metrics.ObserveCacheMiss(c.Name, c.Config.Provider)
	return nil, status.LookupStatusKeyMiss, cache.ErrKNF
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
