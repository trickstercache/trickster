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

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

// Cache represents a redis cache object that conforms to the Cache interface
type Cache struct {
	Name   string
	Config *config.CachingConfig

	client        *redis.Client
	clusterClient *redis.ClusterClient

	connectFunc    func() error
	closeFunc      func() error
	retrieveFunc   func(string) (string, error)
	storeFunc      func(string, []byte, time.Duration) error
	removeFunc     func(string)
	bulkRemoveFunc func([]string, bool)
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

func (c *Cache) mapOpFuncs() {
	switch c.Config.Redis.ClientType {
	case "sentinel":
		c.connectFunc = c.sentinelConnect
		c.closeFunc = c.sentinelClose
		c.retrieveFunc = c.sentinelRetrieve
		c.storeFunc = c.sentinelStore
		c.removeFunc = c.sentinelRemove
		c.bulkRemoveFunc = c.sentinelBulkRemove
	case "cluster":
		c.connectFunc = c.clusterConnect
		c.closeFunc = c.clusterClose
		c.retrieveFunc = c.clusterRetrieve
		c.storeFunc = c.clusterStore
		c.removeFunc = c.clusterRemove
		c.bulkRemoveFunc = c.clusterBulkRemove
	default:
		c.connectFunc = c.clientConnect
		c.closeFunc = c.clientClose
		c.retrieveFunc = c.clientRetrieve
		c.storeFunc = c.clientStore
		c.removeFunc = c.clientRemove
		c.bulkRemoveFunc = c.clientBulkRemove
	}
}

// Connect connects to the configured Redis endpoint
func (c *Cache) Connect() error {
	c.mapOpFuncs()
	return c.connectFunc()
}

// Store places the the data into the Redis Cache using the provided Key and TTL
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	cache.ObserveCacheOperation(c.Name, c.Config.CacheType, "set", "none", float64(len(data)))
	log.Debug("redis cache store", log.Pairs{"key": cacheKey})
	return c.storeFunc(cacheKey, data, ttl)
}

// Retrieve gets data from the Redis Cache using the provided Key
func (c *Cache) Retrieve(cacheKey string) ([]byte, error) {
	res, err := c.retrieveFunc(cacheKey)
	if err != nil {
		log.Debug("redis cache miss", log.Pairs{"key": cacheKey})
		cache.ObserveCacheMiss(cacheKey, c.Name, c.Config.CacheType)
		return []byte{}, err
	}
	data := []byte(res)
	log.Debug("redis cache retrieve", log.Pairs{"key": cacheKey})
	cache.ObserveCacheOperation(c.Name, c.Config.CacheType, "get", "hit", float64(len(data)))
	return data, nil
}

// Remove removes an object in cache, if present
func (c *Cache) Remove(cacheKey string) {
	log.Debug("redis cache remove", log.Pairs{"key": cacheKey})
	c.removeFunc(cacheKey)
}

// BulkRemove removes a list of objects from the cache. noLock is not used for Redis
func (c *Cache) BulkRemove(cacheKeys []string, noLock bool) {
	log.Debug("redis cache bulk remove", log.Pairs{})
	c.bulkRemoveFunc(cacheKeys, noLock)
}

// Close disconnects from the Redis Cache
func (c *Cache) Close() error {
	log.Info("closing redis connection", log.Pairs{})
	return c.closeFunc()
}

func durationFromMS(input int) time.Duration {
	return time.Duration(int64(input)) * time.Millisecond
}
