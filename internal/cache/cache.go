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

package cache

import (
	"fmt"

	"github.com/Comcast/trickster/internal/config"
)

// Cache is the interface for the supported caching fabrics
// When making new cache types, Retrieve() must return an error on cache miss
type Cache interface {
	Connect() error
	Store(cacheKey string, data []byte, ttl int64) error
	Retrieve(cacheKey string) ([]byte, error)
	Remove(cacheKey string)
	BulkRemove(cacheKeys []string, noLock bool)
	Close() error
	Configuration() *config.CachingConfig
}

// CacheMiss returns a standard Cache Miss response
func ObserveCacheMiss(cacheKey, cacheName, cacheType string) ([]byte, error) {
	ObserveCacheOperation(cacheName, cacheType, "get", "miss", 0)
	return nil, fmt.Errorf("value  for key [%s] not in cache", cacheKey)
}

// CacheError returns an empty cache object and the formatted error
func CacheError(cacheKey, cacheName, cacheType string, msg string) ([]byte, error) {
	ObserveCacheEvent(cacheName, cacheType, "error", msg)
	return nil, fmt.Errorf(msg, cacheKey)
}
