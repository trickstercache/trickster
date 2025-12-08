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

func (o *Options) UnmarshalYAML(unmarshal func(any) error) error {
	type loadOptions Options
	lo := loadOptions(*(New()))
	if err := unmarshal(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}

// Equal returns true if all values in the Options are identical
func (o *Options) Equal(o2 *Options) bool {
	if o2 == nil {
		return o == nil
	}
	if o == nil {
		return false
	}
	return o.ClientType == o2.ClientType &&
		o.Protocol == o2.Protocol &&
		o.Endpoint == o2.Endpoint &&
		equalStringSlices(o.Endpoints, o2.Endpoints) &&
		o.Username == o2.Username &&
		o.Password == o2.Password &&
		o.SentinelMaster == o2.SentinelMaster &&
		o.DB == o2.DB &&
		o.MaxRetries == o2.MaxRetries &&
		o.MinRetryBackoff == o2.MinRetryBackoff &&
		o.MaxRetryBackoff == o2.MaxRetryBackoff &&
		o.DialTimeout == o2.DialTimeout &&
		o.ReadTimeout == o2.ReadTimeout &&
		o.WriteTimeout == o2.WriteTimeout &&
		o.PoolSize == o2.PoolSize &&
		o.MinIdleConns == o2.MinIdleConns &&
		o.ConnMaxLifetime == o2.ConnMaxLifetime &&
		o.PoolTimeout == o2.PoolTimeout &&
		o.ConnMaxIdleTime == o2.ConnMaxIdleTime &&
		o.UseTLS == o2.UseTLS
}

// equalStringSlices compares two string slices for equality
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
