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

// Package nativedelta is the delta proxy cache of the native SQL listeners: it plans their
// fetches, whose rows are DataSets, and merges, crops, retains and caches the rows.
package nativedelta

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"golang.org/x/sync/singleflight"
)

// ErrUnmergeable marks a fetch or conversion failure that reflects data the
// protocol cannot model for delta caching (an unorderable group column, an
// unrepresentable schema, ...) rather than an upstream failure. The engine
// responds by recording a fallback marker against the plan and serving the
// request through the object path instead; other fetch errors surface to the
// caller as proxy errors.
var ErrUnmergeable = errors.New("result cannot be modeled for delta caching")

// Codec supplies the payload serialization for one protocol's object-tier cache entries.
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
	// RetentionPoints is timeseries_retention_factor: the newest buckets a delta entry keeps, and
	// above which a request counts against the retention metric. Zero disables both.
	RetentionPoints int
	// VolatileWindow and VolatileWindowPoints are the backend's volatile window: the newest complete
	// buckets a delta entry leaves out, so they are fetched again
	VolatileWindow       time.Duration
	VolatileWindowPoints int
	// PartialBucketTTL is how long the object tier keeps a partial bucket, and the answer to a range
	// holding no complete bucket; zero uses the backend default
	PartialBucketTTL time.Duration
	// Provider labels the partial bucket metric; empty uses Protocol
	Provider string
	// ObserveCacheFailure and ObserveRewriteFailure are optional metric hooks.
	ObserveCacheFailure   func(reason string)
	ObserveRewriteFailure func(reason string)
}

// Engine is a delta and object cache engine: delta entries hold rows as a DataSet, and object
// entries hold the protocol's own payload R.
type Engine[R any] struct {
	cfg     Config
	objects tier[R]
	deltas  tier[*Delta]

	lockMtx     sync.Mutex
	locks       map[string]*keyLock
	objectGroup singleflight.Group
}

// New returns an Engine using the provided configuration and object payload codec.
func New[R any](cfg Config, codec Codec[R]) *Engine[R] {
	e := &Engine[R]{cfg: cfg, locks: make(map[string]*keyLock)}
	e.objects = tier[R]{cfg: &e.cfg, codec: codec}
	e.deltas = tier[*Delta]{cfg: &e.cfg, codec: deltaCodec{}}
	return e
}

func (e *Engine[R]) gridExtents(el timeseries.ExtentList,
	plan *sqlanalyzer.QueryPlan,
) timeseries.ExtentList {
	// a bound between buckets would render or record a partial bucket as though it were
	// complete, so ranges are narrowed to the whole buckets they hold
	out, offGrid := el.ClampToGrid(plan.Step, plan.Phase)
	if offGrid > 0 {
		metrics.TimeseriesOffGridExtents.WithLabelValues(e.cfg.BackendName,
			e.cfg.Protocol).Add(float64(offGrid))
		logger.Debug("narrowed off-grid extents to whole buckets",
			logging.Pairs{
				keys.Protocol: e.cfg.Protocol, keys.BackendName: e.cfg.BackendName,
				keys.Extent: el.String(),
			})
	}
	return out
}

func (e *Engine[R]) observeRewriteFailure(reason string) {
	if e.cfg.ObserveRewriteFailure != nil {
		e.cfg.ObserveRewriteFailure(reason)
	}
}

// DeltaOps supplies the protocol-specific operations for one delta execution.
// Every callback captures its own request context.
type DeltaOps[R any] struct {
	// Fetch runs a rendered sub-range statement and models its rows, sorted in each series. An
	// ErrUnmergeable sends the request to the object path; other errors are proxy errors.
	Fetch func(statement string) (*Delta, error)
	// FetchOriginal executes the caller's original statement, used whenever
	// the delta machinery cannot proceed (unsupported bounds, render
	// failure).
	FetchOriginal func() (R, error)
	// ObjectFallback serves the request through the protocol's object-cache
	// path, used when a plan proves unmergeable.
	ObjectFallback func() (R, cachestatus.LookupStatus, error)
	// SameHeader reports whether rows under two headers can be merged; nil compares their bytes.
	SameHeader func(a, b []byte) bool
	// Shard optionally splits missing extents into origin-sized fetches.
	Shard func(missing timeseries.ExtentList) timeseries.ExtentList
	// FetchPartial answers a statement of partial buckets alone from the object tier, keeping it for ttl
	// under its own key; when nil, such statements are proxied.
	FetchPartial func(ctx context.Context, statement string, ttl time.Duration) (R, cachestatus.LookupStatus, error)
	// Model converts an object-tier result into rows, as Fetch models a fetch.
	Model func(R) (*Delta, error)
	// ConcurrentPartials fetches the partial buckets beside the interior, for protocols whose origin
	// calls may overlap; otherwise they are fetched after it.
	ConcurrentPartials bool
}

// Outcome is a delta execution's response: the delta tier's rows, or else the object tier's or
// the origin's own result.
type Outcome[R any] struct {
	Delta  *Delta
	Object R
}

