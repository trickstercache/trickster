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

package options

import (
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config/types"
)

// Options is a collection of Configurations for Connecting to Redis
type Options struct {
	// ClientType defines the type of Redis Client ("standard", "cluster", "sentinel")
	ClientType string `yaml:"client_type,omitempty"`
	// Protocol represents the connection method (e.g., "tcp", "unix", etc.)
	Protocol string `yaml:"protocol,omitempty"`
	// Endpoint represents FQDN:port or IP:Port of the Redis Endpoint
	Endpoint string `yaml:"endpoint,omitempty"`
	// Endpoints represents FQDN:port or IP:Port collection of a Redis Cluster or Sentinel Nodes
	Endpoints []string `yaml:"endpoints,omitempty"`
	// Username can be set when using a password protected redis instance.
	Username string `yaml:"username,omitempty"`
	// Password can be set when using a password protected redis instance.
	Password types.EnvString `yaml:"password,omitempty"`
	// SentinelMaster should be set when using Redis Sentinel to indicate the Master Node
	SentinelMaster string `yaml:"sentinel_master,omitempty"`
	// DB is the Database to be selected after connecting to the server.
	DB int `yaml:"db,omitempty"`
	// MaxRetries is the maximum number of retries before giving up on the command
	MaxRetries int `yaml:"max_retries,omitempty"`
	// MinRetryBackoff is the minimum backoff between each retry.
	MinRetryBackoff time.Duration `yaml:"min_retry_backoff,omitempty"`
	// MaxRetryBackoff is the Maximum backoff between each retry.
	MaxRetryBackoff time.Duration `yaml:"max_retry_backoff,omitempty"`
	// DialTimeout is the timeout for establishing new connections.
	DialTimeout time.Duration `yaml:"dial_timeout,omitempty"`
	// ReadTimeout is the timeout for socket reads.
	// If reached, commands will fail with a timeout instead of blocking.
	ReadTimeout time.Duration `yaml:"read_timeout,omitempty"`
	// WriteTimeout is the timeout for socket writes.
	// If reached, commands will fail with a timeout instead of blocking.
	WriteTimeout time.Duration `yaml:"write_timeout,omitempty"`
	// PoolSize is the maximum number of socket connections.
	PoolSize int `yaml:"pool_size,omitempty"`
	// MinIdleConns is the minimum number of idle connections
	// which is useful when establishing new connection is slow.
	MinIdleConns int `yaml:"min_idle_conns,omitempty"`
	// ConnMaxLifetime is the connection age at which client retires (closes) the connection.
	ConnMaxLifetime time.Duration `yaml:"max_conn_age,omitempty"`
	// PoolTimeout is the amount of time client waits for connection if all
	// connections are busy before returning an error.
	PoolTimeout time.Duration `yaml:"pool_timeout,omitempty"`
	// ConnMaxIdleTime is the amount of time after which client closes idle connections.
	ConnMaxIdleTime time.Duration `yaml:"idle_timeout,omitempty"`
	// UseTLS indicates whether the server connection is TLS
	UseTLS bool `yaml:"use_tls,omitempty"`
}

// New returns a new Redis Options Reference with default values set
func New() *Options {
	return &Options{
		ClientType: DefaultRedisClientType,
		Protocol:   DefaultRedisProtocol,
		Endpoint:   DefaultRedisEndpoint,
		Endpoints:  []string{DefaultRedisEndpoint},
	}
}

type loaderOptions struct {
	ClientType      *string          `yaml:"client_type,omitempty"`
	Protocol        *string          `yaml:"protocol,omitempty"`
	Endpoint        *string          `yaml:"endpoint,omitempty"`
	Endpoints       []string         `yaml:"endpoints,omitempty"`
	Username        *string          `yaml:"username,omitempty"`
	Password        *types.EnvString `yaml:"password,omitempty"`
	SentinelMaster  *string          `yaml:"sentinel_master,omitempty"`
	DB              *int             `yaml:"db,omitempty"`
	MaxRetries      *int             `yaml:"max_retries,omitempty"`
	MinRetryBackoff *time.Duration   `yaml:"min_retry_backoff,omitempty"`
	MaxRetryBackoff *time.Duration   `yaml:"max_retry_backoff,omitempty"`
	DialTimeout     *time.Duration   `yaml:"dial_timeout,omitempty"`
	ReadTimeout     *time.Duration   `yaml:"read_timeout,omitempty"`
	WriteTimeout    *time.Duration   `yaml:"write_timeout,omitempty"`
	PoolSize        *int             `yaml:"pool_size,omitempty"`
	MinIdleConns    *int             `yaml:"min_idle_conns,omitempty"`
	ConnMaxLifetime *time.Duration   `yaml:"max_conn_age,omitempty"`
	PoolTimeout     *time.Duration   `yaml:"pool_timeout,omitempty"`
	ConnMaxIdleTime *time.Duration   `yaml:"idle_timeout,omitempty"`
	UseTLS          *bool            `yaml:"use_tls,omitempty"`
}

func (o *Options) UnmarshalYAML(unmarshal func(interface{}) error) error {
	*o = *(New())

	var load loaderOptions
	if err := unmarshal(&load); err != nil {
		return err
	}

	if load.ClientType != nil {
		o.ClientType = *load.ClientType
	}
	if load.Protocol != nil {
		o.Protocol = *load.Protocol
	}
	if load.Endpoint != nil {
		o.Endpoint = *load.Endpoint
	}
	if load.Endpoints != nil {
		o.Endpoints = load.Endpoints
	}
	if load.Username != nil {
		o.Username = *load.Username
	}
	if load.Password != nil {
		o.Password = *load.Password
	}
	if load.SentinelMaster != nil {
		o.SentinelMaster = *load.SentinelMaster
	}
	if load.DB != nil {
		o.DB = *load.DB
	}
	if load.MaxRetries != nil {
		o.MaxRetries = *load.MaxRetries
	}
	if load.MinRetryBackoff != nil {
		o.MinRetryBackoff = *load.MinRetryBackoff
	}
	if load.MaxRetryBackoff != nil {
		o.MaxRetryBackoff = *load.MaxRetryBackoff
	}
	if load.DialTimeout != nil {
		o.DialTimeout = *load.DialTimeout
	}
	if load.ReadTimeout != nil {
		o.ReadTimeout = *load.ReadTimeout
	}
	if load.WriteTimeout != nil {
		o.WriteTimeout = *load.WriteTimeout
	}
	if load.PoolSize != nil {
		o.PoolSize = *load.PoolSize
	}
	if load.MinIdleConns != nil {
		o.MinIdleConns = *load.MinIdleConns
	}
	if load.ConnMaxLifetime != nil {
		o.ConnMaxLifetime = *load.ConnMaxLifetime
	}
	if load.PoolTimeout != nil {
		o.PoolTimeout = *load.PoolTimeout
	}
	if load.ConnMaxIdleTime != nil {
		o.ConnMaxIdleTime = *load.ConnMaxIdleTime
	}
	if load.UseTLS != nil {
		o.UseTLS = *load.UseTLS
	}

	return nil
}
