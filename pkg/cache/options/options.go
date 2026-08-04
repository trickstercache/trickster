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
	memory "github.com/trickstercache/trickster/v2/pkg/cache/memory/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/options/defaults"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	redis "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
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
	// Memory provides options for Memory caching
	Memory *memory.Options `yaml:"memory,omitempty"`

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

var _ types.ConfigOptions[Options] = &Options{}

var (
	restrictedNames = sets.New([]string{"", "none"})
	ErrInvalidName  = errors.New("invalid cache name")
)

// New will return a pointer to a CacheOptions with the default configuration settings
func New() *Options {
	return &Options{
		Provider:              defaults.DefaultCacheProvider,
		ProviderID:            defaults.DefaultCacheProviderID,
		Redis:                 redis.New(),
		Filesystem:            filesystem.New(),
		BBolt:                 bbolt.New(),
		Badger:                badger.New(),
		Memory:                memory.New(),
		Index:                 index.New(),
		UseCacheChunking:      defaults.DefaultUseCacheChunking,
		TimeseriesChunkFactor: defaults.DefaultTimeseriesChunkFactor,
		ByterangeChunkSize:    defaults.DefaultByterangeChunkSize,
	}
}

// Clone returns an exact copy of a *CachingConfig
func (o *Options) Clone() *Options {
	out := pointers.Clone(o)
	out.Redis = pointers.Clone(o.Redis)
	out.Filesystem = pointers.Clone(o.Filesystem)
	out.BBolt = pointers.Clone(o.BBolt)
	out.Badger = pointers.Clone(o.Badger)
	out.Memory = pointers.Clone(o.Memory)
	out.Index = pointers.Clone(o.Index)
	return out
}

// Equal returns true if all values in the Options references and their
// their child Option references are completely identical
func (o *Options) Equal(o2 *Options) bool {
	if o2 == nil {
		return false
	}
	if o.Name != o2.Name ||
		o.Provider != o2.Provider ||
		o.ProviderID != o2.ProviderID ||
		o.UseCacheChunking != o2.UseCacheChunking ||
		o.TimeseriesChunkFactor != o2.TimeseriesChunkFactor ||
		o.ByterangeChunkSize != o2.ByterangeChunkSize {
		return false
	}
	if (o.Index == nil || o2.Index == nil) || !o.Index.Equal(o2.Index) {
		return false
	}
	switch o.ProviderID {
	case providers.RedisID:
		return o.Redis.Equal(o2.Redis)
	case providers.FilesystemID:
		return o.Filesystem.Equal(o2.Filesystem)
	case providers.BBoltID:
		return o.BBolt.Equal(o2.BBolt)
	case providers.BadgerDBID:
		return o.Badger.Equal(o2.Badger)
	default: // memory
		return o.Memory.Equal(o2.Memory)
	}
}

func (o *Options) Validate() (bool, error) {
	if restrictedNames.Contains(o.Name) {
		return false, ErrInvalidName
	}
	if o.Index == nil {
		return true, nil
	}
	if o.Index.MaxSizeBytes > 0 && o.Index.MaxSizeBackoffBytes > o.Index.MaxSizeBytes {
		return false, errMaxSizeBackoffBytesTooBig
	}
	if o.Index.MaxSizeObjects > 0 && o.Index.MaxSizeBackoffObjects > o.Index.MaxSizeObjects {
		return false, errMaxSizeBackoffObjectsTooBig
	}

	return true, nil
}

var (
	errMaxSizeBackoffBytesTooBig   = errors.New("MaxSizeBackoffBytes can't be larger than MaxSizeBytes")
	errMaxSizeBackoffObjectsTooBig = errors.New("MaxSizeBackoffObjects can't be larger than MaxSizeObjects")
)

// Initialize sets up the cache Options with default values and overlays
// any values that were set during YAML unmarshaling
func (o *Options) Initialize(name string) error {
	o.Name = name

	if o.Provider != "" {
		o.Provider = strings.TrimSpace(strings.ToLower(o.Provider))
		if n, ok := providers.Names[o.Provider]; ok {
			o.ProviderID = n
		}
	}

	if providers.UsesIndex(o.Provider) {
		if o.Index == nil {
			o.Index = index.New()
		}
	} else {
		o.Index = nil
	}
	if o.ProviderID == providers.RedisID {
		if o.Redis == nil {
			o.Redis = redis.New()
		}
	} else {
		o.Redis = nil
	}
	if o.ProviderID == providers.FilesystemID {
		if o.Filesystem == nil {
			o.Filesystem = filesystem.New()
		}
	} else {
		o.Filesystem = nil
	}
	if o.ProviderID == providers.BBoltID {
		if o.BBolt == nil {
			o.BBolt = bbolt.New()
		}
	} else {
		o.BBolt = nil
	}
	if o.ProviderID == providers.BadgerDBID {
		if o.Badger == nil {
			o.Badger = badger.New()
		}
	} else {
		o.Badger = nil
	}
	if o.ProviderID == providers.MemoryID {
		if o.Memory == nil {
			o.Memory = memory.New()
		}
	} else {
		o.Memory = nil
	}

	o.UseCacheChunking = defaults.DefaultUseCacheChunking

	if o.TimeseriesChunkFactor == 0 {
		o.TimeseriesChunkFactor = defaults.DefaultTimeseriesChunkFactor
	}
	if o.ByterangeChunkSize == 0 {
		o.ByterangeChunkSize = defaults.DefaultByterangeChunkSize
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

		if v.ProviderID == providers.RedisID {
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
	for k, o := range l {
		o.Name = k
		_, err := o.Validate()
		if err != nil {
			return err
		}
	}
	return nil
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

func (o *Options) ClearProviderOptions() {
	o.Index = nil
	o.Redis = nil
	o.Filesystem = nil
	o.BBolt = nil
	o.Badger = nil
	o.Memory = nil
}
