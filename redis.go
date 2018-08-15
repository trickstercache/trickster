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

import (
	"sync"
	"time"

	"github.com/go-kit/kit/log/level"
	"github.com/go-redis/redis"
)

// RedisCache represents a redis cache object that conforms to the Cache interface
type RedisCache struct {
	T         *TricksterHandler
	Config    RedisCacheConfig
	client    *redis.Client
	CacheKeys sync.Map
}

// Connect connects to the configured Redis endpoint
func (r *RedisCache) Connect() error {
	level.Info(r.T.Logger).Log("event", "connecting to redis", "protocol", r.Config.Protocol, "Endpoint", r.Config.Endpoint)
	r.client = redis.NewClient(&redis.Options{
		Network: r.Config.Protocol,
		Addr:    r.Config.Endpoint,
	})
	if r.Config.Password != "" {
		r.client.Options().Password = r.Config.Password
	}
	return r.client.Ping().Err()
}

// Store places the the data into the Redis Cache using the provided Key and TTL
func (r *RedisCache) Store(cacheKey string, data string, ttl int64) error {
	level.Debug(r.T.Logger).Log("event", "redis cache store", "key", cacheKey)
	return r.client.Set(cacheKey, data, time.Second*time.Duration(ttl)).Err()
}

// Retrieve gets data from the Redis Cache using the provided Key
func (r *RedisCache) Retrieve(cacheKey string) (string, error) {
	level.Debug(r.T.Logger).Log("event", "redis cache retrieve", "key", cacheKey)
	return r.client.Get(cacheKey).Result()
}

// Reap continually iterates through the cache to find expired elements and removes them
func (r *RedisCache) Reap() {
	for {
		r.ReapOnce()
		time.Sleep(time.Duration(r.T.Config.Caching.ReapSleepMS) * time.Millisecond)
	}
}

func (r *RedisCache) ReapOnce() {
	var keys []string

	r.T.ChannelCreateMtx.Lock()
	for key, _ := range r.T.ResponseChannels {
		keys = append(keys, key)
	}
	r.T.ChannelCreateMtx.Unlock()

	for _, key := range keys {
		_, err := r.client.Get(key).Result()
		if err == redis.Nil {
			level.Debug(r.T.Logger).Log("event", "redis cache reap", "key", key)

			r.T.ChannelCreateMtx.Lock()

			// Close out the channel if it exists
			if _, ok := r.T.ResponseChannels[key]; ok {
				close(r.T.ResponseChannels[key])
				delete(r.T.ResponseChannels, key)
			}

			r.T.ChannelCreateMtx.Unlock()
		}
	}
}

// Close disconnects from the Redis Cache
func (r *RedisCache) Close() error {
	level.Info(r.T.Logger).Log("event", "closing redis connection")
	r.client.Close()
	return nil
}
