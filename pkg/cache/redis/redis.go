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
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	"github.com/trickstercache/trickster/v2/pkg/cache/internal"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"

	"github.com/go-redis/redis"
)

// Redis is the string "redis"
const Redis = "redis"

// Cache represents a redis cache object that conforms to the Cache interface
type Cache struct {
	internal.Cache

	client redis.Cmdable
	closer func() error
}

func New(name string, cfg *options.Options) *Cache {
	c := &Cache{}
	c.Cache = *internal.NewCache(name, "", &internal.CacheOptions{
		Options:  cfg,
		Connect:  c.connect,
		Store:    c.store,
		Retrieve: c.retrieve,
		Delete: func(cacheKey string) error {
			return c.client.Del(cacheKey).Err()
		},
		SetTTL: c.setTTL,
		Close:  c.closer,
	})
	c.SetLocker(locks.NewNamedLocker())
	return c
}

// Connect connects to the configured Redis endpoint
func (c *Cache) connect() error {
	logger.Info("connecting to redis",
		logging.Pairs{"protocol": c.Config.Redis.Protocol,
			"endpoint": c.Config.Redis.Endpoint})

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
	c.Cache.Options.Close = c.closer
	return c.client.Ping().Err()
}

// Store places the the data into the Redis Cache using the provided Key and TTL
func (c *Cache) store(cacheKey string, data []byte, refData cache.ReferenceObject, ttl time.Duration, updateIndex bool) error {
	metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "set", "none", float64(len(data)))
	logger.Debug("redis cache store", logging.Pairs{"key": cacheKey})
	return c.client.Set(cacheKey, data, ttl).Err()
}

// Retrieve gets data from the Redis Cache using the provided Key
// because Redis manages Object Expiration internally, allowExpired is not used.
func (c *Cache) retrieve(cacheKey string, allowExpired bool, atime bool) (*index.Object, status.LookupStatus, error) {
	res, err := c.client.Get(cacheKey).Result()

	if err == nil {
		data := []byte(res)
		logger.Debug("redis cache retrieve", logging.Pairs{"key": cacheKey})
		metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "get", "hit", float64(len(data)))
		return &index.Object{Key: cacheKey, Value: data}, status.LookupStatusHit, nil
	}

	if err == redis.Nil {
		logger.Debug("redis cache miss", logging.Pairs{"key": cacheKey})
		metrics.ObserveCacheMiss(c.Name, c.Config.Provider)
		return nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}

	logger.Debug("redis cache retrieve failed", logging.Pairs{"key": cacheKey, "reason": err.Error()})
	metrics.ObserveCacheMiss(c.Name, c.Config.Provider)
	return nil, status.LookupStatusError, err
}

// SetTTL updates the TTL for the provided cache object
func (c *Cache) setTTL(cacheKey string, ttl time.Duration) {
	c.client.Expire(cacheKey, ttl)
}