// DeltaRequest describes one delta execution.
type DeltaRequest[R any] struct {
	// Key is the cache key for the plan's delta entry; FallbackKey marks the
	// plan unmergeable.
	Key, FallbackKey string
	// Statement is the client's own statement.
	Statement string
	// Context bounds the request's partial bucket fetches; nil uses context.Background.
	Context context.Context
	Plan    *sqlanalyzer.QueryPlan
	Now     time.Time
	// StepAlignment is the request's mode; zero, off or one the plan doesn't support uses its default.
	StepAlignment timeseries.StepAlignment
	// RequireUpperBound proxies open-ended plans instead of running them to
	// the present.
	RequireUpperBound bool
	Ops               DeltaOps[R]
}

// ExecuteObject serves a request from the object cache, storing a whole response for ttl (zero keeps
// CacheTTL) and collapsing concurrent identical requests into one origin fetch.
func (e *Engine[R]) ExecuteObject(key string, ttl time.Duration,
	fetch func() (R, error),
) (R, cachestatus.LookupStatus, error) {
	if ttl <= 0 {
		ttl = e.cfg.CacheTTL
	}
	type execution struct {
		payload R
		status  cachestatus.LookupStatus
	}
	value, err, _ := e.objectGroup.Do(key, func() (any, error) {
		if cached, ok := e.objects.retrieve(key); ok && !cached.Marker {
			return execution{payload: cached.Payload, status: cachestatus.LookupStatusHit}, nil
		}
		payload, fetchErr := fetch()
		if fetchErr != nil {
			return execution{}, fetchErr
		}
		e.objects.store(key, &Entry[R]{Payload: payload}, ttl)
		return execution{payload: payload, status: cachestatus.LookupStatusKeyMiss}, nil
	})
	if err != nil {
		var zero R
		return zero, cachestatus.LookupStatusProxyError, err
	}
	result := value.(execution)
	return result.payload, result.status, nil
}

// ExecuteDelta serves cached rows, fetching only missing buckets, plus partial edge buckets from the
// object tier; failures fall open to the object path or the original
func (e *Engine[R]) ExecuteDelta(req DeltaRequest[R]) (Outcome[R], cachestatus.LookupStatus, error) {
	window, windowErr := BuildWindow(req.Plan, req.Now, req.RequireUpperBound, e.stepAlignment(req.StepAlignment))
	if windowErr != nil {
		return e.original(req)
	}
	if window.Empty {
		return e.executeUnaligned(req)
	}
	lock := e.lock(req.Key)
	// A previous execution of this plan may have proven that its results
	// cannot be delta-merged. The marker is keyed on the plan rather than the
	// literal statement, because the statement's time bounds move with every
	// request.
	if _, blocked := e.objects.retrieve(req.FallbackKey); blocked {
		e.unlock(req.Key, lock)
		return e.objectFallback(req)
	}
	var partials *partialFetches
	if req.Ops.ConcurrentPartials {
		partials = e.fetchPartials(req, &window, true)
	}
	answer, lookup, err := e.executeInterior(req, &window)
	// no lock is held across a partial bucket's origin round trip
	e.unlock(req.Key, lock)
	if err != nil || answer.Delta == nil {
		// the response holds none of their rows, so it never waits for them
		partials.abandon()
		return answer, lookup, err
	}
	if partials == nil {
		partials = e.fetchPartials(req, &window, false)
	}
	answer.Delta = e.withPartials(req, answer.Delta, partials)
	return answer, lookup, nil
}

