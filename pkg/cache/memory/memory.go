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
// and uses a sync.Map to manage cache objects
package memory

import (
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
)

var (
	// Cache implements the cache.Client and cache.MemoryClient interfaces
	_ cache.Client      = &Cache{}
	_ cache.MemoryCache = &Cache{}
)

// Cache defines a a Memory Cache client that conforms to the Cache interface
type Cache struct {
	Name   string
	Config *options.Options
	client sync.Map
}

// New returns a new memory cache as a Trickster Cache Interface type
func New(name string, cfg *options.Options) *Cache {
	if cfg == nil {
		cfg = options.New()
	}
	c := &Cache{
		Name:   name,
		Config: cfg,
	}
	return c
}

func (c *Cache) Remove(cacheKeys ...string) error {
	for _, k := range cacheKeys {
		c.client.Delete(k)
	}
	return nil
}

func (c *Cache) Close() error {
	c.client.Clear()
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
	ttl time.Duration) error {

	var o1, o2 any
	if byteData != nil {
		o1 = byteData
		o2 = byteData
	} else if refData != nil {
		o1 = refData
		o2 = refData
	}

	if o1 != nil && o2 != nil {
		c.client.Store(cacheKey, o1)
	}

	return nil
}

// RetrieveReference looks for an object in cache and returns it (or an error if not found)
func (c *Cache) RetrieveReference(cacheKey string) (any,
	status.LookupStatus, error) {
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
	status.LookupStatus, error) {
	record, ok := c.client.Load(cacheKey)
	if ok {
		return record, status.LookupStatusHit, nil
	}
	return nil, status.LookupStatusKeyMiss, cache.ErrKNF
}
