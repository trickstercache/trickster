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

package filesystem

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/pkg/locks"
)

var lockPrefix string

// Cache describes a Filesystem Cache
type Cache struct {
	Name   string
	Config *config.CachingConfig
	Index  *cache.Index
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

// Connect instantiates the Cache mutex map and starts the Expired Entry Reaper goroutine
func (c *Cache) Connect() error {
	log.Info("filesystem cache setup", log.Pairs{"cachePath": c.Config.Filesystem.CachePath})
	if err := makeDirectory(c.Config.Filesystem.CachePath); err != nil {
		return err
	}
	lockPrefix = c.Name + ".file."
	c.Index = cache.NewIndex(nil, c.BulkRemove, time.Duration(c.Config.ReapIntervalMS)*time.Millisecond)
	return nil
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl int64) error {

	dataFile := c.getFileName(cacheKey)

	locks.Acquire(lockPrefix + cacheKey)
	defer locks.Release(lockPrefix + cacheKey)

	o := cache.Object{Key: cacheKey, Value: data, Expiration: time.Now().Add(time.Duration(ttl) * time.Second)}
	err := ioutil.WriteFile(dataFile, o.ToBytes(), os.FileMode(0777))
	if err != nil {
		return err
	}
	log.Debug("filesystem cache store", log.Pairs{"key": cacheKey, "dataFile": dataFile})
	go c.Index.UpdateObject(o)
	return nil

}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string) ([]byte, error) {

	dataFile := c.getFileName(cacheKey)

	locks.Acquire(lockPrefix + cacheKey)
	defer locks.Release(lockPrefix + cacheKey)

	log.Debug("filesystem cache retrieve", log.Pairs{"key": cacheKey, "dataFile": dataFile})
	data, err := ioutil.ReadFile(dataFile)
	if err != nil {
		return cache.CacheMiss(cacheKey)
	}

	o, err := cache.ObjectFromBytes(data)
	if err != nil {
		return cache.CacheError(cacheKey, "value for key [%s] could not be deserialized from cache")
	}

	if o.Expiration.After(time.Now()) {
		log.Debug("memorycache cache retrieve", log.Pairs{"cacheKey": cacheKey})
		c.Index.UpdateObjectAccessTime(cacheKey)
		return o.Value, nil
	}
	// Cache Object has been expired but not reaped, go ahead and delete it
	go c.Remove(cacheKey)
	return cache.CacheMiss(cacheKey)

}

// Remove removes an object from the cache
func (c *Cache) Remove(cacheKey string) {

	locks.Acquire(lockPrefix + cacheKey)
	defer locks.Release(lockPrefix + cacheKey)

	if err := os.Remove(c.getFileName(cacheKey)); err == nil {
		c.Index.RemoveObject(cacheKey, false)
	}
}

// BulkRemove removes a list of objects from the cache
func (c *Cache) BulkRemove(cacheKeys []string, noLock bool) {
	for _, cacheKey := range cacheKeys {
		locks.Acquire(lockPrefix + cacheKey)
		defer locks.Release(lockPrefix + cacheKey)

		if err := os.Remove(c.getFileName(cacheKey)); err == nil {
			c.Index.RemoveObject(cacheKey, noLock)
		}
	}
}

// Close is not used for Cache
func (c *Cache) Close() error {
	return nil
}

func (c *Cache) getFileName(cacheKey string) string {
	prefix := strings.Replace(c.Config.Filesystem.CachePath+"/"+cacheKey+".", "//", "/", 1)
	return prefix + "data"
}

// writeable returns true if the path is writeable by the calling process.
func writeable(path string) bool {
	return unix.Access(path, unix.W_OK) == nil
}

// makeDirectory creates a directory on the filesystem and exits the application in the event of a failure.
func makeDirectory(path string) error {
	err := os.MkdirAll(path, 0755)
	if err != nil || !writeable(path) {
		return fmt.Errorf("[%s] directory is not writeable by the trickster: %v", path, err)
	}

	return nil
}
