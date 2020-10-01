/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package options

import (
	d "github.com/tricksterproxy/trickster/pkg/config/defaults"
)

// Options is a collection of Configurations for Connecting to Redis
type Options struct {
	// ClientType defines the type of Redis Client ("standard", "cluster", "sentinel")
	ClientType string `toml:"client_type"`
	// Protocol represents the connection method (e.g., "tcp", "unix", etc.)
	Protocol string `toml:"protocol"`
	// Endpoint represents FQDN:port or IP:Port of the Redis Endpoint
	Endpoint string `toml:"endpoint"`
	// Endpoints represents FQDN:port or IP:Port collection of a Redis Cluster or Sentinel Nodes
	Endpoints []string `toml:"endpoints"`
	// Password can be set when using password protected redis instance.
	Password string `toml:"password"`
	// SentinelMaster should be set when using Redis Sentinel to indicate the Master Node
	SentinelMaster string `toml:"sentinel_master"`
	// DB is the Database to be selected after connecting to the server.
	DB int `toml:"db"`
	// MaxRetries is the maximum number of retries before giving up on the command
	MaxRetries int `toml:"max_retries"`
	// MinRetryBackoffMS is the minimum backoff between each retry.
	MinRetryBackoffMS int `toml:"min_retry_backoff_ms"`
	// MaxRetryBackoffMS is the Maximum backoff between each retry.
	MaxRetryBackoffMS int `toml:"max_retry_backoff_ms"`
	// DialTimeoutMS is the timeout for establishing new connections.
	DialTimeoutMS int `toml:"dial_timeout_ms"`
	// ReadTimeoutMS is the timeout for socket reads.
	// If reached, commands will fail with a timeout instead of blocking.
	ReadTimeoutMS int `toml:"read_timeout_ms"`
	// WriteTimeoutMS is the timeout for socket writes.
	// If reached, commands will fail with a timeout instead of blocking.
	WriteTimeoutMS int `toml:"write_timeout_ms"`
	// PoolSize is the maximum number of socket connections.
	PoolSize int `toml:"pool_size"`
	// MinIdleConns is the minimum number of idle connections
	// which is useful when establishing new connection is slow.
	MinIdleConns int `toml:"min_idle_conns"`
	// MaxConnAgeMS is the connection age at which client retires (closes) the connection.
	MaxConnAgeMS int `toml:"max_conn_age_ms"`
	// PoolTimeoutMS is the amount of time client waits for connection if all
	// connections are busy before returning an error.
	PoolTimeoutMS int `toml:"pool_timeout_ms"`
	// IdleTimeoutMS is the amount of time after which client closes idle connections.
	IdleTimeoutMS int `toml:"idle_timeout_ms"`
	// IdleCheckFrequencyMS is the frequency of idle checks made by idle connections reaper.
	IdleCheckFrequencyMS int `toml:"idle_check_frequency_ms"`
}

// New returns a new Redis Options Reference with default values set
func New() *Options {
	return &Options{
		ClientType: d.DefaultRedisClientType,
		Protocol:   d.DefaultRedisProtocol,
		Endpoint:   d.DefaultRedisEndpoint,
		Endpoints:  []string{d.DefaultRedisEndpoint},
	}
}
