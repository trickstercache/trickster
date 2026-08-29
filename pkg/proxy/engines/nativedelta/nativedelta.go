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

// Package nativedelta is the transport-agnostic delta-proxy-cache engine for
// native (non-HTTP) protocols. It owns the shape of a delta execution — the
// bucket window, extent diffing against cached coverage, per-plan
// single-flight locking, sub-range fetch orchestration via the sqlanalyzer
// extent renderer, the versioned cache envelope, and the fail-open fallbacks —
// while each protocol supplies its own payload representation and the
// operations on it (fetch, merge, crop, finalize) through a Codec and
// DeltaOps. The engine was extracted from the MySQL native listener's proven
// delta implementation and serves both MySQL (vitess results) and Flight SQL
// (Arrow record batches via dataset.DataSet).
package nativedelta

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"golang.org/x/sync/singleflight"
)

// ErrUnmergeable marks a fetch or conversion failure that reflects data the
// protocol cannot model for delta caching (an unorderable group column, an
// unrepresentable schema, ...) rather than an upstream failure. The engine
// responds by recording a fallback marker against the plan and serving the
// request through the object path instead; other fetch errors surface to the
// caller as proxy errors.
var ErrUnmergeable = errors.New("result cannot be modeled for delta caching")

// Codec supplies the payload serialization for one protocol's cache entries.
type Codec[R any] interface {
	Marshal(payload R) ([]byte, error)
	Unmarshal(data []byte) (R, error)
	// Size approximates the heap retained by payload, for typed
	// memory-cache accounting.
	Size(payload R) int
}

// Config carries the engine's per-backend settings.
type Config struct {
	// Protocol labels logs and failure metrics (e.g. "mysql", "flightsql").
	Protocol string
	// BackendName labels logs.
	BackendName string
	// CacheClient resolves the cache at call time, so provider failover is
	// honored per request.
	CacheClient func() cache.Cache
	// CacheTTL bounds the lifetime of every stored entry.
	CacheTTL time.Duration
	// MaxObjectSize rejects oversized entries when positive.
	MaxObjectSize int64
	// ObserveCacheFailure and ObserveRewriteFailure are optional metric hooks.
	ObserveCacheFailure   func(reason string)
	ObserveRewriteFailure func(reason string)
}

// Engine is a delta/object cache engine generic over the protocol payload.
type Engine[R any] struct {
	cfg   Config
	codec Codec[R]

	lockMtx     sync.Mutex
	locks       map[string]*keyLock
	objectGroup singleflight.Group
}

// New returns an Engine using the provided configuration and payload codec.
func New[R any](cfg Config, codec Codec[R]) *Engine[R] {
	return &Engine[R]{cfg: cfg, codec: codec, locks: make(map[string]*keyLock)}
}

func (e *Engine[R]) cacheClient() cache.Cache {
	if e.cfg.CacheClient == nil {
		return nil
	}
	return e.cfg.CacheClient()
}

func (e *Engine[R]) observeCacheFailure(reason string) {
	if e.cfg.ObserveCacheFailure != nil {
		e.cfg.ObserveCacheFailure(reason)
	}
}

func (e *Engine[R]) observeRewriteFailure(reason string) {
	if e.cfg.ObserveRewriteFailure != nil {
		e.cfg.ObserveRewriteFailure(reason)
	}
}

// DeltaOps supplies the protocol-specific operations for one delta execution.
// Every callback captures its own request context.
type DeltaOps[R any] struct {
	// Fetch executes a rendered sub-range statement at the origin. An error
	// wrapping ErrUnmergeable routes the request to the object path; any
	// other error is a proxy error.
	Fetch func(statement string) (R, error)
	// FetchOriginal executes the caller's original statement, used whenever
	// the delta machinery cannot proceed (unsupported bounds, render
	// failure).
	FetchOriginal func() (R, error)
	// Merge combines the cached payload and freshly fetched parts, in order,
	// into one payload sorted on the time axis.
	Merge func(parts []R) (R, error)
	// CropResponse shapes a full-cache-hit response from the cached payload.
	CropResponse func(payload R, requested timeseries.Extent) (R, error)
	// Finalize shapes the response and the retained cache object from a
	// merged payload: response is cropped to requested; retained and
	// cacheExtents bound what is stored (retention limits, volatile-tail
	// trimming).
	Finalize func(merged R, allExtents timeseries.ExtentList,
		requested timeseries.Extent, now time.Time,
	) (response R, retained R, cacheExtents timeseries.ExtentList, err error)
	// ObjectFallback serves the request through the protocol's object-cache
	// path, used when a plan proves unmergeable.
	ObjectFallback func() (R, cachestatus.LookupStatus, error)
	// Shard optionally splits missing extents into origin-sized fetches.
	Shard func(missing timeseries.ExtentList) timeseries.ExtentList
	// RenderEmpty optionally renders the statement for a window with no
	// complete bucket; when nil, empty windows proxy the original statement.
	RenderEmpty func(lower, upper time.Time) (string, error)
}

