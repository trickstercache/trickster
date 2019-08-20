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
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis"

	"github.com/Comcast/trickster/internal/util/log"
)

// Connect connects to the configured Redis endpoint
func (c *Cache) clusterConnect() error {
	log.Info(c.logger, "connecting to redis cluster", log.Pairs{"Endpoints": strings.Join(c.Config.Redis.Endpoints, ",")})
	opts, err := c.clusterOpts()
	if err != nil {
		return err
	}
	c.clusterClient = redis.NewClusterClient(opts)
	return c.client.Ping().Err()
}

// Store places the the data into the Redis Cluster using the provided Key and TTL
func (c *Cache) clusterStore(cacheKey string, data []byte, ttl time.Duration) error {
	return c.clusterClient.Set(cacheKey, data, ttl).Err()
}

// Retrieve gets data from the Redis Cluster using the provided Key
func (c *Cache) clusterRetrieve(cacheKey string) (string, error) {
	return c.clusterClient.Get(cacheKey).Result()
}

// Remove removes an object in cache, if present
func (c *Cache) clusterRemove(cacheKey string) {
	c.clusterClient.Del(cacheKey)
}

// BulkRemove removes a list of objects from the cache. noLock is ignored for Redis Clusteer
func (c *Cache) clusterBulkRemove(cacheKeys []string, noLock bool) {
	c.clusterClient.Del(cacheKeys...)
}

// Close disconnects from the Redis Cluster
func (c *Cache) clusterClose() error {
	log.Info(c.logger, "closing redis cluster connection", log.Pairs{})
	c.clusterClient.Close()
	return nil
}

func (c *Cache) clusterOpts() (*redis.ClusterOptions, error) {

	if len(c.Config.Redis.Endpoints) == 0 {
		return nil, fmt.Errorf("Invalid 'endpoints' config")
	}

	o := &redis.ClusterOptions{
		Addrs: c.Config.Redis.Endpoints,
	}

	if c.Config.Redis.Password != "" {
		o.Password = c.Config.Redis.Password
	}

	if c.Config.Redis.MaxRetries != 0 {
		o.MaxRetries = c.Config.Redis.MaxRetries
	}

	if c.Config.Redis.MinRetryBackoffMS != 0 {
		o.MinRetryBackoff = durationFromMS(c.Config.Redis.MinRetryBackoffMS)
	}

	if c.Config.Redis.MaxRetryBackoffMS != 0 {
		o.MaxRetryBackoff = durationFromMS(c.Config.Redis.MaxRetryBackoffMS)
	}

	if c.Config.Redis.DialTimeoutMS != 0 {
		o.DialTimeout = durationFromMS(c.Config.Redis.DialTimeoutMS)
	}

	if c.Config.Redis.ReadTimeoutMS != 0 {
		o.ReadTimeout = durationFromMS(c.Config.Redis.ReadTimeoutMS)
	}

	if c.Config.Redis.WriteTimeoutMS != 0 {
		o.WriteTimeout = durationFromMS(c.Config.Redis.WriteTimeoutMS)
	}

	if c.Config.Redis.PoolSize != 0 {
		o.PoolSize = c.Config.Redis.PoolSize
	}

	if c.Config.Redis.MinIdleConns != 0 {
		o.MinIdleConns = c.Config.Redis.MinIdleConns
	}

	if c.Config.Redis.MaxConnAgeMS != 0 {
		o.MaxConnAge = durationFromMS(c.Config.Redis.MaxConnAgeMS)
	}

	if c.Config.Redis.PoolTimeoutMS != 0 {
		o.PoolTimeout = durationFromMS(c.Config.Redis.PoolTimeoutMS)
	}

	if c.Config.Redis.IdleTimeoutMS != 0 {
		o.IdleTimeout = durationFromMS(c.Config.Redis.IdleTimeoutMS)
	}

	if c.Config.Redis.IdleCheckFrequencyMS != 0 {
		o.IdleCheckFrequency = durationFromMS(c.Config.Redis.IdleCheckFrequencyMS)
	}

	return o, nil
}
