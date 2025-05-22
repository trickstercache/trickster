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
	"errors"
	"fmt"
	"strings"

	badger "github.com/trickstercache/trickster/v2/pkg/cache/badger/options"
	bbolt "github.com/trickstercache/trickster/v2/pkg/cache/bbolt/options"
	filesystem "github.com/trickstercache/trickster/v2/pkg/cache/filesystem/options"
	index "github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/options/defaults"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	redis "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"
)

// Lookup is a map of Options
type Lookup map[string]*Options

// Options is a collection of defining the Trickster Caching Behavior
type Options struct {
	// Name is the Name of the cache, taken from the Key in the Caches map[string]*CacheConfig
	Name string `yaml:"-"`
	// Provider represents the type of cache that we wish to use: "boltdb", "memory", "filesystem", or "redis"
	Provider string `yaml:"provider,omitempty"`
	// Index provides options for the Cache Index
	Index *index.Options `yaml:"index,omitempty"`
	// Redis provides options for Redis caching
	Redis *redis.Options `yaml:"redis,omitempty"`
	// Filesystem provides options for Filesystem caching
	Filesystem *filesystem.Options `yaml:"filesystem,omitempty"`
	// BBolt provides options for BBolt caching
	BBolt *bbolt.Options `yaml:"bbolt,omitempty"`
	// Badger provides options for BadgerDB caching
	Badger *badger.Options `yaml:"badger,omitempty"`

	// Defines if the cache should use cache chunking. Splits cache objects into smaller, reliably-sized parts.
	UseCacheChunking bool `yaml:"use_cache_chunking,omitempty"`
	// Determines chunk size (duration) for timeseries objects, query step * chunk factor
	TimeseriesChunkFactor int64 `yaml:"timeseries_chunk_factor"`
	// Determines chunk size (bytes) for byterange objects
	ByterangeChunkSize int64 `yaml:"byterange_chunk_size"`

	//  Synthetic Values

	// ProviderID represents the internal constant for the provided Provider string
	// and is automatically populated at startup
	ProviderID providers.Provider `yaml:"-"`
}

var restrictedNames = sets.New([]string{"", "none"})
var ErrInvalidName = errors.New("invalid cache name")

// New will return a pointer to a CacheOptions with the default configuration settings
func New() *Options {
	return &Options{
		Provider:              defaults.DefaultCacheProvider,
		ProviderID:            defaults.DefaultCacheProviderID,
		Redis:                 redis.New(),
		Filesystem:            filesystem.New(),
		BBolt:                 bbolt.New(),
		Badger:                badger.New(),
		Index:                 index.New(),
		UseCacheChunking:      defaults.DefaultUseCacheChunking,
		TimeseriesChunkFactor: defaults.DefaultTimeseriesChunkFactor,
		ByterangeChunkSize:    defaults.DefaultByterangeChunkSize,
	}
}

// Clone returns an exact copy of a *CachingConfig
func (c *Options) Clone() *Options {

	out := New()
	out.Name = c.Name
	out.Provider = c.Provider
	out.ProviderID = c.ProviderID

	out.Index.FlushInterval = c.Index.FlushInterval
	out.Index.FlushInterval = c.Index.FlushInterval
	out.Index.MaxSizeBackoffBytes = c.Index.MaxSizeBackoffBytes
	out.Index.MaxSizeBackoffObjects = c.Index.MaxSizeBackoffObjects
	out.Index.MaxSizeBytes = c.Index.MaxSizeBytes
	out.Index.MaxSizeObjects = c.Index.MaxSizeObjects
	out.Index.ReapInterval = c.Index.ReapInterval
	out.Index.ReapInterval = c.Index.ReapInterval

	out.Badger.Directory = c.Badger.Directory
	out.Badger.ValueDirectory = c.Badger.ValueDirectory

	out.Filesystem.CachePath = c.Filesystem.CachePath

	out.BBolt.Bucket = c.BBolt.Bucket
	out.BBolt.Filename = c.BBolt.Filename

	out.Redis.ClientType = c.Redis.ClientType
	out.Redis.DB = c.Redis.DB
	out.Redis.DialTimeout = c.Redis.DialTimeout
	out.Redis.Endpoint = c.Redis.Endpoint
	out.Redis.Endpoints = c.Redis.Endpoints
	out.Redis.IdleCheckFrequency = c.Redis.IdleCheckFrequency
	out.Redis.IdleTimeout = c.Redis.IdleTimeout
	out.Redis.MaxConnAge = c.Redis.MaxConnAge
	out.Redis.MaxRetries = c.Redis.MaxRetries
	out.Redis.MaxRetryBackoff = c.Redis.MaxRetryBackoff
	out.Redis.MinIdleConns = c.Redis.MinIdleConns
	out.Redis.MinRetryBackoff = c.Redis.MinRetryBackoff
	out.Redis.Password = c.Redis.Password
	out.Redis.PoolSize = c.Redis.PoolSize
	out.Redis.PoolTimeout = c.Redis.PoolTimeout
	out.Redis.Protocol = c.Redis.Protocol
	out.Redis.ReadTimeout = c.Redis.ReadTimeout
	out.Redis.SentinelMaster = c.Redis.SentinelMaster
	out.Redis.WriteTimeout = c.Redis.WriteTimeout

	out.UseCacheChunking = c.UseCacheChunking
	out.TimeseriesChunkFactor = c.TimeseriesChunkFactor
	out.ByterangeChunkSize = c.ByterangeChunkSize

	return c
}

