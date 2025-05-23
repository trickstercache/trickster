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

package manager

import (
	"path/filepath"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"golang.org/x/sync/singleflight"
)

// Provide initialization options to the Manager / cache.Cache creation
type CacheOptions struct {
	UseIndex     bool
	IndexCliOpts index.IndexedClientOptions
}

func NewCache(cli cache.Client, cacheOpts CacheOptions, cacheConfig *options.Options) cache.Cache {
	cm := &Manager{
		Client:      cli,
		originalCli: cli,
		config:      cacheConfig,
		opts:        cacheOpts,
	}
	cm.locker = locks.NewNamedLocker()
	return cm
}

// Manager implements the cache.Cache interface for Trickster, providing an abstracted
// cache layer with metrics, locking, and optional index / LRU-key-reaper.
type Manager struct {
	cache.Client
	originalCli cache.Client
	sf          singleflight.Group
	config      *options.Options
	locker      locks.NamedLocker
	opts        CacheOptions
}

func (cm *Manager) StoreReference(cacheKey string, data cache.ReferenceObject, ttl time.Duration) error {
	nl, _ := cm.locker.Acquire(filepath.Join(cm.config.Name, cm.config.Provider, cacheKey))
	defer nl.Release()
	metrics.ObserveCacheOperation(cm.config.Name, cm.config.Provider, "setDirect", "none", float64(data.Size()))
	logger.Debug("cache store", logging.Pairs{"key": cacheKey, "provider": cm.config.Provider})
	return cm.Client.(cache.MemoryCache).StoreReference(cacheKey, data, ttl)
}

func (cm *Manager) Store(cacheKey string, byteData []byte, ttl time.Duration) error {
	nl, _ := cm.locker.Acquire(filepath.Join(cm.config.Name, cm.config.Provider, cacheKey))
	defer nl.Release()
	metrics.ObserveCacheOperation(cm.config.Name, cm.config.Provider, "set", "none", float64(len(byteData)))
	logger.Debug("cache store", logging.Pairs{"key": cacheKey, "provider": cm.config.Provider})
	return cm.Client.Store(cacheKey, byteData, ttl)
}

func (cm *Manager) observeRetrieval(cacheKey string, size int, s status.LookupStatus, err error) {
	if err == cache.ErrKNF || s == status.LookupStatusKeyMiss {
		logger.Debug("cache miss", logging.Pairs{"key": cacheKey, "provider": cm.config.Provider})
		metrics.ObserveCacheMiss(cm.config.Name, cm.config.Provider)
	} else if err != nil {
		logger.Debug("cache retrieve failed", logging.Pairs{"key": cacheKey, "provider": cm.config.Provider})
		metrics.ObserveCacheEvent(cm.config.Name, cm.config.Provider, "error", "failed to retrieve cache entry")
	} else if s == status.LookupStatusHit {
		logger.Debug("cache retrieve", logging.Pairs{"key": cacheKey, "provider": cm.config.Provider})
		metrics.ObserveCacheOperation(cm.config.Name, cm.config.Provider, "get", "hit", float64(size))
	}
}

func (cm *Manager) RetrieveReference(cacheKey string) (any, status.LookupStatus, error) {
	nl, _ := cm.locker.RAcquire(filepath.Join(cm.config.Name, cm.config.Provider, cacheKey))
	defer nl.RRelease()
	v, s, err := cm.Client.(cache.MemoryCache).RetrieveReference(cacheKey)
	if ro, ok := v.(cache.ReferenceObject); ok {
		cm.observeRetrieval(cacheKey, ro.Size(), s, err)
	}
	return v, s, err
}

type retrieveResult struct {
	Data   any
	Status status.LookupStatus
}

func (cm *Manager) Retrieve(cacheKey string) ([]byte, status.LookupStatus, error) {
	val, err, _ := cm.sf.Do(cacheKey, func() (any, error) {
		nl, _ := cm.locker.RAcquire(filepath.Join(cm.config.Name, cm.config.Provider, cacheKey))
		defer nl.RRelease()
		b, s, err := cm.Client.Retrieve(cacheKey)
		cm.observeRetrieval(cacheKey, len(b), s, err)
		return &retrieveResult{
			Data:   b,
			Status: s,
		}, err
	})
	rr := val.(*retrieveResult)
	return rr.Data.([]byte), rr.Status, err
}

func (cm *Manager) Remove(cacheKeys ...string) error {
	for _, k := range cacheKeys {
		nl, _ := cm.locker.Acquire(filepath.Join(cm.config.Name, cm.config.Provider, k))
		defer nl.Release()
	}
	metrics.ObserveCacheDel(cm.config.Name, cm.config.Provider, float64(len(cacheKeys)-1))
	logger.Debug("cache remove", logging.Pairs{"keys": cacheKeys, "provider": cm.config.Provider})
	return cm.Client.Remove(cacheKeys...)
}

func (cm *Manager) Connect() error {
	if err := cm.originalCli.Connect(); err != nil {
		return err
	}
	if cm.opts.UseIndex {
		cm.Client = index.NewIndexedClient(
			cm.config.Name,
			cm.config.Provider,
			nil,
			cm.config.Index,
			cm.originalCli,
			func(ico *index.IndexedClientOptions) {
				*ico = cm.opts.IndexCliOpts
			},
		)
	}
	return nil
}

func (cm *Manager) Configuration() *options.Options {
	return cm.config
}

func (cm *Manager) Locker() locks.NamedLocker {
	return cm.locker
}
func (cm *Manager) SetLocker(l locks.NamedLocker) {
	cm.locker = l
}
