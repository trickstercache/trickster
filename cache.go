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

package main

// Cache is the interface for the supported caching fabrics
// When making new cache types, Retrieve() must return an error on cache miss
type Cache interface {
	Connect() error
	Store(cacheKey string, data string, ttl int64) error
	Retrieve(cacheKey string) (string, error)
	Reap()
	Close() error
}

func getCache(t *TricksterHandler) Cache {
	switch t.Config.Caching.CacheType {
	case ctFilesystem:
		return &FilesystemCache{Config: t.Config.Caching.Filesystem, T: t}
	case ctRedis:
		return &RedisCache{Config: t.Config.Caching.Redis, T: t}
	default:
		return &MemoryCache{T: t}
	}
}
