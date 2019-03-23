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
	"fmt"
	"sync"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

// Cache defines a a Memory Cache client that conforms to the Cache interface
type Cache struct {
	Name   string
	client sync.Map
	Config *config.CachingConfig
	Index  cache.Index
}

// CacheObject represents a Cached object as stored in the Memory Cache
type CacheObject struct {
	Key        string
	Value      []byte
	Expiration int64
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

// Connect initializes the Cache
func (c *Cache) Connect() error {
	log.Info("memorycache setup", log.Pairs{})
	c.client = sync.Map{}
	go c.Reap()
	return nil
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl int64) error {
	log.Debug("memorycache cache store", log.Pairs{"key": cacheKey, "length": len(data), "ttl": ttl})
	c.client.Store(cacheKey, CacheObject{Key: cacheKey, Value: data, Expiration: time.Now().Unix() + ttl})
	return nil
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string) ([]byte, error) {
	record, ok := c.client.Load(cacheKey)
	if ok {
		log.Debug("memorycache cache retrieve", log.Pairs{"key": cacheKey})
		return record.(CacheObject).Value, nil
	}
	return []byte{}, fmt.Errorf("Value  for key [%s] not in cache", cacheKey)
}

// Reap continually iterates through the cache to find expired elements and removes them
func (c *Cache) Reap() {
	for {
		c.ReapOnce()
		time.Sleep(time.Duration(c.Config.ReapIntervalMS) * time.Millisecond)
	}
}

// ReapOnce makes a single iteration through the cache to to find and remove expired elements
func (c *Cache) ReapOnce() {
	//log.Debug("memorycache cache reaponce", log.Pairs{"reapInterval": c.Config.ReapIntervalMS})
	now := time.Now().Unix()
	c.client.Range(func(k, value interface{}) bool {
		if value.(CacheObject).Expiration < now {
			log.Debug("memorycache cache reap", log.Pairs{"key": k.(string)})
			c.client.Delete(k)
		}
		return true
	})
}

// Close is not used for Cache, and is here to fully prototype the Cache Interface
func (c *Cache) Close() error {
	return nil
}
