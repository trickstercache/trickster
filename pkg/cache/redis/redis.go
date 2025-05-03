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
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"

	"github.com/go-redis/redis"
)

var (
	// CacheClient implements the cache.Client interface
	_ cache.Client = &CacheClient{}
)

// Redis is the string "redis"
const Redis = "redis"

// CacheClient represents a redis cache client that conforms to the cache.Client interface
type CacheClient struct {
	Name   string
	Config *options.Options
	client redis.Cmdable
	closer func() error
}

func New(name string, cfg *options.Options) *CacheClient {
	c := &CacheClient{
		Name:   name,
		Config: cfg,
	}
	return c
}

// Connect connects to the configured Redis endpoint
func (c *CacheClient) Connect() error {
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

func (c *CacheClient) Remove(cacheKeys ...string) error {
	return c.client.Del(cacheKeys...).Err()
}

// Store places the the data into the Redis Cache using the provided Key and TTL
func (c *CacheClient) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return c.client.Set(cacheKey, data, ttl).Err()
}

// Retrieve gets data from the Redis Cache using the provided Key
// because Redis manages Object Expiration internally, allowExpired is not used.
func (c *CacheClient) Retrieve(cacheKey string) ([]byte, status.LookupStatus, error) {
	res, err := c.client.Get(cacheKey).Result()

	if err == nil {
		data := []byte(res)
		return data, status.LookupStatusHit, nil
	}

	if err == redis.Nil {
		return nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}

	return nil, status.LookupStatusError, err
}

func (c *CacheClient) Close() error {
	return c.closer()
}
