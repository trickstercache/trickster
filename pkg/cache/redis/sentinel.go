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

package redis

import (
	"crypto/tls"

	redis "github.com/redis/go-redis/v9"
)

func (c *CacheClient) sentinelOpts() (*redis.FailoverOptions, error) {

	if len(c.Config.Redis.Endpoints) == 0 {
		return nil, ErrInvalidEndpointsConfig
	}

	if c.Config.Redis.SentinelMaster == "" {
		return nil, ErrInvalidSentinalMasterConfig
	}

	o := &redis.FailoverOptions{
		SentinelAddrs: c.Config.Redis.Endpoints,
		MasterName:    c.Config.Redis.SentinelMaster,
	}

	if c.Config.Redis.UseTLS {
		o.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	if c.Config.Redis.Username != "" {
		o.Username = c.Config.Redis.Username
	}

	if c.Config.Redis.Password != "" {
		o.Password = string(c.Config.Redis.Password)
	}

	if c.Config.Redis.DB != 0 {
		o.DB = c.Config.Redis.DB
	}

	if c.Config.Redis.MaxRetries != 0 {
		o.MaxRetries = c.Config.Redis.MaxRetries
	}

	if c.Config.Redis.MinRetryBackoff != 0 {
		o.MinRetryBackoff = c.Config.Redis.MinRetryBackoff
	}

	if c.Config.Redis.MaxRetryBackoff != 0 {
		o.MaxRetryBackoff = c.Config.Redis.MaxRetryBackoff
	}

	if c.Config.Redis.DialTimeout != 0 {
		o.DialTimeout = c.Config.Redis.DialTimeout
	}

	if c.Config.Redis.ReadTimeout != 0 {
		o.ReadTimeout = c.Config.Redis.ReadTimeout
	}

	if c.Config.Redis.WriteTimeout != 0 {
		o.WriteTimeout = c.Config.Redis.WriteTimeout
	}

	if c.Config.Redis.PoolSize != 0 {
		o.PoolSize = c.Config.Redis.PoolSize
	}

	if c.Config.Redis.MinIdleConns != 0 {
		o.MinIdleConns = c.Config.Redis.MinIdleConns
	}

	if c.Config.Redis.ConnMaxLifetime != 0 {
		o.ConnMaxLifetime = c.Config.Redis.ConnMaxLifetime
	}

	if c.Config.Redis.PoolTimeout != 0 {
		o.PoolTimeout = c.Config.Redis.PoolTimeout
	}

	if c.Config.Redis.ConnMaxIdleTime != 0 {
		o.ConnMaxIdleTime = c.Config.Redis.ConnMaxIdleTime
	}

	return o, nil
}