func (e *Engine[R]) executeInterior(req DeltaRequest[R], window *Window,
) (Outcome[R], cachestatus.LookupStatus, error) {
	var none Outcome[R]
	metrics.ObserveTimeseriesRetentionFactor(e.cfg.BackendName,
		timeseries.ExtentList{window.Output}.TimestampCount(req.Plan.Step),
		e.cfg.RetentionPoints)
	requested := window.Output

	cached, found := e.deltas.retrieve(req.Key)
	if found && (cached.Payload == nil || cached.Payload.DS == nil) {
		e.deltas.remove(req.Key, "invalid_cached_rows")
		cached, found = nil, false
	}
	cacheStatus := cachestatus.LookupStatusKeyMiss
	var covered timeseries.ExtentList
	if found {
		covered = e.gridExtents(cached.Extents, req.Plan)
		cacheStatus = cachestatus.LookupStatusPartialHit
	}
	missing := e.gridExtents(covered.CalculateDeltas(window.Cacheable, req.Plan.Step), req.Plan)
	if len(missing) == 0 && found {
		return Outcome[R]{Delta: cached.Payload.view(requested)}, cachestatus.LookupStatusHit, nil
	}
	if found && len(missing) == 1 && missing[0].Start.Equal(window.Cacheable[0].Start) &&
		missing[0].End.Equal(window.Cacheable[0].End) {
		cacheStatus = cachestatus.LookupStatusRangeMiss
	}

	fetchExtents := missing
	if req.Ops.Shard != nil {
		fetchExtents = req.Ops.Shard(missing)
	}
	parts := make([]*Delta, 0, len(fetchExtents)+1)
	if found {
		parts = append(parts, cached.Payload)
	}
	var mergeErr error
	for _, extent := range fetchExtents {
		statement, renderErr := req.Plan.RenderExtent(extent)
		if renderErr != nil {
			e.observeRewriteFailure("render_extent")
			return e.original(req)
		}
		part, fetchErr := req.Ops.Fetch(statement)
		if fetchErr != nil {
			if errors.Is(fetchErr, ErrUnmergeable) {
				mergeErr = fetchErr
				break
			}
			return none, cachestatus.LookupStatusProxyError, fetchErr
		}
		parts = append(parts, part)
	}
	var merged *Delta
	if mergeErr == nil {
		merged, mergeErr = mergeDeltas(req.Plan, req.Ops.SameHeader, parts)
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
			e.deltas.remove(req.Key, "unmergeable_delta_result")
		}
		// Record the failure against the plan so later requests skip the
		// delta fetch entirely instead of repeating it and discarding the
		// result.
		e.Store(req.FallbackKey, &Entry[R]{Marker: true})
		return e.objectFallback(req)
	}
	allExtents := covered.Merge(missing, req.Plan.Step)
	retained, cacheExtents := e.retain(req.Plan, merged, allExtents, time.Now())
	e.deltas.store(req.Key, &Entry[*Delta]{Payload: retained, Extents: cacheExtents}, e.cfg.CacheTTL)
	return Outcome[R]{Delta: merged.view(requested)}, cacheStatus, nil
}

func (e *Engine[R]) objectFallback(req DeltaRequest[R]) (Outcome[R], cachestatus.LookupStatus, error) {
	payload, lookup, err := req.Ops.ObjectFallback()
	return Outcome[R]{Object: payload}, lookup, err
}

func (e *Engine[R]) original(req DeltaRequest[R]) (Outcome[R], cachestatus.LookupStatus, error) {
	payload, err := req.Ops.FetchOriginal()
	return Outcome[R]{Object: payload}, cachestatus.LookupStatusProxyOnly, err
}

func mergeDeltas(plan *sqlanalyzer.QueryPlan, same func(a, b []byte) bool, parts []*Delta) (*Delta, error) {
	// joins the cached rows and the fetched parts, which never share a bucket; the newest header,
	// fetched last, describes them all
	if same == nil {
		same = bytes.Equal
	}
	sets := make([]*dataset.DataSet, 0, len(parts))
	var header []byte
	for i, part := range parts {
		if part == nil || part.DS == nil {
			return nil, Unmergeable(errors.New("missing native delta rows"))
		}
		if i > 0 && !same(header, part.Header) {
			return nil, Unmergeable(errors.New("native delta result columns changed between fetches"))
		}
		header = part.Header
		sets = append(sets, part.DS)
	}
	trq := &timeseries.TimeRangeQuery{Step: plan.Step, Phase: plan.Phase}
	return &Delta{Header: header, DS: dataset.MergeDisjoint(trq, sets...)}, nil
}

func (e *Engine[R]) retain(plan *sqlanalyzer.QueryPlan, merged *Delta, all timeseries.ExtentList,
	now time.Time,
) (*Delta, timeseries.ExtentList) {
	// retention and the volatile window bound what is stored, never the response; stored rows are
	// cropped to their stable extents, so a later merge never meets a bucket they hold
	rows, extents := merged.DS, all
	if kept, oldest, trimmed := rows.RetainNewest(e.cfg.RetentionPoints); trimmed && len(all) > 0 {
		rows, extents = kept, all.Crop(timeseries.Extent{Start: oldest, End: all[len(all)-1].End})
	}
	window := VolatileWindow(e.cfg.VolatileWindow, e.cfg.VolatileWindowPoints, plan.Step,
		plan.Directives.VolatileWindow)
	stable := StableExtents(extents, plan.Step, plan.Phase, window, now)
	if len(stable) == 0 {
		// an entry with no coverage keeps the header but no rows
		rows = rows.View(timeseries.Extent{Start: time.Unix(0, math.MaxInt64), End: time.Unix(0, math.MaxInt64)})
	} else {
		rows = rows.View(timeseries.Extent{Start: stable[0].Start, End: stable[len(stable)-1].End})
	}
	return &Delta{Header: merged.Header, DS: rows}, stable
}

func (d *Delta) view(e timeseries.Extent) *Delta {
	return &Delta{Header: d.Header, DS: d.DS.View(e)}
}

// Remove deletes the object-tier entry stored at key.
func (e *Engine[R]) Remove(key string) {
	e.objects.remove(key, explicitRemoval)
}

// RemoveDelta deletes the delta-tier entry stored at key, such as one whose rows the protocol
// could not render.
func (e *Engine[R]) RemoveDelta(key string) {
	e.deltas.remove(key, explicitRemoval)
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
