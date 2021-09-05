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
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

// Cache defines a a Memory Cache client that conforms to the Cache interface
type Cache struct {
	Name       string
	client     sync.Map
	Config     *options.Options
	Index      *index.Index
	Logger     interface{}
	locker     locks.NamedLocker
	lockPrefix string
}

// New returns a new memory cache as a Trickster Cache Interface type
func New() (cache.Cache, error) {
	c := &Cache{}
	c.SetLocker(locks.NewNamedLocker())
	c.Config = options.New()
	err := c.Connect()
	if err != nil {
		return nil, err
	}
	return c, nil
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

// Connect initializes the Cache
func (c *Cache) Connect() error {
	tl.Info(c.Logger, "memorycache setup", tl.Pairs{"name": c.Name,
		"maxSizeBytes": c.Config.Index.MaxSizeBytes, "maxSizeObjects": c.Config.Index.MaxSizeObjects})
	c.lockPrefix = c.Name + ".memory."
	c.client = sync.Map{}
	c.Index = index.NewIndex(c.Name, c.Config.Provider, nil, c.Config.Index, c.BulkRemove, nil, c.Logger)
	return nil
}

// StoreReference stores an object directly to the memory cache without requiring serialization
func (c *Cache) StoreReference(cacheKey string, data cache.ReferenceObject, ttl time.Duration) error {
	return c.store(cacheKey, nil, data, ttl, true)
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return c.store(cacheKey, data, nil, ttl, true)
}

func (c *Cache) store(cacheKey string, byteData []byte, refData cache.ReferenceObject,
	ttl time.Duration, updateIndex bool) error {

	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}

	var o1, o2 *index.Object
	var l int
	isDirect := byteData == nil && refData != nil
	if byteData != nil {
		l = len(byteData)
		metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "set", "none", float64(l))
		o1 = &index.Object{Key: cacheKey, Value: byteData, Expiration: exp}
		o2 = &index.Object{Key: cacheKey, Value: byteData, Expiration: exp}
	} else if refData != nil {
		metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "setDirect", "none", 0)
		o1 = &index.Object{Key: cacheKey, ReferenceValue: refData, Expiration: exp}
		o2 = &index.Object{Key: cacheKey, ReferenceValue: refData, Expiration: exp}
	}

	if o1 != nil && o2 != nil {
		nl, _ := c.locker.Acquire(c.lockPrefix + cacheKey)
		tl.Debug(c.Logger, "memorycache cache store",
			tl.Pairs{"cacheName": c.Name, "cacheKey": cacheKey, "length": l, "ttl": ttl, "is_direct": isDirect})
		c.client.Store(cacheKey, o1)
		if updateIndex {
			c.Index.UpdateObject(o2)
		}
		nl.Release()
	}

	return nil
}

// RetrieveReference looks for an object in cache and returns it (or an error if not found)
func (c *Cache) RetrieveReference(cacheKey string, allowExpired bool) (interface{},
	status.LookupStatus, error) {
	o, s, err := c.retrieve(cacheKey, allowExpired, true)
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
	o, s, err := c.retrieve(cacheKey, allowExpired, true)
	if err != nil {
		return nil, s, err
	}
	if o != nil {
		return o.Value, s, nil
	}
	return nil, s, nil
}

func (c *Cache) retrieve(cacheKey string, allowExpired bool, atime bool) (*index.Object,
	status.LookupStatus, error) {

	nl, _ := c.locker.RAcquire(c.lockPrefix + cacheKey)
	record, ok := c.client.Load(cacheKey)
	nl.RRelease()

	if ok {
		o := record.(*index.Object)
		o.Expiration = c.Index.GetExpiration(cacheKey)

		if allowExpired || o.Expiration.IsZero() || o.Expiration.After(time.Now()) {
			tl.Debug(c.Logger, "memory cache retrieve", tl.Pairs{"cacheKey": cacheKey})
			if atime {
				go c.Index.UpdateObjectAccessTime(cacheKey)
			}
			metrics.ObserveCacheOperation(c.Name, c.Config.Provider, "get", "hit", float64(len(o.Value)))
			return o, status.LookupStatusHit, nil
		}
		// Cache Object has been expired but not reaped, go ahead and delete it
		go c.remove(cacheKey, false)
	}
	metrics.ObserveCacheMiss(cacheKey, c.Name, c.Config.Provider)
	return nil, status.LookupStatusKeyMiss, cache.ErrKNF
}

// SetTTL updates the TTL for the provided cache object
func (c *Cache) SetTTL(cacheKey string, ttl time.Duration) {
	go c.Index.UpdateObjectTTL(cacheKey, ttl)
}

// Remove removes an object from the cache
func (c *Cache) Remove(cacheKey string) {
	c.remove(cacheKey, false)
}

func (c *Cache) remove(cacheKey string, isBulk bool) {
	nl, _ := c.locker.Acquire(c.lockPrefix + cacheKey)
	c.client.Delete(cacheKey)
	nl.Release()
	if !isBulk {
		go c.Index.RemoveObject(cacheKey)
	}
	metrics.ObserveCacheDel(c.Name, c.Config.Provider, 0)
}

// BulkRemove removes a list of objects from the cache
func (c *Cache) BulkRemove(cacheKeys []string) {
	wg := &sync.WaitGroup{}
	for _, cacheKey := range cacheKeys {
		wg.Add(1)
		go func(key string) {
			c.remove(key, true)
			wg.Done()
		}(cacheKey)
	}
	wg.Wait()
}

// Close is not used for Cache, and is here to fully prototype the Cache Interface
func (c *Cache) Close() error {
	if c.Index != nil {
		c.Index.Close()
	}
	return nil
}
