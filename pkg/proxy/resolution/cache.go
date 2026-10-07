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

package resolution

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/dns/resolver"

	"golang.org/x/sync/singleflight"
)

type lookupResult int8

const (
	resultHit lookupResult = iota
	resultMiss
	resultStale
	resultNegative
	resultError
	resultCount
)

var lookupResultNames = [resultCount]string{"hit", "miss", "stale", "negative", "error"}

const (
	// maxCacheEntries bounds each cache, since owner names can derive from client-supplied hosts
	maxCacheEntries = 8192
	// lookupTimeout bounds one shared lookup, which outlives a dial whose context ends first
	lookupTimeout = 5 * time.Second
	// staleRefreshWait bounds how long a dial waits on a refresh before serving the last good answer
	staleRefreshWait = 500 * time.Millisecond
)

// errNoTargets marks an answer with no usable records; it is cached like NXDOMAIN
var errNoTargets = errors.New("dns answer has no usable records")

type fetchFunc[T any] func(ctx context.Context, name string) (T, time.Duration, error)

type cacheEntry[T any] struct {
	val T
	// err is set on a negative entry
	err error
	// expires is when the entry needs a refresh
	expires time.Time
	// staleUntil is how long a positive entry may serve while refreshes fail
	staleUntil time.Time
}

// ttlCache caches answers per name for their TTL clamped to [minTTL, maxTTL], shares in-flight
// lookups, and serves the last good answer while refreshes fail
type ttlCache[T any] struct {
	mtx     sync.RWMutex
	entries map[string]*cacheEntry[T]
	flight  singleflight.Group
	fetch   fetchFunc[T]
	now     func() time.Time

	minTTL, maxTTL, negativeTTL, staleWait time.Duration
}

func newTTLCache[T any](fetch fetchFunc[T], minTTL, maxTTL, negativeTTL time.Duration) *ttlCache[T] {
	return &ttlCache[T]{
		entries:     make(map[string]*cacheEntry[T]),
		fetch:       fetch,
		now:         time.Now,
		minTTL:      minTTL,
		maxTTL:      maxTTL,
		negativeTTL: negativeTTL,
		staleWait:   staleRefreshWait,
	}
}

type flightResult[T any] struct {
	val T
	res lookupResult
}

// get returns the answer for name, refreshing it when it has expired; while a last good answer
// can serve, the caller waits at most staleWait for the refresh
func (c *ttlCache[T]) get(ctx context.Context, name string) (T, lookupResult, error) {
	c.mtx.RLock()
	e := c.entries[name]
	c.mtx.RUnlock()
	now := c.now()
	if e != nil && now.Before(e.expires) {
		if e.err != nil {
			var zero T
			return zero, resultNegative, e.err
		}
		return e.val, resultHit, nil
	}
	ch := c.flight.DoChan(name, func() (any, error) {
		val, res, err := c.refresh(name)
		return flightResult[T]{val: val, res: res}, err
	})
	if e != nil && e.err == nil && now.Before(e.staleUntil) {
		t := time.NewTimer(c.staleWait)
		defer t.Stop()
		select {
		case <-ctx.Done():
			var zero T
			return zero, resultError, ctx.Err()
		case r := <-ch:
			return flightReturn[T](r)
		case <-t.C:
			// the last good answer may have aged past max_ttl during the wait
			if c.now().Before(e.staleUntil) {
				return e.val, resultStale, nil
			}
		}
	}
	select {
	case <-ctx.Done():
		var zero T
		return zero, resultError, ctx.Err()
	case r := <-ch:
		return flightReturn[T](r)
	}
}

func flightReturn[T any](r singleflight.Result) (T, lookupResult, error) {
	fr, _ := r.Val.(flightResult[T])
	return fr.val, fr.res, r.Err
}

func (c *ttlCache[T]) refresh(name string) (T, lookupResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	val, ttl, err := c.fetch(ctx, name)
	now := c.now()
	if err == nil {
		c.put(name, &cacheEntry[T]{
			val:        val,
			expires:    now.Add(min(max(ttl, c.minTTL), c.maxTTL)),
			staleUntil: now.Add(c.maxTTL),
		}, now)
		return val, resultMiss, nil
	}
	var zero T
	if errors.Is(err, resolver.ErrNotFound) || errors.Is(err, errNoTargets) {
		// an authoritative answer that the name has nothing to dial replaces any last good one
		c.put(name, &cacheEntry[T]{err: err, expires: now.Add(c.negativeTTL)}, now)
		return zero, resultNegative, err
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if prev := c.entries[name]; prev != nil && prev.err == nil && now.Before(prev.staleUntil) {
		// keep serving the last good answer, retrying no sooner than minTTL from now
		c.entries[name] = &cacheEntry[T]{
			val:        prev.val,
			expires:    minTime(now.Add(c.minTTL), prev.staleUntil),
			staleUntil: prev.staleUntil,
		}
		return prev.val, resultStale, nil
	}
	// a failed lookup with nothing to serve is held briefly, so that every dial does not wait on it
	c.putLocked(name, &cacheEntry[T]{err: err, expires: now.Add(c.negativeTTL)}, now)
	return zero, resultError, err
}

// prime stores a positive answer obtained elsewhere, unless the cached entry is fresh already
func (c *ttlCache[T]) prime(name string, val T, ttl time.Duration) {
	now := c.now()
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if e := c.entries[name]; e != nil && e.err == nil && now.Before(e.expires) {
		return
	}
	c.putLocked(name, &cacheEntry[T]{
		val:        val,
		expires:    now.Add(min(max(ttl, c.minTTL), c.maxTTL)),
		staleUntil: now.Add(c.maxTTL),
	}, now)
}

func (c *ttlCache[T]) put(name string, e *cacheEntry[T], now time.Time) {
	c.mtx.Lock()
	c.putLocked(name, e, now)
	c.mtx.Unlock()
}

func (c *ttlCache[T]) putLocked(name string, e *cacheEntry[T], now time.Time) {
	if _, ok := c.entries[name]; !ok && len(c.entries) >= maxCacheEntries {
		c.evictLocked(now)
	}
	c.entries[name] = e
}

// evictLocked removes every entry that can no longer serve; when none can be removed that way,
// it removes one arbitrary entry, relying on Go's randomized map iteration order
func (c *ttlCache[T]) evictLocked(now time.Time) {
	for k, e := range c.entries {
		if !now.Before(e.expires) && !now.Before(e.staleUntil) {
			delete(c.entries, k)
		}
	}
	if len(c.entries) < maxCacheEntries {
		return
	}
	for k := range c.entries {
		delete(c.entries, k)
		return
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
