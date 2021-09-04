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

// Package redis is the redis implementation of the Trickster Cache
// and supports Standalone, Sentinel and Cluster
package redis

import (
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"

	"github.com/go-redis/redis"
)

// Redis is the string "redis"
const Redis = "redis"

// Cache represents a redis cache object that conforms to the Cache interface
type Cache struct {
	Name   string
	Config *options.Options
	Logger interface{}
	locker locks.NamedLocker

	client redis.Cmdable
	closer func() error
}

// Locker returns the cache's locker
func (c *Cache) Locker() locks.NamedLocker {
	return c.locker
}

// SetLocker sets the cache's locker
func (c *Cache) SetLocker(l locks.NamedLocker) {
	c.locker = l
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *options.Options {
	return c.Config
}

// Connect connects to the configured Redis endpoint
func (c *Cache) Connect() error {
	tl.Info(c.Logger, "connecting to redis",
		tl.Pairs{"protocol": c.Config.Redis.Protocol, "Endpoint": c.Config.Redis.Endpoint})

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
	metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "set", "none", float64(len(data)))
	tl.Debug(c.Logger, "redis cache store", tl.Pairs{"key": cacheKey})
	return c.client.Set(cacheKey, data, ttl).Err()
}

// Retrieve gets data from the Redis Cache using the provided Key
// because Redis manages Object Expiration internally, allowExpired is not used.
func (c *Cache) Retrieve(cacheKey string, allowExpired bool) ([]byte, status.LookupStatus, error) {
	res, err := c.client.Get(cacheKey).Result()

	if err == nil {
		data := []byte(res)
		tl.Debug(c.Logger, "redis cache retrieve", tl.Pairs{"key": cacheKey})
		metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "get", "hit", float64(len(data)))
		return data, status.LookupStatusHit, nil
	}

	if err == redis.Nil {
		tl.Debug(c.Logger, "redis cache miss", tl.Pairs{"key": cacheKey})
		metrics.ObserveCacheMiss(cacheKey, c.Name, c.Config.Provider)
		return nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}

	tl.Debug(c.Logger, "redis cache retrieve failed", tl.Pairs{"key": cacheKey, "reason": err.Error()})
	metrics.ObserveCacheMiss(cacheKey, c.Name, c.Config.Provider)
	return nil, status.LookupStatusError, err
}

// Remove removes an object in cache, if present
func (c *Cache) Remove(cacheKey string) {
	tl.Debug(c.Logger, "redis cache remove", tl.Pairs{"key": cacheKey})
	c.client.Del(cacheKey)
	metrics.ObserveCacheDel(c.Name, c.Config.Provider, 0)
}

// SetTTL updates the TTL for the provided cache object
func (c *Cache) SetTTL(cacheKey string, ttl time.Duration) {
	c.client.Expire(cacheKey, ttl)
}

// BulkRemove removes a list of objects from the cache. noLock is not used for Redis
func (c *Cache) BulkRemove(cacheKeys []string) {
	tl.Debug(c.Logger, "redis cache bulk remove", tl.Pairs{})
	c.client.Del(cacheKeys...)
	metrics.ObserveCacheDel(c.Name, c.Config.Provider, float64(len(cacheKeys)))
}

// Close disconnects from the Redis Cache
func (c *Cache) Close() error {
	tl.Info(c.Logger, "closing redis connection", tl.Pairs{})
	return c.closer()
}

func durationFromMS(input int) time.Duration {
	return time.Duration(int64(input)) * time.Millisecond
}
