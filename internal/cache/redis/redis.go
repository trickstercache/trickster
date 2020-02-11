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

// Package redis is the redis implementation of the Trickster Cache
// and supports Standalone, Sentinel and Cluster
package redis

import (
	"time"

	"github.com/go-redis/redis"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/cache/status"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

// Redis is the string "redis"
const Redis = "redis"

// Cache represents a redis cache object that conforms to the Cache interface
type Cache struct {
	Name   string
	Config *config.CachingConfig

	client redis.Cmdable
	closer func() error
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

// Connect connects to the configured Redis endpoint
func (c *Cache) Connect() error {
	log.Info("connecting to redis", log.Pairs{"protocol": c.Config.Redis.Protocol, "Endpoint": c.Config.Redis.Endpoint})

	switch c.Config.Redis.ClientType {
	case "sentinel":
		opts, err := c.sentinelOpts()
		if err != nil {
			return err
		}
		client := redis.NewFailoverClient(opts)
		c.closer = client.Close
		c.client = client
	case "cluster":
		opts, err := c.clusterOpts()
		if err != nil {
			return err
		}
		client := redis.NewClusterClient(opts)
		c.closer = client.Close
		c.client = client
	default:
		opts, err := c.clientOpts()
		if err != nil {
			return err
		}
		client := redis.NewClient(opts)
		c.closer = client.Close
		c.client = client
	}
	return c.client.Ping().Err()
}

// Store places the the data into the Redis Cache using the provided Key and TTL
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	cache.ObserveCacheOperation(c.Name, c.Config.CacheType, "set", "none", float64(len(data)))
	log.Debug("redis cache store", log.Pairs{"key": cacheKey})
	return c.client.Set(cacheKey, data, ttl).Err()
}

// Retrieve gets data from the Redis Cache using the provided Key
// because Redis manages Object Expiration internally, allowExpired is not used.
func (c *Cache) Retrieve(cacheKey string, allowExpired bool) ([]byte, status.LookupStatus, error) {
	res, err := c.client.Get(cacheKey).Result()

	if err == nil {
		data := []byte(res)
		log.Debug("redis cache retrieve", log.Pairs{"key": cacheKey})
		cache.ObserveCacheOperation(c.Name, c.Config.CacheType, "get", "hit", float64(len(data)))
		return data, status.LookupStatusHit, nil
	}

	if err == redis.Nil {
		log.Debug("redis cache miss", log.Pairs{"key": cacheKey})
		cache.ObserveCacheMiss(cacheKey, c.Name, c.Config.CacheType)
		return nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}

	log.Debug("redis cache retrieve failed", log.Pairs{"key": cacheKey, "reason": err.Error()})
	cache.ObserveCacheMiss(cacheKey, c.Name, c.Config.CacheType)
	return nil, status.LookupStatusError, err
}

// Remove removes an object in cache, if present
func (c *Cache) Remove(cacheKey string) {
	log.Debug("redis cache remove", log.Pairs{"key": cacheKey})
	c.client.Del(cacheKey)
	cache.ObserveCacheDel(c.Name, c.Config.CacheType, 0)
}

// SetTTL updates the TTL for the provided cache object
func (c *Cache) SetTTL(cacheKey string, ttl time.Duration) {
	c.client.Expire(cacheKey, ttl)
}

// BulkRemove removes a list of objects from the cache. noLock is not used for Redis
func (c *Cache) BulkRemove(cacheKeys []string, noLock bool) {
	log.Debug("redis cache bulk remove", log.Pairs{})
	c.client.Del(cacheKeys...)
	cache.ObserveCacheDel(c.Name, c.Config.CacheType, float64(len(cacheKeys)))
}

// Close disconnects from the Redis Cache
func (c *Cache) Close() error {
	log.Info("closing redis connection", log.Pairs{})
	return c.closer()
}

func durationFromMS(input int) time.Duration {
	return time.Duration(int64(input)) * time.Millisecond
}
