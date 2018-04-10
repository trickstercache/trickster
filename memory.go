package main

/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. * See the License for the specific language governing permissions and
* limitations under the License.
 */

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-kit/kit/log/level"
)

// MemoryCache defines a a Memory Cache client that conforms to the Cache interface
type MemoryCache struct {
	T      *TricksterHandler
	client sync.Map
}

// CacheObject represents a Cached object as stored in the Memory Cache
type CacheObject struct {
	Key        string
	Value      string
	Expiration int64
}

// Connect initializes the MemoryCache
func (c *MemoryCache) Connect() error {

	level.Info(c.T.Logger).Log("event", "memorycache setup")
	c.client = sync.Map{}
	go c.Reap()
	return nil
}

// Store places an object in the cache using the specified key and ttl
func (c *MemoryCache) Store(cacheKey string, data string, ttl int64) error {
	level.Debug(c.T.Logger).Log("event", "memorycache cache store", "key", cacheKey)
	c.client.Store(cacheKey, CacheObject{Key: cacheKey, Value: data, Expiration: time.Now().Unix() + ttl})
	return nil
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *MemoryCache) Retrieve(cacheKey string) (string, error) {
	record, ok := c.client.Load(cacheKey)
	if ok {
		level.Debug(c.T.Logger).Log("event", "memorycache cache retrieve", "key", cacheKey)
		return record.(CacheObject).Value, nil
	}
	return "", fmt.Errorf("Value  for key [%s] not in cache", cacheKey)
}

// Reap continually iterates through the cache to find expired elements and removes them
func (c *MemoryCache) Reap() {
	for {
		now := time.Now().Unix()

		c.client.Range(func(k, value interface{}) bool {
			if value.(CacheObject).Expiration < now {

				key := k.(string)
				level.Debug(c.T.Logger).Log("event", "memorycache cache reap", "key", key)

				// Get a lock
				c.T.ChannelCreateMtx.Lock()

				// Delete the key
				c.client.Delete(k)

				// Close out the channel if it exists
				if _, ok := c.T.ResponseChannels[key]; ok {
					close(c.T.ResponseChannels[key])
					delete(c.T.ResponseChannels, key)
				}

				// Unlock
				c.T.ChannelCreateMtx.Unlock()
			}
			return true
		})

		time.Sleep(time.Duration(c.T.Config.Caching.ReapSleepMS) * time.Millisecond)
	}
}

// Close is not used for MemoryCache, and is here to fully prototype the Cache Interface
func (c *MemoryCache) Close() error {
	return nil
}
