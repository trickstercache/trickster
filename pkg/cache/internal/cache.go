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

package internal

import (
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
)

// Initialize a new cache struct
func NewCache(name, lockPrefix string, opts *CacheOptions) *Cache {
	if opts == nil {
		panic("cache options cannot be nil")
	}
	return &Cache{
		Name:       name,
		lockPrefix: lockPrefix,
		Config:     opts.Options,
		options:    opts,
	}
}

// CacheOptions contains the options for a cache
type CacheOptions struct {
	Options  *options.Options
	Connect  func() error
	Store    func(cacheKey string, byteData []byte, refData cache.ReferenceObject, ttl time.Duration, updateIndex bool) error
	Retrieve func(cacheKey string, allowExpired bool, atime bool) (*index.Object, status.LookupStatus, error)
	Delete   func(cacheKey string) error
	SetTTL   func(cacheKey string, ttl time.Duration)
}

// Cache implements the cache.Cache interface, and is meant to implement common functionality for all cache implementations
type Cache struct {
	Name       string
	Config     *options.Options
	Index      *index.Index // TODO: ensure usage is optional
	locker     locks.NamedLocker
	lockPrefix string
	options    *CacheOptions
}

// Locker returns the cache's locker
func (c *Cache) Locker() locks.NamedLocker {
	return c.locker
}

// SetLocker sets the cache's locker
func (c *Cache) SetLocker(l locks.NamedLocker) {
	c.locker = l
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *options.Options {
	return c.Config
}

// SetTTL updates the TTL for the provided cache object
func (c *Cache) SetTTL(cacheKey string, ttl time.Duration) {
	if c.Index != nil {
		c.Index.UpdateObjectTTL(cacheKey, ttl)
	}
	if c.options.SetTTL != nil {
		c.options.SetTTL(cacheKey, ttl)
	}
}

// Remove removes an object from the cache
func (c *Cache) Remove(cacheKey string) {
	c.remove(cacheKey, false)
}

func (c *Cache) Connect() error {
	return c.options.Connect()
}

// RetrieveReference looks for an object in cache and returns it (or an error if not found)
func (c *Cache) RetrieveReference(cacheKey string, allowExpired bool) (any,
	status.LookupStatus, error) {
	o, s, err := c.options.Retrieve(cacheKey, allowExpired, true)
	if err != nil {
		return nil, s, err
	}
	if o != nil {
		return o.ReferenceValue, s, nil
	}
	return nil, s, nil
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string, allowExpired bool) ([]byte, status.LookupStatus, error) {
	o, s, err := c.options.Retrieve(cacheKey, allowExpired, true)
	if err != nil {
		return nil, s, err
	}
	if o != nil {
		return o.Value, s, nil
	}
	return nil, s, nil
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return c.options.Store(cacheKey, data, nil, ttl, true)
}

// StoreReference stores an object directly to the memory cache without requiring serialization
func (c *Cache) StoreReference(cacheKey string, data cache.ReferenceObject, ttl time.Duration) error {
	return c.options.Store(cacheKey, nil, data, ttl, true)
}

func (c *Cache) remove(cacheKey string, isBulk bool) {
	nl, _ := c.locker.Acquire(c.lockPrefix + cacheKey)
	err := c.options.Delete(cacheKey)
	nl.Release()
	if !isBulk && err == nil && c.Index != nil {
		go c.Index.RemoveObject(cacheKey)
	}
	metrics.ObserveCacheDel(c.Name, c.Config.Provider, 0)
}

// BulkRemove removes a list of objects from the cache
func (c *Cache) BulkRemove(cacheKeys []string) {
	wg := sync.WaitGroup{}
	wg.Add(len(cacheKeys))
	for _, cacheKey := range cacheKeys {
		go func(key string) {
			c.remove(key, true)
			wg.Done()
		}(cacheKey)
	}
	wg.Wait()
}

// Close is not used for Cache
func (c *Cache) Close() error {
	if c.Index != nil {
		c.Index.Close()
	}
	return nil
}
