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

// Package memory is the memory implementation of the Trickster Cache
// and uses ristretto (TinyLFU admission-controlled cache) to manage cache objects
package memory

import (
	"time"

	"github.com/dgraph-io/ristretto/v2"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	memoryopts "github.com/trickstercache/trickster/v2/pkg/cache/memory/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
)

var (
	// Cache implements the cache.Client and cache.MemoryClient interfaces
	_ cache.Client      = &Cache{}
	_ cache.MemoryCache = &Cache{}
)

// Cache defines a Memory Cache client that conforms to the Cache interface
type Cache struct {
	Name   string
	Config *options.Options
	client *ristretto.Cache[string, any]
}

// New returns a new memory cache as a Trickster Cache Interface type
func New(name string, cfg *options.Options) *Cache {
	if cfg == nil {
		cfg = options.New()
	}

	// Determine max cache size — prefer memory-specific option, fall back to index for compat.
	maxSize := memoryopts.DefaultMaxSizeBytes
	if cfg.Memory != nil && cfg.Memory.MaxSizeBytes > 0 {
		maxSize = cfg.Memory.MaxSizeBytes
	} else if cfg.Index != nil && cfg.Index.MaxSizeBytes > 0 {
		maxSize = cfg.Index.MaxSizeBytes
	}

	numCounters := memoryopts.DefaultNumCounters
	if cfg.Memory != nil && cfg.Memory.NumCounters > 0 {
		numCounters = cfg.Memory.NumCounters
	}

	// Configure ristretto with appropriate settings for Trickster
	config := &ristretto.Config[string, any]{
		MaxCost:     maxSize,
		NumCounters: numCounters,
		// BufferItems: number of keys per Get buffer (64 is ristretto default, not recommended to tune)
		BufferItems: 64,
		Cost: func(value any) int64 {
			switch v := value.(type) {
			case []byte:
				return int64(len(v))
			case cache.ReferenceObject:
				return int64(v.Size())
			default:
				return 1 // fallback for unknown types
			}
		},
	}

	client, err := ristretto.NewCache(config)
	if err != nil {
		// This should never happen with valid config, but handle gracefully
		panic("failed to create ristretto cache: " + err.Error())
	}

	c := &Cache{
		Name:   name,
		Config: cfg,
		client: client,
	}
	return c
}

func (c *Cache) Remove(cacheKeys ...string) error {
	for _, k := range cacheKeys {
		c.client.Del(k)
	}
	// Wait for buffered deletes to complete to ensure synchronous semantics
	c.client.Wait()
	return nil
}

func (c *Cache) Close() error {
	c.client.Close()
	return nil
}

// Connect initializes the Cache
func (c *Cache) Connect() error {
	return nil
}

// StoreReference stores an object directly to the memory cache without requiring serialization
func (c *Cache) StoreReference(cacheKey string, data cache.ReferenceObject, ttl time.Duration) error {
	return c.store(cacheKey, nil, data, ttl)
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return c.store(cacheKey, data, nil, ttl)
}

func (c *Cache) store(cacheKey string, byteData []byte, refData cache.ReferenceObject,
	ttl time.Duration,
) error {
	var value any
	if byteData != nil {
		value = byteData
	} else if refData != nil {
		value = refData
	}

	if value != nil {
		if ttl > 0 {
			c.client.SetWithTTL(cacheKey, value, 0, ttl) // 0 = use Cost function
		} else {
			c.client.Set(cacheKey, value, 0) // 0 = use Cost function
		}
		// Wait for buffered write to complete to ensure synchronous semantics
		c.client.Wait()
	}

	return nil
}

// RetrieveReference looks for an object in cache and returns it (or an error if not found)
func (c *Cache) RetrieveReference(cacheKey string) (any,
	status.LookupStatus, error,
) {
	o, s, err := c.retrieve(cacheKey)
	if err != nil {
		return nil, s, err
	}
	return o, s, nil
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string) ([]byte, status.LookupStatus, error) {
	o, s, err := c.retrieve(cacheKey)
	if err != nil {
		return nil, s, err
	}
	if o != nil {
		return o.([]byte), s, nil
	}
	return nil, s, nil
}

func (c *Cache) retrieve(cacheKey string) (any,
	status.LookupStatus, error,
) {
	record, ok := c.client.Get(cacheKey)
	if ok {
		return record, status.LookupStatusHit, nil
	}
	return nil, status.LookupStatusKeyMiss, cache.ErrKNF
}
