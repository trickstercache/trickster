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

package cluster

import (
	"strings"
	"time"

	"github.com/go-redis/redis"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

// Cache represents a redis cluster cache object that conforms to the Cache interface
type Cache struct {
	Name   string
	Config *config.CachingConfig
	client *redis.ClusterClient
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

// Connect connects to the configured Redis endpoint
func (c *Cache) Connect() error {
	log.Info("connecting to redis cluster", log.Pairs{"Endpoints": strings.Join(c.Config.RedisCluster.Endpoints, ",")})
	c.client = redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: c.Config.RedisCluster.Endpoints,
	})
	if c.Config.RedisCluster.Password != "" {
		c.client.Options().Password = c.Config.RedisCluster.Password
	}
	return c.client.Ping().Err()
}

// Store places the the data into the Redis Cluster using the provided Key and TTL
func (c *Cache) Store(cacheKey string, data []byte, ttl int64) error {
	cache.ObserveCacheOperation(c.Name, c.Config.Type, "set", "none", float64(len(data)))
	log.Debug("redis cluster cache store", log.Pairs{"key": cacheKey})
	return c.client.Set(cacheKey, data, time.Second*time.Duration(ttl)).Err()
}

// Retrieve gets data from the Redis Cluster using the provided Key
func (c *Cache) Retrieve(cacheKey string) ([]byte, error) {
	log.Debug("redis cluster cache retrieve", log.Pairs{"key": cacheKey})
	res, err := c.client.Get(cacheKey).Result()
	if err != nil {
		cache.ObserveCacheMiss(cacheKey, c.Name, c.Config.Type)
		return []byte{}, err
	}
	data := []byte(res)
	cache.ObserveCacheOperation(c.Name, c.Config.Type, "get", "hit", float64(len(data)))
	return data, nil
}

// Remove removes an object in cache, if present
func (c *Cache) Remove(cacheKey string) {
	log.Debug("redis cluster cache remove", log.Pairs{"key": cacheKey})
	c.client.Del(cacheKey)
}

// BulkRemove removes a list of objects from the cache. noLock is ignored for Redis Clusteer
func (c *Cache) BulkRemove(cacheKeys []string, noLock bool) {
	log.Debug("redis cluster cache bulk remove", log.Pairs{})
	c.client.Del(cacheKeys...)
}

// Close disconnects from the Redis Cluster
func (c *Cache) Close() error {
	log.Info("closing redis cluster connection", log.Pairs{})
	c.client.Close()
	return nil
}
