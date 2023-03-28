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
	strutil "github.com/trickstercache/trickster/v2/pkg/util/strings"
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
func (cc *Options) Clone() *Options {

	c := New()
	c.Name = cc.Name
	c.Provider = cc.Provider
	c.ProviderID = cc.ProviderID

	c.Index.FlushInterval = cc.Index.FlushInterval
	c.Index.FlushIntervalMS = cc.Index.FlushIntervalMS
	c.Index.MaxSizeBackoffBytes = cc.Index.MaxSizeBackoffBytes
	c.Index.MaxSizeBackoffObjects = cc.Index.MaxSizeBackoffObjects
	c.Index.MaxSizeBytes = cc.Index.MaxSizeBytes
	c.Index.MaxSizeObjects = cc.Index.MaxSizeObjects
	c.Index.ReapInterval = cc.Index.ReapInterval
	c.Index.ReapIntervalMS = cc.Index.ReapIntervalMS

	c.Badger.Directory = cc.Badger.Directory
	c.Badger.ValueDirectory = cc.Badger.ValueDirectory

	c.Filesystem.CachePath = cc.Filesystem.CachePath

	c.BBolt.Bucket = cc.BBolt.Bucket
	c.BBolt.Filename = cc.BBolt.Filename

	c.Redis.ClientType = cc.Redis.ClientType
	c.Redis.DB = cc.Redis.DB
	c.Redis.DialTimeoutMS = cc.Redis.DialTimeoutMS
	c.Redis.Endpoint = cc.Redis.Endpoint
	c.Redis.Endpoints = cc.Redis.Endpoints
	c.Redis.IdleCheckFrequencyMS = cc.Redis.IdleCheckFrequencyMS
	c.Redis.IdleTimeoutMS = cc.Redis.IdleTimeoutMS
	c.Redis.MaxConnAgeMS = cc.Redis.MaxConnAgeMS
	c.Redis.MaxRetries = cc.Redis.MaxRetries
	c.Redis.MaxRetryBackoffMS = cc.Redis.MaxRetryBackoffMS
	c.Redis.MinIdleConns = cc.Redis.MinIdleConns
	c.Redis.MinRetryBackoffMS = cc.Redis.MinRetryBackoffMS
	c.Redis.Password = cc.Redis.Password
	c.Redis.PoolSize = cc.Redis.PoolSize
	c.Redis.PoolTimeoutMS = cc.Redis.PoolTimeoutMS
	c.Redis.Protocol = cc.Redis.Protocol
	c.Redis.ReadTimeoutMS = cc.Redis.ReadTimeoutMS
	c.Redis.SentinelMaster = cc.Redis.SentinelMaster
	c.Redis.WriteTimeoutMS = cc.Redis.WriteTimeoutMS

	c.UseCacheChunking = cc.UseCacheChunking
	c.TimeseriesChunkFactor = cc.TimeseriesChunkFactor
	c.ByterangeChunkSize = cc.ByterangeChunkSize

	return c

}

// Equal returns true if all values in the Options references and their
// their child Option references are completely identical
func (cc *Options) Equal(cc2 *Options) bool {

	if cc2 == nil {
		return false
	}

	return cc.Name == cc2.Name &&
		cc.Provider == cc2.Provider &&
		cc.ProviderID == cc2.ProviderID

}

var errMaxSizeBackoffBytesTooBig = errors.New("MaxSizeBackoffBytes can't be larger than MaxSizeBytes")
var errMaxSizeBackoffObjectsTooBig = errors.New("MaxSizeBackoffObjects can't be larger than MaxSizeObjects")