// Equal returns true if all values in the Options references and their
// their child Option references are completely identical
func (c *Options) Equal(c2 *Options) bool {
	if c2 == nil {
		return false
	}
	return c.Name == c2.Name &&
		c.Provider == c2.Provider &&
		c.ProviderID == c2.ProviderID
}

func (c *Options) Validate() error {
	if restrictedNames.Contains(c.Name) {
		return ErrInvalidName
	}
	return nil
}

var errMaxSizeBackoffBytesTooBig = errors.New("MaxSizeBackoffBytes can't be larger than MaxSizeBytes")
var errMaxSizeBackoffObjectsTooBig = errors.New("MaxSizeBackoffObjects can't be larger than MaxSizeObjects")

// OverlayYAMLData extracts supported cache Options values from the yaml map,
// and overlays the extracted values into activeCaches
func (l Lookup) OverlayYAMLData(y yamlx.KeyLookup,
	activeCaches sets.Set[string]) ([]string, error) {

	// setCachingDefaults assumes that processBackendOptions was just ran

	lw := make([]string, 0)

	for k, v := range l {

		if _, ok := activeCaches[k]; !ok {
			// a configured cache was not used by any backend. don't even instantiate it
			delete(l, k)
			continue
		}

		c := New()
		c.Name = k

		if y.IsDefined("caches", k, "provider") {
			c.Provider = strings.ToLower(v.Provider)
			if n, ok := providers.Names[c.Provider]; ok {
				c.ProviderID = n
			}
		}

		if y.IsDefined("caches", k, "index", "reap_interval") {
			c.Index.ReapInterval = v.Index.ReapInterval
		}

		if y.IsDefined("caches", k, "index", "flush_interval") {
			c.Index.FlushInterval = v.Index.FlushInterval
		}

		if y.IsDefined("caches", k, "index", "max_size_bytes") {
			c.Index.MaxSizeBytes = v.Index.MaxSizeBytes
		}

		if y.IsDefined("caches", k, "index", "max_size_backoff_bytes") {
			c.Index.MaxSizeBackoffBytes = v.Index.MaxSizeBackoffBytes
		}

		if c.Index.MaxSizeBytes > 0 && c.Index.MaxSizeBackoffBytes > c.Index.MaxSizeBytes {
			return nil, errMaxSizeBackoffBytesTooBig
		}

		if y.IsDefined("caches", k, "index", "max_size_objects") {
			c.Index.MaxSizeObjects = v.Index.MaxSizeObjects
		}

		if y.IsDefined("caches", k, "index", "max_size_backoff_objects") {
			c.Index.MaxSizeBackoffObjects = v.Index.MaxSizeBackoffObjects
		}

		if c.Index.MaxSizeObjects > 0 && c.Index.MaxSizeBackoffObjects > c.Index.MaxSizeObjects {
			return nil, errMaxSizeBackoffObjectsTooBig
		}

		if c.ProviderID == providers.Redis {

			var hasEndpoint, hasEndpoints bool

			ct := strings.ToLower(v.Redis.ClientType)
			if y.IsDefined("caches", k, "redis", "client_type") {
				c.Redis.ClientType = ct
			}

			if y.IsDefined("caches", k, "redis", "protocol") {
				c.Redis.Protocol = v.Redis.Protocol
			}

			if y.IsDefined("caches", k, "redis", "endpoint") {
				c.Redis.Endpoint = v.Redis.Endpoint
				hasEndpoint = true
			}

			if y.IsDefined("caches", k, "redis", "endpoints") {
				c.Redis.Endpoints = v.Redis.Endpoints
				hasEndpoints = true
			}

			if c.Redis.ClientType == "standard" {
				if hasEndpoints && !hasEndpoint {
					lw = append(lw,
						"'standard' redis type configured, but 'endpoints' value is provided instead of 'endpoint'")
				}
			} else {
				if hasEndpoint && !hasEndpoints {
					lw = append(lw, fmt.Sprintf(
						"'%s' redis type configured, but 'endpoint' value is provided instead of 'endpoints'",
						c.Redis.ClientType))
				}
			}

			if y.IsDefined("caches", k, "redis", "sentinel_master") {
				c.Redis.SentinelMaster = v.Redis.SentinelMaster
			}

			if y.IsDefined("caches", k, "redis", "password") {
				c.Redis.Password = v.Redis.Password
			}

			if y.IsDefined("caches", k, "redis", "db") {
				c.Redis.DB = v.Redis.DB
			}

			if y.IsDefined("caches", k, "redis", "max_retries") {
				c.Redis.MaxRetries = v.Redis.MaxRetries
			}

			if y.IsDefined("caches", k, "redis", "min_retry_backoff") {
				c.Redis.MinRetryBackoff = v.Redis.MinRetryBackoff
			}

			if y.IsDefined("caches", k, "redis", "max_retry_backoff") {
				c.Redis.MaxRetryBackoff = v.Redis.MaxRetryBackoff
			}

			if y.IsDefined("caches", k, "redis", "dial_timeout") {
				c.Redis.DialTimeout = v.Redis.DialTimeout
			}

			if y.IsDefined("caches", k, "redis", "read_timeout") {
				c.Redis.ReadTimeout = v.Redis.ReadTimeout
			}

			if y.IsDefined("caches", k, "redis", "write_timeout") {
				c.Redis.WriteTimeout = v.Redis.WriteTimeout
			}

			if y.IsDefined("caches", k, "redis", "pool_size") {
				c.Redis.PoolSize = v.Redis.PoolSize
			}

			if y.IsDefined("caches", k, "redis", "min_idle_conns") {
				c.Redis.MinIdleConns = v.Redis.MinIdleConns
			}

			if y.IsDefined("caches", k, "redis", "max_conn_age") {
				c.Redis.MaxConnAge = v.Redis.MaxConnAge
			}

			if y.IsDefined("caches", k, "redis", "pool_timeout") {
				c.Redis.PoolTimeout = v.Redis.PoolTimeout
			}

			if y.IsDefined("caches", k, "redis", "idle_timeout") {
				c.Redis.IdleTimeout = v.Redis.IdleTimeout
			}

			if y.IsDefined("caches", k, "redis", "idle_check_frequency") {
				c.Redis.IdleCheckFrequency = v.Redis.IdleCheckFrequency
			}
		}

		if y.IsDefined("caches", k, "filesystem", "cache_path") {
			c.Filesystem.CachePath = v.Filesystem.CachePath
		}

		if y.IsDefined("caches", k, "bbolt", "filename") {
			c.BBolt.Filename = v.BBolt.Filename
		}

		if y.IsDefined("caches", k, "bbolt", "bucket") {
			c.BBolt.Bucket = v.BBolt.Bucket
		}

		if y.IsDefined("caches", k, "badger", "directory") {
			c.Badger.Directory = v.Badger.Directory
		}

		if y.IsDefined("caches", k, "badger", "value_directory") {
			c.Badger.ValueDirectory = v.Badger.ValueDirectory
		}

		l[k] = c
	}
	return lw, nil
}

func (l Lookup) Validate() error {
	for k, c := range l {
		c.Name = k
		if err := c.Validate(); err != nil {
			return err
		}
	}
	return nil
}