// DeltaRequest describes one delta execution.
type DeltaRequest[R any] struct {
	// Key is the cache key for the plan's delta entry; FallbackKey marks the
	// plan unmergeable; EmptyKey caches empty-window responses.
	Key, FallbackKey, EmptyKey string
	Plan                       *sqlanalyzer.QueryPlan
	Now                        time.Time
	// RequireUpperBound proxies open-ended plans instead of running them to
	// the present.
	RequireUpperBound bool
	Ops               DeltaOps[R]
}

// ExecuteObject serves a request through the object cache: whole responses
// cached briefly and returned verbatim, with concurrent identical requests
// collapsed into one origin fetch.
func (e *Engine[R]) ExecuteObject(key string,
	fetch func() (R, error),
) (R, cachestatus.LookupStatus, error) {
	type execution struct {
		payload R
		status  cachestatus.LookupStatus
	}
	value, err, _ := e.objectGroup.Do(key, func() (any, error) {
		if cached, ok := e.Retrieve(key); ok && !cached.Marker {
			return execution{payload: cached.Payload, status: cachestatus.LookupStatusHit}, nil
		}
		payload, fetchErr := fetch()
		if fetchErr != nil {
			return execution{}, fetchErr
		}
		e.Store(key, &Entry[R]{Payload: payload})
		return execution{payload: payload, status: cachestatus.LookupStatusKeyMiss}, nil
	})
	if err != nil {
		var zero R
		return zero, cachestatus.LookupStatusProxyError, err
	}
	result := value.(execution)
	return result.payload, result.status, nil
}

// ExecuteDelta serves a request through the delta proxy cache: the plan's
// bucket window is diffed against cached coverage, only missing sub-ranges
// are fetched from the origin, and the merged result is cropped for the
// response and re-stored. Every failure mode falls open — to the object path
// for unmergeable data, or to a proxy of the original statement.
func (e *Engine[R]) ExecuteDelta(req DeltaRequest[R]) (R, cachestatus.LookupStatus, error) {
	var zero R
	lock := e.lock(req.Key)
	defer e.unlock(req.Key, lock)

	// A previous execution of this plan may have proven that its results
	// cannot be delta-merged. The marker is keyed on the plan rather than the
	// literal statement, because the statement's time bounds move with every
	// request.
	if _, blocked := e.Retrieve(req.FallbackKey); blocked {
		return req.Ops.ObjectFallback()
	}

	window, windowErr := BuildWindow(req.Plan, req.Now, req.RequireUpperBound)
	if windowErr != nil {
		payload, fetchErr := req.Ops.FetchOriginal()
		return payload, cachestatus.LookupStatusProxyOnly, fetchErr
	}
	if window.Empty {
		return e.executeEmptyDelta(req, window)
	}
	requested := window.Output

	cached, found := e.Retrieve(req.Key)
	cacheStatus := cachestatus.LookupStatusKeyMiss
	var covered timeseries.ExtentList
	if found {
		covered = cached.Extents
		cacheStatus = cachestatus.LookupStatusPartialHit
	}
	missing := covered.CalculateDeltas(window.Cacheable, req.Plan.Step)
	if len(missing) == 0 && found {
		cropped, cropErr := req.Ops.CropResponse(cached.Payload, requested)
		if cropErr == nil {
			return cropped, cachestatus.LookupStatusHit, nil
		}
		e.remove(req.Key, "invalid_cached_time_axis")
		cached, found, covered = nil, false, nil
		cacheStatus = cachestatus.LookupStatusKeyMiss
		missing = window.Cacheable
	}
	if found && len(missing) == 1 && missing[0].Start.Equal(window.Cacheable[0].Start) &&
		missing[0].End.Equal(window.Cacheable[0].End) {
		cacheStatus = cachestatus.LookupStatusRangeMiss
	}

	fetchExtents := missing
	if req.Ops.Shard != nil {
		fetchExtents = req.Ops.Shard(missing)
	}
	parts := make([]R, 0, len(fetchExtents)+1)
	if found {
		parts = append(parts, cached.Payload)
	}
	unmergeable := false
	for _, extent := range fetchExtents {
		statement, renderErr := req.Plan.RenderExtent(extent)
		if renderErr != nil {
			e.observeRewriteFailure("render_extent")
			payload, fetchErr := req.Ops.FetchOriginal()
			return payload, cachestatus.LookupStatusProxyOnly, fetchErr
		}
		payload, fetchErr := req.Ops.Fetch(statement)
		if fetchErr != nil {
			if errors.Is(fetchErr, ErrUnmergeable) {
				unmergeable = true
				break
			}
			return zero, cachestatus.LookupStatusProxyError, fetchErr
		}
		parts = append(parts, payload)
	}
	var merged R
	var mergeErr error
	if unmergeable {
		mergeErr = ErrUnmergeable
	} else {
		merged, mergeErr = req.Ops.Merge(parts)
	}
	if mergeErr != nil {
		logger.Warn("native delta result could not be modeled; using object cache",
			logging.Pairs{
				keys.Protocol:    e.cfg.Protocol,
				keys.BackendName: e.cfg.BackendName,
				keys.Detail:      mergeErr.Error(),
			})
		// Drop the delta entry: a stale part is one of the things that can
		// make a merge fail, and the retry after the marker expires should
		// start from a clean slate.
		if found {
			e.remove(req.Key, "unmergeable_delta_result")
		}
		// Record the failure against the plan so later requests skip the
		// delta fetch entirely instead of repeating it and discarding the
		// result.
		e.Store(req.FallbackKey, &Entry[R]{Marker: true})
		return req.Ops.ObjectFallback()
	}
	allExtents := covered.Merge(missing, req.Plan.Step)
	response, retained, cacheExtents, finalizeErr := req.Ops.Finalize(
		merged, allExtents, requested, time.Now())
	if finalizeErr != nil {
		return zero, cachestatus.LookupStatusProxyError, finalizeErr
	}
	e.Store(req.Key, &Entry[R]{Payload: retained, Extents: cacheExtents})
	return response, cacheStatus, nil
}

