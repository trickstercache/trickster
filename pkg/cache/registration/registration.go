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

// Package registration handles the registration of cache implementations
// to be used by proxy cache handlers
package registration

import (
	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/badger"
	"github.com/trickstercache/trickster/v2/pkg/cache/bbolt"
	"github.com/trickstercache/trickster/v2/pkg/cache/filesystem"
	"github.com/trickstercache/trickster/v2/pkg/cache/memory"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/redis"
	"github.com/trickstercache/trickster/v2/pkg/locks"
)

// Cache Interface Types
const (
	ctFilesystem = "filesystem"
	ctRedis      = "redis"
	ctBBolt      = "bbolt"
	ctBadger     = "badger"
)

// Caches maintains a list of active caches
// var Caches = make(map[string]cache.Cache)

// GetCache returns the Cache named cacheName if it exists
// func GetCache(cacheName string) (cache.Cache, error) {
// 	if c, ok := Caches[cacheName]; ok {
// 		return c, nil
// 	}
// 	return nil, fmt.Errorf("Could not find Cache named [%s]", cacheName)
// }

// LoadCachesFromConfig iterates the Caching Config and Connects/Maps each Cache
func LoadCachesFromConfig(conf *config.Config, logger interface{}) map[string]cache.Cache {
	caches := make(map[string]cache.Cache)
	for k, v := range conf.Caches {
		c := NewCache(k, v, logger)
		caches[k] = c
	}
	return caches
}

// CloseCaches iterates the set of caches and closes each
func CloseCaches(caches map[string]cache.Cache) error {
	for _, c := range caches {
		if err := c.Close(); err != nil {
			return err
		}
	}
	return nil
}

// NewCache returns a Cache object based on the provided config.CachingConfig
func NewCache(cacheName string, cfg *options.Options, logger interface{}) cache.Cache {

	var c cache.Cache

	switch cfg.Provider {
	case ctFilesystem:
		c = &filesystem.Cache{Name: cacheName, Config: cfg, Logger: logger}
	case ctRedis:
		c = &redis.Cache{Name: cacheName, Config: cfg, Logger: logger}
	case ctBBolt:
		c = &bbolt.Cache{Name: cacheName, Config: cfg, Logger: logger}
	case ctBadger:
		c = &badger.Cache{Name: cacheName, Config: cfg, Logger: logger}
	default:
		// Default to MemoryCache
		c = &memory.Cache{Name: cacheName, Config: cfg, Logger: logger}
	}

	c.SetLocker(locks.NewNamedLocker())
	c.Connect()
	return c
}
