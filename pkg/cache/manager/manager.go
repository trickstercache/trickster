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
	"errors"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"golang.org/x/sync/singleflight"
)

// DefaultCloseDrainHardTimeout is the absolute upper bound a draining Close()
// will wait for in-flight cache operations before invoking the underlying
// client Close anyway. Prevents one stuck request from blocking reload forever.
const DefaultCloseDrainHardTimeout = 30 * time.Second

// ErrCacheClosed is returned by Store/Retrieve/Remove when invoked after
// Close() has started draining. Handlers should treat this as a transient
// failure (config reload in progress) and respond to the client without
// touching the cache.
var ErrCacheClosed = errors.New("cache is closed")

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
	return cm
}

// Manager implements the cache.Cache interface for Trickster, providing an abstracted
// cache layer with metrics, locking, and optional index / LRU-key-reaper.
//
// Manager also tracks in-flight Store/Retrieve/Remove operations so that
// Close() can drain them before tearing down the underlying client. This is
// the reload-safety contract: when the daemon replaces a cache instance on
// config reload, Close() on the old Manager blocks until handlers that still
// hold a reference have finished, with a hard timeout fallback so a single
// stuck request can't block reload forever.
type Manager struct {
	cache.Client
	originalCli cache.Client
	sf          singleflight.Group
	config      *options.Options
	opts        CacheOptions

	// mu serializes acquire/release vs Close. WaitGroup forbids concurrent
	// Add and Wait, so the closing flag and Add(1) live under one lock.
	mu sync.Mutex
	// inflight counts active Store/Retrieve/Remove calls. Close() waits for
	// it to reach zero before invoking the underlying client Close.
	inflight sync.WaitGroup
	// closing is set once Close() begins so new operations short-circuit.
	closing bool
	// closeDrainTimeout bounds how long Close() waits for inflight to drain.
	closeDrainTimeout time.Duration
}

// SetCloseDrainTimeout overrides the hard timeout used by Close(). A zero or
// negative value resets to DefaultCloseDrainHardTimeout. Safe to call once
// during construction.
func (cm *Manager) SetCloseDrainTimeout(d time.Duration) {
	cm.closeDrainTimeout = d
}

// acquire increments the inflight counter if the cache is not closing.
// Returns false if Close() has already started.
func (cm *Manager) acquire() bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.closing {
		return false
	}
	cm.inflight.Add(1)
	return true
}

// release decrements the inflight counter.
func (cm *Manager) release() {
	cm.inflight.Done()
}

func (cm *Manager) StoreReference(cacheKey string, data cache.ReferenceObject, ttl time.Duration) error {
	if !cm.acquire() {
		return ErrCacheClosed
	}
	defer cm.release()
	metrics.ObserveCacheOperation(cm.config.Name, cm.config.Provider, "setDirect", "none", float64(data.Size()))
	logger.Debug("cache store", logging.Pairs{"key": cacheKey, "provider": cm.config.Provider})
	return cm.Client.(cache.MemoryCache).StoreReference(cacheKey, data, ttl)
}

func (cm *Manager) Store(cacheKey string, byteData []byte, ttl time.Duration) error {
	if !cm.acquire() {
		return ErrCacheClosed
	}
	defer cm.release()
	metrics.ObserveCacheOperation(cm.config.Name, cm.config.Provider, "set", "none", float64(len(byteData)))
	logger.Debug("cache store", logging.Pairs{"key": cacheKey, "provider": cm.config.Provider})
	return cm.Client.Store(cacheKey, byteData, ttl)
}

func (cm *Manager) observeRetrieval(cacheKey string, size int, s status.LookupStatus, err error) {
	switch {
	case errors.Is(err, cache.ErrKNF) || s == status.LookupStatusKeyMiss:
		logger.Debug("cache miss", logging.Pairs{"key": cacheKey, "provider": cm.config.Provider})
		metrics.ObserveCacheMiss(cm.config.Name, cm.config.Provider)
	case err != nil:
		logger.Debug("cache retrieve failed", logging.Pairs{"key": cacheKey, "provider": cm.config.Provider})
		metrics.ObserveCacheEvent(cm.config.Name, cm.config.Provider, "error", "failed to retrieve cache entry")
	case s == status.LookupStatusHit:
		logger.Debug("cache retrieve", logging.Pairs{"key": cacheKey, "provider": cm.config.Provider})
		metrics.ObserveCacheOperation(cm.config.Name, cm.config.Provider, "get", "hit", float64(size))
	}
}

func (cm *Manager) RetrieveReference(cacheKey string) (any, status.LookupStatus, error) {
	if !cm.acquire() {
		return nil, status.LookupStatusError, ErrCacheClosed
	}
	defer cm.release()
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
	if !cm.acquire() {
		return nil, status.LookupStatusError, ErrCacheClosed
	}
	defer cm.release()
	val, err, shared := cm.sf.Do(cacheKey, func() (any, error) {
		b, s, err := cm.Client.Retrieve(cacheKey)
		cm.observeRetrieval(cacheKey, len(b), s, err)
		return &retrieveResult{
			Data:   b,
			Status: s,
		}, err
	})
	rr := val.(*retrieveResult)
	s := rr.Status
	if shared {
		if status.IsSuccessful(s) {
			s = status.LookupStatusProxyHit
		} else {
			s = status.LookupStatusProxyError
		}
	}
	return rr.Data.([]byte), s, err
}

func (cm *Manager) Remove(cacheKeys ...string) error {
	if len(cacheKeys) == 0 {
		return nil
	}
	if !cm.acquire() {
		return ErrCacheClosed
	}
	defer cm.release()
	metrics.ObserveCacheDel(cm.config.Name, cm.config.Provider, float64(len(cacheKeys)))
	logger.Debug("cache remove", logging.Pairs{"keys": cacheKeys, "provider": cm.config.Provider})
	return cm.Client.Remove(cacheKeys...)
}

// Close marks the Manager as closing, waits for in-flight cache operations
// to drain, then closes the underlying client. The drain wait is bounded by
// closeDrainTimeout (default DefaultCloseDrainHardTimeout); if it elapses the
// underlying Close is invoked anyway so reload cannot hang.
//
// Subsequent Store/Retrieve/Remove calls return ErrCacheClosed without
// touching the underlying client. Close is safe to call once; further calls
// no-op the drain and forward to the client.
func (cm *Manager) Close() error {
	cm.mu.Lock()
	first := !cm.closing
	cm.closing = true
	cm.mu.Unlock()
	if !first {
		return cm.Client.Close()
	}
	timeout := cm.closeDrainTimeout
	if timeout <= 0 {
		timeout = DefaultCloseDrainHardTimeout
	}
	done := make(chan struct{})
	go func() {
		cm.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		logger.Warn("cache close drain timed out, closing underlying client anyway",
			logging.Pairs{
				"cache":   cm.config.Name,
				"timeout": timeout.String(),
			})
	}
	return cm.Client.Close()
}

func (cm *Manager) Connect() error {
	if err := cm.originalCli.Connect(); err != nil {
		return err
	}
	if cm.opts.UseIndex {
		cm.Client = index.NewIndexedClient(
			cm.config.Name,
			cm.config.Provider,
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
