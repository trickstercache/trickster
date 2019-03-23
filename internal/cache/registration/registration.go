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

package registration

import (
	"fmt"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/cache/bbolt"
	"github.com/Comcast/trickster/internal/cache/filesystem"
	"github.com/Comcast/trickster/internal/cache/memory"
	"github.com/Comcast/trickster/internal/cache/redis"
	"github.com/Comcast/trickster/internal/config"
)

// Cache Interface Types
const (
	ctMemory     = "memory"
	ctFilesystem = "filesystem"
	ctRedis      = "redis"
	ctBBolt      = "bbolt"
)

// Caches maintains a list of active caches
var Caches = make(map[string]cache.Cache)

// GetCache returns the Cache named cacheName if it exists
func GetCache(cacheName string) (cache.Cache, error) {
	if c, ok := Caches[cacheName]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("Could not find Cache named [%s]", cacheName)
}

// LoadCachesFromConfig iterates the Caching Confi and Connects/Maps each Cache
func LoadCachesFromConfig() {
	for k, v := range config.Caches {
		c := NewCache(&v)
		Caches[k] = c
	}
}

// NewCache returns a Cache object based on the provided config.CachingConfig
func NewCache(cfg *config.CachingConfig) cache.Cache {

	var c cache.Cache

	switch cfg.Type {
	case ctFilesystem:
		c = &filesystem.Cache{Config: cfg}

	case ctRedis:
		c = &redis.Cache{Config: cfg}

	case ctBBolt:
		c = &bbolt.Cache{Config: cfg}
	default:
		// Default to MemoryCache
		c = &memory.Cache{Config: cfg}
	}

	c.Connect()
	return c
}
