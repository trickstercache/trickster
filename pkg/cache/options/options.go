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
	out.Redis.ConnMaxIdleTime = c.Redis.ConnMaxIdleTime
	out.Redis.ConnMaxLifetime = c.Redis.ConnMaxLifetime
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
	if c.Index.MaxSizeBytes > 0 && c.Index.MaxSizeBackoffBytes > c.Index.MaxSizeBytes {
		return errMaxSizeBackoffBytesTooBig
	}
	if c.Index.MaxSizeObjects > 0 && c.Index.MaxSizeBackoffObjects > c.Index.MaxSizeObjects {
		return errMaxSizeBackoffObjectsTooBig
	}

	return nil
}

var errMaxSizeBackoffBytesTooBig = errors.New("MaxSizeBackoffBytes can't be larger than MaxSizeBytes")
var errMaxSizeBackoffObjectsTooBig = errors.New("MaxSizeBackoffObjects can't be larger than MaxSizeObjects")

// Initialize sets up the cache Options with default values and overlays
// any values that were set during YAML unmarshaling
func (c *Options) Initialize(name string) error {
	c.Name = name

	if c.Provider != "" {
		c.Provider = strings.ToLower(c.Provider)
		if n, ok := providers.Names[c.Provider]; ok {
			c.ProviderID = n
		}
	}

	if c.Index == nil {
		c.Index = index.New()
	}
	if c.Redis == nil {
		c.Redis = redis.New()
	}
	if c.Filesystem == nil {
		c.Filesystem = filesystem.New()
	}
	if c.BBolt == nil {
		c.BBolt = bbolt.New()
	}
	if c.Badger == nil {
		c.Badger = badger.New()
	}

	if !c.UseCacheChunking && c.TimeseriesChunkFactor == 0 {
		c.UseCacheChunking = defaults.DefaultUseCacheChunking
	}
	if c.TimeseriesChunkFactor == 0 {
		c.TimeseriesChunkFactor = defaults.DefaultTimeseriesChunkFactor
	}
	if c.ByterangeChunkSize == 0 {
		c.ByterangeChunkSize = defaults.DefaultByterangeChunkSize
	}

	return nil
}

// Initialize initializes all cache options in the lookup with default values
// and overlays any values that were set during YAML unmarshaling
func (l Lookup) Initialize(activeCaches sets.Set[string]) ([]string, error) {
	var warnings []string

	for k := range l {
		if _, ok := activeCaches[k]; !ok {
			delete(l, k)
		}
	}

	for k, v := range l {
		if err := v.Initialize(k); err != nil {
			return nil, err
		}

		if v.ProviderID == providers.Redis {
			var hasEndpoint, hasEndpoints bool

			if v.Redis.Endpoint != "" {
				hasEndpoint = true
			}
			if len(v.Redis.Endpoints) > 0 {
				hasEndpoints = true
			}

			if v.Redis.ClientType == "standard" {
				if hasEndpoints && !hasEndpoint {
					warnings = append(warnings,
						"'standard' redis type configured, but 'endpoints' value is provided instead of 'endpoint'")
				}
			} else {
				if hasEndpoint && !hasEndpoints {
					warnings = append(warnings, fmt.Sprintf(
						"'%s' redis type configured, but 'endpoint' value is provided instead of 'endpoints'",
						v.Redis.ClientType))
				}
			}
		}
	}
	return warnings, nil
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
