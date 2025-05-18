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

// Package registry handles the registration of cache implementations
// to be used by proxy cache handlers
package registry

import (
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/badger"
	"github.com/trickstercache/trickster/v2/pkg/cache/bbolt"
	"github.com/trickstercache/trickster/v2/pkg/cache/filesystem"
	"github.com/trickstercache/trickster/v2/pkg/cache/manager"
	"github.com/trickstercache/trickster/v2/pkg/cache/memory"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/redis"
	"github.com/trickstercache/trickster/v2/pkg/config"
)

// Cache Interface Types
const (
	ctFilesystem = "filesystem"
	ctRedis      = "redis"
	ctBBolt      = "bbolt"
	ctBadger     = "badger"
)

// LoadCachesFromConfig iterates the Caching Config and Connects/Maps each Cache
func LoadCachesFromConfig(conf *config.Config) cache.Lookup {
	caches := make(cache.Lookup)
	for k, v := range conf.Caches {
		c := NewCache(k, v)
		caches[k] = c
	}
	return caches
}

// CloseCaches iterates the set of caches and closes each
func CloseCaches(caches cache.Lookup) error {
	for _, c := range caches {
		if err := c.Close(); err != nil {
			return err
		}
	}
	return nil
}

// NewCache returns a Cache object based on the provided config.CachingConfig
func NewCache(cacheName string, cfg *options.Options) cache.Cache {

	var c cache.Cache

	switch cfg.Provider {
	case ctFilesystem:
		c = manager.NewCache(filesystem.NewCache(cacheName, cfg), manager.CacheOptions{
			UseIndex: true,
		}, cfg)
	case ctRedis:
		c = manager.NewCache(redis.New(cacheName, cfg), manager.CacheOptions{}, cfg)
	case ctBBolt:
		c = manager.NewCache(bbolt.New(cacheName, "", "", cfg), manager.CacheOptions{
			UseIndex: true,
		}, cfg)
	case ctBadger:
		c = manager.NewCache(badger.New(cacheName, cfg), manager.CacheOptions{}, cfg)
	default:
		// Default to MemoryCache
		c = manager.NewCache(memory.New(cacheName, cfg), manager.CacheOptions{
			UseIndex: true,
		}, cfg)
	}
	c.Connect()
	return c
}
