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

package redis

import (
	"time"

	"github.com/go-redis/redis"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

// Cache represents a redis cache object that conforms to the Cache interface
type Cache struct {
	Name   string
	Config *config.CachingConfig
	client *redis.Client
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

// Connect connects to the configured Redis endpoint
func (c *Cache) Connect() error {
	log.Info("connecting to redis", log.Pairs{"protocol": c.Config.Redis.Protocol, "Endpoint": c.Config.Redis.Endpoint})
	c.client = redis.NewClient(&redis.Options{
		Network: c.Config.Redis.Protocol,
		Addr:    c.Config.Redis.Endpoint,
	})
	if c.Config.Redis.Password != "" {
		c.client.Options().Password = c.Config.Redis.Password
	}
	return c.client.Ping().Err()
}

// Store places the the data into the Redis Cache using the provided Key and TTL
func (c *Cache) Store(cacheKey string, data []byte, ttl int64) error {
	log.Debug("redis cache store", log.Pairs{"key": cacheKey})
	return c.client.Set(cacheKey, data, time.Second*time.Duration(ttl)).Err()
}

// Retrieve gets data from the Redis Cache using the provided Key
func (c *Cache) Retrieve(cacheKey string) ([]byte, error) {
	log.Debug("redis cache retrieve", log.Pairs{"key": cacheKey})
	res, err := c.client.Get(cacheKey).Result()
	if err != nil {
		return []byte{}, err
	}
	return []byte(res), nil
}

// Remove removes an object in cache, if present
func (c *Cache) Remove(cacheKey string) {
	log.Debug("redis cache remove", log.Pairs{"key": cacheKey})
	c.client.Del(cacheKey)
}

// BulkRemove removes a list of objects from the cache. noLock is not used for Redis
func (c *Cache) BulkRemove(cacheKeys []string, noLock bool) {
	log.Debug("redis cache bulk remove", log.Pairs{})
	c.client.Del(cacheKeys...)
}

// Reap is not used with Redis Cache as it has built-in record lifetime management
func (c *Cache) Reap() {}

// ReapOnce is not used with Redis Cache as it has built-in record lifetime management
func (c *Cache) ReapOnce() {}

// Close disconnects from the Redis Cache
func (c *Cache) Close() error {
	log.Info("closing redis connection", log.Pairs{})
	c.client.Close()
	return nil
}