// SetDefaults iterates the provided Options, and overlays user-set values onto the default Options
func (l Lookup) SetDefaults(metadata yamlx.KeyLookup, activeCaches strutil.Lookup) ([]string, error) {

	// setCachingDefaults assumes that processBackendOptionss was just ran

	lw := make([]string, 0)

	for k, v := range l {

		if _, ok := activeCaches[k]; !ok {
			// a configured cache was not used by any backend. don't even instantiate it
			delete(l, k)
			continue
		}

		cc := New()
		cc.Name = k

		if metadata.IsDefined("caches", k, "provider") {
			cc.Provider = strings.ToLower(v.Provider)
			if n, ok := providers.Names[cc.Provider]; ok {
				cc.ProviderID = n
			}
		}

		if metadata.IsDefined("caches", k, "index", "reap_interval_ms") {
			cc.Index.ReapIntervalMS = v.Index.ReapIntervalMS
		}

		if metadata.IsDefined("caches", k, "index", "flush_interval_ms") {
			cc.Index.FlushIntervalMS = v.Index.FlushIntervalMS
		}

		if metadata.IsDefined("caches", k, "index", "max_size_bytes") {
			cc.Index.MaxSizeBytes = v.Index.MaxSizeBytes
		}

		if metadata.IsDefined("caches", k, "index", "max_size_backoff_bytes") {
			cc.Index.MaxSizeBackoffBytes = v.Index.MaxSizeBackoffBytes
		}

		if cc.Index.MaxSizeBytes > 0 && cc.Index.MaxSizeBackoffBytes > cc.Index.MaxSizeBytes {
			return nil, errMaxSizeBackoffBytesTooBig
		}

		if metadata.IsDefined("caches", k, "index", "max_size_objects") {
			cc.Index.MaxSizeObjects = v.Index.MaxSizeObjects
		}

		if metadata.IsDefined("caches", k, "index", "max_size_backoff_objects") {
			cc.Index.MaxSizeBackoffObjects = v.Index.MaxSizeBackoffObjects
		}

		if cc.Index.MaxSizeObjects > 0 && cc.Index.MaxSizeBackoffObjects > cc.Index.MaxSizeObjects {
			return nil, errMaxSizeBackoffObjectsTooBig
		}

		if cc.ProviderID == providers.Redis {

			var hasEndpoint, hasEndpoints bool

			ct := strings.ToLower(v.Redis.ClientType)
			if metadata.IsDefined("caches", k, "redis", "client_type") {
				cc.Redis.ClientType = ct
			}

			if metadata.IsDefined("caches", k, "redis", "protocol") {
				cc.Redis.Protocol = v.Redis.Protocol
			}

			if metadata.IsDefined("caches", k, "redis", "endpoint") {
				cc.Redis.Endpoint = v.Redis.Endpoint
				hasEndpoint = true
			}

			if metadata.IsDefined("caches", k, "redis", "endpoints") {
				cc.Redis.Endpoints = v.Redis.Endpoints
				hasEndpoints = true
			}

			if cc.Redis.ClientType == "standard" {
				if hasEndpoints && !hasEndpoint {
					lw = append(lw,
						"'standard' redis type configured, but 'endpoints' value is provided instead of 'endpoint'")
				}
			} else {
				if hasEndpoint && !hasEndpoints {
					lw = append(lw, fmt.Sprintf(
						"'%s' redis type configured, but 'endpoint' value is provided instead of 'endpoints'",
						cc.Redis.ClientType))
				}
			}

			if metadata.IsDefined("caches", k, "redis", "sentinel_master") {
				cc.Redis.SentinelMaster = v.Redis.SentinelMaster
			}

			if metadata.IsDefined("caches", k, "redis", "password") {
				cc.Redis.Password = v.Redis.Password
			}

			if metadata.IsDefined("caches", k, "redis", "db") {
				cc.Redis.DB = v.Redis.DB
			}

			if metadata.IsDefined("caches", k, "redis", "max_retries") {
				cc.Redis.MaxRetries = v.Redis.MaxRetries
			}

			if metadata.IsDefined("caches", k, "redis", "min_retry_backoff_ms") {
				cc.Redis.MinRetryBackoffMS = v.Redis.MinRetryBackoffMS
			}

			if metadata.IsDefined("caches", k, "redis", "max_retry_backoff_ms") {
				cc.Redis.MaxRetryBackoffMS = v.Redis.MaxRetryBackoffMS
			}

			if metadata.IsDefined("caches", k, "redis", "dial_timeout_ms") {
				cc.Redis.DialTimeoutMS = v.Redis.DialTimeoutMS
			}

			if metadata.IsDefined("caches", k, "redis", "read_timeout_ms") {
				cc.Redis.ReadTimeoutMS = v.Redis.ReadTimeoutMS
			}

			if metadata.IsDefined("caches", k, "redis", "write_timeout_ms") {
				cc.Redis.WriteTimeoutMS = v.Redis.WriteTimeoutMS
			}

			if metadata.IsDefined("caches", k, "redis", "pool_size") {
				cc.Redis.PoolSize = v.Redis.PoolSize
			}

			if metadata.IsDefined("caches", k, "redis", "min_idle_conns") {
				cc.Redis.MinIdleConns = v.Redis.MinIdleConns
			}

			if metadata.IsDefined("caches", k, "redis", "max_conn_age_ms") {
				cc.Redis.MaxConnAgeMS = v.Redis.MaxConnAgeMS
			}

			if metadata.IsDefined("caches", k, "redis", "pool_timeout_ms") {
				cc.Redis.PoolTimeoutMS = v.Redis.PoolTimeoutMS
			}

			if metadata.IsDefined("caches", k, "redis", "idle_timeout_ms") {
				cc.Redis.IdleTimeoutMS = v.Redis.IdleTimeoutMS
			}

			if metadata.IsDefined("caches", k, "redis", "idle_check_frequency_ms") {
				cc.Redis.IdleCheckFrequencyMS = v.Redis.IdleCheckFrequencyMS
			}
		}

		if metadata.IsDefined("caches", k, "filesystem", "cache_path") {
			cc.Filesystem.CachePath = v.Filesystem.CachePath
		}

		if metadata.IsDefined("caches", k, "bbolt", "filename") {
			cc.BBolt.Filename = v.BBolt.Filename
		}

		if metadata.IsDefined("caches", k, "bbolt", "bucket") {
			cc.BBolt.Bucket = v.BBolt.Bucket
		}

		if metadata.IsDefined("caches", k, "badger", "directory") {
			cc.Badger.Directory = v.Badger.Directory
		}

		if metadata.IsDefined("caches", k, "badger", "value_directory") {
			cc.Badger.ValueDirectory = v.Badger.ValueDirectory
		}

		l[k] = cc
	}
	return lw, nil
}
