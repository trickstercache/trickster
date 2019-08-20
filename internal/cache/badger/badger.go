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

package badger

import (
	"time"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/dgraph-io/badger"
)

// Cache describes a Badger Cache
type Cache struct {
	Name   string
	Config *config.CachingConfig
	dbh    *badger.DB
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

// Connect opens the configured Badger key-value store
func (c *Cache) Connect() error {
	log.Info("badger cache setup", log.Pairs{"cacheDir": c.Config.Badger.Directory})

	opts := badger.DefaultOptions
	opts.Dir = c.Config.Badger.Directory
	opts.ValueDir = c.Config.Badger.ValueDirectory

	var err error
	c.dbh, err = badger.Open(opts)
	if err != nil {
		return err
	}

	return nil
}

// Store places the the data into the Badger Cache using the provided Key and TTL
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	cache.ObserveCacheOperation(c.Name, c.Config.Type, "set", "none", float64(len(data)))
	log.Debug("badger cache store", log.Pairs{"key": cacheKey, "ttl": ttl})
	return c.dbh.Update(func(txn *badger.Txn) error {
		return txn.SetWithTTL([]byte(cacheKey), data, ttl)
	})
}

// Retrieve gets data from the Badger Cache using the provided Key
func (c *Cache) Retrieve(cacheKey string) ([]byte, error) {
	var data []byte
	err := c.dbh.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(cacheKey))
		if err != nil {
			return err
		}
		data, err = item.ValueCopy(nil)
		return err

	})

	if err != nil {
		log.Debug("badger cache miss", log.Pairs{"key": cacheKey})
		cache.ObserveCacheMiss(cacheKey, c.Name, c.Config.Type)
	} else {
		log.Debug("badger cache retrieve", log.Pairs{"key": cacheKey})
		cache.ObserveCacheOperation(c.Name, c.Config.Type, "get", "hit", float64(len(data)))
	}

	return data, err
}

// Remove removes an object in cache, if present
func (c *Cache) Remove(cacheKey string) {
	log.Debug("badger cache remove", log.Pairs{"key": cacheKey})
	c.dbh.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(cacheKey))
	})
	cache.ObserveCacheDel(c.Name, c.Config.Type, 0)
}

// BulkRemove removes a list of objects from the cache. noLock is not used for Badger
func (c *Cache) BulkRemove(cacheKeys []string, noLock bool) {
	log.Debug("badger cache bulk remove", log.Pairs{})

	c.dbh.Update(func(txn *badger.Txn) error {
		for _, key := range cacheKeys {
			if err := txn.Delete([]byte(key)); err != nil {
				return err
			}
			cache.ObserveCacheDel(c.Name, c.Config.Type, 0)
		}
		return nil
	})
}

// Close closes the Badger Cache
func (c *Cache) Close() error {
	return c.dbh.Close()
}