// executeEmptyDelta serves a window with no complete bucket: the normalized
// statement is rendered and its (typically empty) response cached whole.
func (e *Engine[R]) executeEmptyDelta(req DeltaRequest[R],
	window Window,
) (R, cachestatus.LookupStatus, error) {
	var zero R
	if req.Ops.RenderEmpty == nil {
		e.observeRewriteFailure("render_empty_extent")
		payload, err := req.Ops.FetchOriginal()
		return payload, cachestatus.LookupStatusProxyOnly, err
	}
	statement, err := req.Ops.RenderEmpty(window.Lower, window.Upper)
	if err != nil {
		e.observeRewriteFailure("render_empty_extent")
		payload, fetchErr := req.Ops.FetchOriginal()
		return payload, cachestatus.LookupStatusProxyOnly, fetchErr
	}
	if cached, ok := e.Retrieve(req.EmptyKey); ok && !cached.Marker {
		return cached.Payload, cachestatus.LookupStatusHit, nil
	}
	payload, err := req.Ops.Fetch(statement)
	if err != nil {
		return zero, cachestatus.LookupStatusProxyError, err
	}
	e.Store(req.EmptyKey, &Entry[R]{Payload: payload})
	return payload, cachestatus.LookupStatusKeyMiss, nil
}

// Remove deletes the entry stored at key.
func (e *Engine[R]) Remove(key string) {
	e.remove(key, "explicit_removal")
}

// keyLock serializes delta executions that share one cache key, so concurrent
// identical queries collapse to a single origin fetch.
type keyLock struct {
	sync.Mutex
	references int
}

func (e *Engine[R]) lock(key string) *keyLock {
	e.lockMtx.Lock()
	if e.locks == nil {
		e.locks = make(map[string]*keyLock)
	}
	lock := e.locks[key]
	if lock == nil {
		lock = &keyLock{}
		e.locks[key] = lock
	}
	lock.references++
	e.lockMtx.Unlock()
	lock.Lock()
	return lock
}

func (e *Engine[R]) unlock(key string, lock *keyLock) {
	lock.Unlock()
	e.lockMtx.Lock()
	lock.references--
	if lock.references == 0 && e.locks[key] == lock {
		delete(e.locks, key)
	}
	e.lockMtx.Unlock()
}

// Unmergeable wraps err so the engine treats it as data the protocol cannot
// model for delta caching, routing the request to the object path.
func Unmergeable(err error) error {
	if err == nil {
		return ErrUnmergeable
	}
	return fmt.Errorf("%w: %w", ErrUnmergeable, err)
}
