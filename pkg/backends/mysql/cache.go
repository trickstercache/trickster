/*
 * Copyright 2026 The Trickster Authors
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

package mysql

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	cacheproviders "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	checksum "github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/client_golang/prometheus"
	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/collations"
	"vitess.io/vitess/go/mysql/collations/colldata"
	"vitess.io/vitess/go/mysql/decimal"
	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
	"vitess.io/vitess/go/vt/sqlparser"
)

const (
	cacheEnvelopeVersion          byte = 1
	cacheIdentityVersion          byte = 1
	mysqlDialect                       = "mysql"
	cacheModeOPC                       = "opc"
	cacheModeDPC                       = "dpc"
	cacheModeDPCEmpty                  = "dpc-empty"
	cacheModeDPCFallback               = "dpc-fallback"
	cacheFailureDecode                 = "decode_failure"
	cacheFailureOversized              = "oversized_cached_object"
	logKeyBackendName                  = "backend_name"
	logKeyDetail                       = "detail"
	metricMethodQuery                  = "QUERY"
	metricPathQuery                    = "query"
	metricHTTPStatusOK                 = "200"
	metricHTTPStatusInternalError      = "500"
)

var cacheEnvelopeMagic = [4]byte{'T', 'M', 'Y', 'Q'}

type analysisMetricKey struct {
	mode   sqlanalyzer.CacheMode
	reason string
}

var analysisMetricKeys = [...]analysisMetricKey{
	{sqlanalyzer.CacheModeNone, string(sqlanalyzer.ReasonNondeterministic)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonInvalidSQL)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsupportedStatement)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonNotTimeRange)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsupportedBucket)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsafePredicate)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonAmbiguousTimeAxis)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsupportedGrouping)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsupportedFormat)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsupportedLimit)},
	{sqlanalyzer.CacheModeDelta, string(sqlanalyzer.ReasonDeltaCacheable)},
}

type cacheMetricKey struct {
	mode   sqlanalyzer.CacheMode
	status cachestatus.LookupStatus
}

type cacheMetricHandles struct {
	native   prometheus.Counter
	requests prometheus.Counter
	elements prometheus.Counter
	duration prometheus.Observer
}

type protocolMetricHandles struct {
	connectLatency prometheus.Observer
	queryLatency   prometheus.Observer
	analysis       map[analysisMetricKey]prometheus.Counter
	cache          map[cacheMetricKey]cacheMetricHandles
}

type cachedQueryResult struct {
	result  *sqltypes.Result
	extents timeseries.ExtentList
	size    int
}

func (c *cachedQueryResult) Size() int {
	if c == nil {
		return 0
	}
	return c.size
}

// estimateCachedQueryResultSize approximates the heap retained by a typed
// memory-cache entry. Slice capacities are intentional: the cache retains the
// backing arrays, not just their populated elements.
func estimateCachedQueryResultSize(cached *cachedQueryResult) int {
	if cached == nil {
		return 0
	}
	size := uint64(unsafe.Sizeof(*cached))
	size += uint64(cap(cached.extents)) * uint64(unsafe.Sizeof(timeseries.Extent{}))
	result := cached.result
	if result == nil {
		return saturatedSize(size)
	}
	size += uint64(unsafe.Sizeof(*result))
	size += uint64(cap(result.Fields)) * uint64(unsafe.Sizeof((*querypb.Field)(nil)))
	for _, field := range result.Fields {
		if field == nil {
			continue
		}
		size += uint64(unsafe.Sizeof(*field))
		size += uint64(len(field.Name) + len(field.Table) + len(field.OrgTable) +
			len(field.Database) + len(field.OrgName) + len(field.ColumnType))
	}
	size += uint64(cap(result.Rows)) * uint64(unsafe.Sizeof(sqltypes.Row(nil)))
	for _, row := range result.Rows {
		size += uint64(cap(row)) * uint64(unsafe.Sizeof(sqltypes.Value{}))
		for _, value := range row {
			// #nosec G115 -- Value.Len reports the nonnegative length of its byte slice.
			size += uint64(value.Len())
		}
	}
	size += uint64(len(result.SessionStateChanges) + len(result.Info))
	return saturatedSize(size)
}

func saturatedSize(size uint64) int {
	if size > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(size)
}

type cacheExecution struct {
	result *sqltypes.Result
	status cachestatus.LookupStatus
}

type deltaRequestWindow struct {
	output    timeseries.Extent
	cacheable timeseries.ExtentList
	lower     time.Time
	upper     time.Time
	empty     bool
}

type normalizedTimeRangeRenderer interface {
	renderTimeRange(lower, upper time.Time) (string, error)
}

func (h *protocolHandler) cacheEligible(session *upstreamSession) bool {
	if h.config.ProxyOnly || h.cacheClient() == nil || session == nil {
		return false
	}
	session.mtx.Lock()
	eligible := !session.inTx && !session.cacheUnsafe
	session.mtx.Unlock()
	return eligible
}

func (h *protocolHandler) cacheClient() cache.Cache {
	if h.config.CacheProvider != nil {
		return h.config.CacheProvider.Cache()
	}
	return h.config.Cache
}

func (h *protocolHandler) executeCached(c *vtmysql.Conn, session *upstreamSession,
	query string, analysis sqlanalyzer.Analysis,
) (*sqltypes.Result, cachestatus.LookupStatus, error) {
	switch analysis.Mode {
	case sqlanalyzer.CacheModeDelta:
		if analysis.Plan != nil {
			return h.executeDelta(c, session, query, analysis.Plan)
		}
	case sqlanalyzer.CacheModeObject:
		return h.executeObject(c, session, query)
	}
	return nil, cachestatus.LookupStatusProxyOnly, errors.New("uncacheable MySQL query")
}

func (h *protocolHandler) executeObject(c *vtmysql.Conn, session *upstreamSession,
	query string,
) (*sqltypes.Result, cachestatus.LookupStatus, error) {
	key := h.queryCacheKey(c, session, cacheModeOPC, strings.TrimSpace(query))
	value, err, _ := h.opcGroup.Do(key, func() (any, error) {
		if cached, ok := h.retrieveCached(key); ok {
			return cacheExecution{
				result: cached.result, status: cachestatus.LookupStatusHit,
			}, nil
		}
		result, fetchErr := h.executeOrigin(session, query)
		if fetchErr != nil {
			return cacheExecution{}, fetchErr
		}
		h.storeCached(key, &cachedQueryResult{result: result})
		return cacheExecution{
			result: result, status: cachestatus.LookupStatusKeyMiss,
		}, nil
	})
	if err != nil {
		return nil, cachestatus.LookupStatusProxyError, err
	}
	execution := value.(cacheExecution)
	return execution.result, execution.status, nil
}

func (h *protocolHandler) executeDelta(c *vtmysql.Conn, session *upstreamSession,
	query string, plan *sqlanalyzer.QueryPlan,
) (*sqltypes.Result, cachestatus.LookupStatus, error) {
	key := h.queryCacheKey(c, session, cacheModeDPC, plan.CanonicalSQL, plan.IdentitySuffix)
	lock := h.lockDPC(key)
	defer h.unlockDPC(key, lock)

	// A previous execution of this plan may have proven that its results cannot
	// be delta-merged — an unorderable group column, for instance. The marker is
	// keyed on the plan rather than on the literal statement, because the
	// statement's time bounds move with every request.
	fallbackKey := h.queryCacheKey(c, session, cacheModeDPCFallback,
		plan.CanonicalSQL, plan.IdentitySuffix)
	if _, blocked := h.retrieveCached(fallbackKey); blocked {
		return h.executeObject(c, session, query)
	}

	window, windowErr := buildDeltaRequestWindow(plan)
	if windowErr != nil {
		result, fetchErr := h.executeOrigin(session, query)
		return result, cachestatus.LookupStatusProxyOnly, fetchErr
	}
	if window.empty {
		return h.executeEmptyDelta(c, session, query, plan, window)
	}
	requested := window.output

	cached, found := h.retrieveCached(key)
	cacheStatus := cachestatus.LookupStatusKeyMiss
	var covered timeseries.ExtentList
	if found {
		covered = cached.extents
		cacheStatus = cachestatus.LookupStatusPartialHit
	}
	var missing timeseries.ExtentList
	if len(window.cacheable) > 0 {
		missing = covered.CalculateDeltas(window.cacheable, plan.Step)
	}
	if len(missing) == 0 && found {
		cropped, cropErr := h.cropAndSortResult(cached.result, plan, requested)
		if cropErr == nil {
			return cropped, cachestatus.LookupStatusHit, nil
		}
		h.removeCached(key, "invalid_cached_time_axis")
		cached, found, covered = nil, false, nil
		cacheStatus = cachestatus.LookupStatusKeyMiss
		missing = window.cacheable
	}
	if found && (len(window.cacheable) == 0 ||
		(len(missing) == 1 && missing[0].Start.Equal(window.cacheable[0].Start) &&
			missing[0].End.Equal(window.cacheable[0].End))) {
		cacheStatus = cachestatus.LookupStatusRangeMiss
	}

	fetchExtents := missing
	if h.config.DoesShard {
		fetchExtents = make(timeseries.ExtentList, 0, len(missing))
		for _, extent := range missing {
			fetchExtents = append(fetchExtents, timeseries.ExtentList{extent}.Splice(plan.Step,
				h.config.ShardMaxRange, h.config.ShardStep, h.config.ShardMaxPoints)...)
		}
	}
	parts := make([]*sqltypes.Result, 0, len(fetchExtents)+1)
	if found && cached.result != nil {
		parts = append(parts, cached.result)
	}
	for _, extent := range fetchExtents {
		originQuery, renderErr := plan.RenderExtent(extent)
		if renderErr != nil {
			h.observeRewriteFailure("render_extent")
			result, fetchErr := h.executeOrigin(session, query)
			return result, cachestatus.LookupStatusProxyOnly, fetchErr
		}
		result, fetchErr := h.executeOrigin(session, originQuery)
		if fetchErr != nil {
			return nil, cachestatus.LookupStatusProxyError, fetchErr
		}
		parts = append(parts, result)
	}
	merged, mergeErr := h.mergeResults(parts, plan)
	if mergeErr != nil {
		logger.Warn("mysql delta result could not be modeled; using object cache", logging.Pairs{
			logKeyBackendName: h.config.BackendName, logKeyDetail: mergeErr.Error(),
		})
		// Drop the delta entry: a stale part is one of the things that can make
		// a merge fail, and the retry after the marker expires should start
		// from a clean slate.
		if found {
			h.removeCached(key, "unmergeable_delta_result")
		}
		// Record the failure against the plan so later requests skip the delta
		// fetch entirely instead of repeating it and discarding the result.
		h.storeCached(fallbackKey, &cachedQueryResult{result: &sqltypes.Result{}})
		return h.executeObject(c, session, query)
	}
	allExtents := covered.Merge(missing, plan.Step)
	response, retained, cacheExtents, finalizeErr := h.finalizeDeltaResult(
		merged, allExtents, plan, requested, time.Now())
	if finalizeErr != nil {
		return nil, cachestatus.LookupStatusProxyError, finalizeErr
	}
	h.storeCached(key, &cachedQueryResult{
		result: retained, extents: cacheExtents,
	})
	return response, cacheStatus, nil
}

// finalizeDeltaResult keeps response shaping independent from cache retention.
// Retention bounds only the stored cache object; it must never discard points
// from the current client request after those points were fetched successfully.
func (h *protocolHandler) finalizeDeltaResult(merged *sqltypes.Result,
	allExtents timeseries.ExtentList, plan *sqlanalyzer.QueryPlan,
	requested timeseries.Extent, now time.Time,
) (*sqltypes.Result, *sqltypes.Result, timeseries.ExtentList, error) {
	if merged == nil {
		return nil, nil, nil, errors.New("nil MySQL delta result")
	}
	timeIndex, _, err := resultIndexes(merged.Fields, plan)
	if err != nil {
		return nil, nil, nil, err
	}
	response, err := cropSortedResult(merged, timeIndex, plan.OutputUnit, requested)
	if err != nil {
		return nil, nil, nil, err
	}
	retained, retainedExtents, err := h.applyRetentionSorted(
		merged, allExtents, plan, timeIndex)
	if err != nil {
		return nil, nil, nil, err
	}
	cacheExtents := h.stableExtents(retainedExtents, plan, now)
	return response, retained, cacheExtents, nil
}

func (h *protocolHandler) executeEmptyDelta(c *vtmysql.Conn, session *upstreamSession,
	originalQuery string, plan *sqlanalyzer.QueryPlan, window deltaRequestWindow,
) (*sqltypes.Result, cachestatus.LookupStatus, error) {
	renderer, ok := plan.Renderer.(normalizedTimeRangeRenderer)
	if !ok {
		h.observeRewriteFailure("render_empty_extent")
		result, err := h.executeOrigin(session, originalQuery)
		return result, cachestatus.LookupStatusProxyOnly, err
	}
	query, err := renderer.renderTimeRange(window.lower, window.upper)
	if err != nil {
		h.observeRewriteFailure("render_empty_extent")
		result, fetchErr := h.executeOrigin(session, originalQuery)
		return result, cachestatus.LookupStatusProxyOnly, fetchErr
	}
	key := h.queryCacheKey(c, session, cacheModeDPCEmpty, plan.CanonicalSQL, plan.IdentitySuffix)
	if cached, found := h.retrieveCached(key); found {
		return cached.result, cachestatus.LookupStatusHit, nil
	}
	result, err := h.executeOrigin(session, query)
	if err != nil {
		return nil, cachestatus.LookupStatusProxyError, err
	}
	h.storeCached(key, &cachedQueryResult{result: result})
	return result, cachestatus.LookupStatusKeyMiss, nil
}

func buildDeltaRequestWindow(plan *sqlanalyzer.QueryPlan) (deltaRequestWindow, error) {
	if plan == nil || plan.LowerBound == nil || plan.UpperBound == nil || plan.Step <= 0 ||
		!plan.LowerBound.Inclusive || plan.UpperBound.Inclusive ||
		plan.UpperBound.Value.Before(plan.LowerBound.Value) {
		return deltaRequestWindow{}, errors.New("unsupported MySQL delta request bounds")
	}
	rawLower, rawUpper := plan.LowerBound.Value, plan.UpperBound.Value
	lower := sqlanalyzer.CeilBucket(rawLower, plan.Step, plan.Phase)
	upper := sqlanalyzer.FloorBucket(rawUpper, plan.Step, plan.Phase)
	if rawUpper.Sub(rawLower) < plan.Step || lower.After(upper) {
		upper = lower
	}
	window := deltaRequestWindow{lower: lower, upper: upper}
	if lower.Equal(upper) {
		window.output = timeseries.Extent{Start: lower, End: lower}
		window.empty = true
		return window, nil
	}
	requested := timeseries.Extent{Start: lower, End: upper.Add(-plan.Step)}
	window.output = requested
	window.cacheable = timeseries.ExtentList{requested}
	return window, nil
}

func (h *protocolHandler) executeOrigin(session *upstreamSession,
	query string,
) (*sqltypes.Result, error) {
	if err := h.connectSession(session); err != nil {
		return nil, err
	}
	session.mtx.Lock()
	upstream := session.conn
	session.mtx.Unlock()
	var result *sqltypes.Result
	err := h.runOriginQuery(session, upstream, parsedQuery{statementType: sqlparser.StmtSelect},
		func() error {
			var fetchErr error
			result, fetchErr = h.collectOriginResult(session, upstream, query)
			return fetchErr
		})
	return result, err
}

func (h *protocolHandler) collectOriginResult(session *upstreamSession, upstream *vtmysql.Conn,
	query string,
) (*sqltypes.Result, error) {
	if err := upstream.ExecuteStreamFetch(query); err != nil {
		// The origin rejected the statement before opening a result stream, so
		// it answered with a complete ERR packet and stayed synchronized.
		return nil, err
	}
	// Every failure from here on abandons a stream the origin is still
	// sending, which leaves the connection desynchronized.
	result, err := h.collectStreamedResult(session, upstream)
	if err != nil {
		// The stream is abandoned partway through, and CloseResult would
		// keep reading until the origin sends a remainder it may never send.
		// The connection is already unusable, so close it instead of draining.
		upstream.Close()
		return nil, upstreamFatal{err: err}
	}
	upstream.CloseResult()
	return result, nil
}

func (h *protocolHandler) collectStreamedResult(session *upstreamSession,
	upstream *vtmysql.Conn,
) (*sqltypes.Result, error) {
	fields, err := upstream.Fields()
	if err != nil {
		return nil, err
	}
	size, overflow := resultFieldsSize(fields, h.config.MaxResultSizeBytes)
	if overflow {
		return nil, h.resultLimitExceeded(session)
	}
	result := &sqltypes.Result{Fields: fields, Rows: make([][]sqltypes.Value, 0,
		min(h.config.MaxResultRows, resultBatchSize))}
	for {
		row, fetchErr := upstream.FetchNext(nil)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if row == nil {
			statusFlags, _, stateErr := originProtocolState(upstream)
			if stateErr != nil {
				return nil, stateErr
			}
			result.StatusFlags = statusFlags
			return result, nil
		}
		if len(result.Rows) >= h.config.MaxResultRows {
			return nil, h.resultLimitExceeded(session)
		}
		size, overflow = addRowSize(size, row, h.config.MaxResultSizeBytes)
		if overflow {
			return nil, h.resultLimitExceeded(session)
		}
		result.Rows = append(result.Rows, row)
	}
}

func (h *protocolHandler) retrieveCached(key string) (*cachedQueryResult, bool) {
	cacheClient := h.cacheClient()
	if cacheClient == nil {
		return nil, false
	}
	if memoryCache, ok := memoryCacheClient(cacheClient); ok {
		value, _, err := memoryCache.RetrieveReference(key)
		if err != nil {
			if !errors.Is(err, cache.ErrKNF) {
				h.observeCacheFailure("retrieve_failure")
				logger.Error("mysql cache retrieval failed", logging.Pairs{
					logKeyBackendName: h.config.BackendName, logKeyDetail: err.Error(),
				})
			}
			return nil, false
		}
		result, valid := value.(*cachedQueryResult)
		if !valid || result == nil || result.result == nil {
			h.observeCacheFailure(cacheFailureDecode)
			h.removeCached(key, cacheFailureDecode)
			return nil, false
		}
		if h.config.MaxObjectSize > 0 && int64(result.Size()) > h.config.MaxObjectSize {
			h.observeCacheFailure(cacheFailureOversized)
			h.removeCached(key, cacheFailureOversized)
			return nil, false
		}
		return result, true
	}
	data, _, err := cacheClient.Retrieve(key)
	if err != nil {
		if !errors.Is(err, cache.ErrKNF) {
			h.observeCacheFailure("retrieve_failure")
			logger.Error("mysql cache retrieval failed", logging.Pairs{
				logKeyBackendName: h.config.BackendName, logKeyDetail: err.Error(),
			})
		}
		return nil, false
	}
	if h.config.MaxObjectSize > 0 && int64(len(data)) > h.config.MaxObjectSize {
		h.observeCacheFailure(cacheFailureOversized)
		h.removeCached(key, cacheFailureOversized)
		return nil, false
	}
	result, err := unmarshalCachedQueryResult(data)
	if err != nil {
		h.observeCacheFailure(cacheFailureDecode)
		h.removeCached(key, cacheFailureDecode)
		return nil, false
	}
	return result, true
}

func (h *protocolHandler) storeCached(key string, result *cachedQueryResult) {
	if result == nil || result.result == nil {
		h.observeCacheFailure("encode_failure")
		logger.Error("mysql cache result encoding failed", logging.Pairs{
			logKeyBackendName: h.config.BackendName, logKeyDetail: "nil MySQL cache result",
		})
		return
	}
	cacheClient := h.cacheClient()
	if cacheClient == nil {
		return
	}
	if memoryCache, ok := memoryCacheClient(cacheClient); ok {
		result.size = estimateCachedQueryResultSize(result)
		if h.config.MaxObjectSize > 0 && int64(result.size) > h.config.MaxObjectSize {
			h.observeCacheFailure("max_object_size")
			if logger.Level() == level.Debug {
				logger.Debug("mysql cache result exceeds max object size", logging.Pairs{
					logKeyBackendName: h.config.BackendName, "size": result.size,
				})
			}
			return
		}
		if err := memoryCache.StoreReference(key, result, h.config.CacheTTL); err != nil {
			h.observeCacheFailure("store_failure")
			logger.Error("mysql cache storage failed", logging.Pairs{
				logKeyBackendName: h.config.BackendName, logKeyDetail: err.Error(),
			})
		}
		return
	}
	data, err := marshalCachedQueryResult(result)
	if err != nil {
		h.observeCacheFailure("encode_failure")
		logger.Error("mysql cache result encoding failed", logging.Pairs{
			logKeyBackendName: h.config.BackendName, logKeyDetail: err.Error(),
		})
		return
	}
	if h.config.MaxObjectSize > 0 && int64(len(data)) > h.config.MaxObjectSize {
		h.observeCacheFailure("max_object_size")
		if logger.Level() == level.Debug {
			logger.Debug("mysql cache result exceeds max object size", logging.Pairs{
				logKeyBackendName: h.config.BackendName, "size": len(data),
			})
		}
		return
	}
	result.size = len(data)
	if err := cacheClient.Store(key, data, h.config.CacheTTL); err != nil {
		h.observeCacheFailure("store_failure")
		logger.Error("mysql cache storage failed", logging.Pairs{
			logKeyBackendName: h.config.BackendName, logKeyDetail: err.Error(),
		})
	}
}

func memoryCacheClient(cacheClient cache.Cache) (cache.MemoryCache, bool) {
	if cacheClient == nil || cacheClient.Configuration() == nil ||
		cacheClient.Configuration().Provider != cacheproviders.Memory {
		return nil, false
	}
	if capability, ok := cacheClient.(interface{ SupportsReferences() bool }); ok &&
		!capability.SupportsReferences() {
		return nil, false
	}
	memoryCache, ok := cacheClient.(cache.MemoryCache)
	return memoryCache, ok
}

func (h *protocolHandler) removeCached(key, reason string) {
	cacheClient := h.cacheClient()
	if cacheClient == nil {
		return
	}
	if err := cacheClient.Remove(key); err != nil {
		h.observeCacheFailure("remove_failure")
		logger.Error("mysql cache removal failed", logging.Pairs{
			logKeyBackendName: h.config.BackendName, "reason": reason, logKeyDetail: err.Error(),
		})
	}
}

func (h *protocolHandler) observeCacheFailure(reason string) {
	cacheClient := h.cacheClient()
	if cacheClient == nil || cacheClient.Configuration() == nil {
		return
	}
	configuration := cacheClient.Configuration()
	metrics.CacheEvents.WithLabelValues(configuration.Name, configuration.Provider,
		"error", "mysql_"+reason).Inc()
}

func (h *protocolHandler) queryCacheKey(c *vtmysql.Conn, session *upstreamSession,
	engine string, statementIdentity ...string,
) string {
	session.mtx.Lock()
	database := session.database
	timeZone := session.timeZone
	collation := session.collation
	session.mtx.Unlock()
	var identity strings.Builder
	identitySize := 1 + len(h.config.BackendName) + len(h.config.CacheKeyPrefix) +
		len(c.User) + len(database) + len(timeZone) + len(engine) +
		(7+len(statementIdentity))*binary.MaxVarintLen64
	for _, part := range statementIdentity {
		identitySize += len(part)
	}
	identity.Grow(identitySize)
	identity.WriteByte(cacheIdentityVersion)
	appendCacheIdentityField(&identity, h.config.BackendName)
	appendCacheIdentityField(&identity, h.config.CacheKeyPrefix)
	appendCacheIdentityField(&identity, c.User)
	appendCacheIdentityField(&identity, database)
	appendCacheIdentityField(&identity, timeZone)
	appendCacheIdentityUint(&identity, uint64(collation))
	appendCacheIdentityField(&identity, engine)
	for _, part := range statementIdentity {
		appendCacheIdentityField(&identity, part)
	}
	suffix := checksum.Checksum(identity.String())
	return h.config.BackendName + "." + h.config.CacheKeyPrefix + ".mysql." + engine + "." + suffix
}

func appendCacheIdentityField(identity *strings.Builder, value string) {
	var length [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(length[:], uint64(len(value)))
	_, _ = identity.Write(length[:n])
	_, _ = identity.WriteString(value)
}

func appendCacheIdentityUint(identity *strings.Builder, value uint64) {
	var encoded [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(encoded[:], value)
	_, _ = identity.Write(encoded[:n])
}

func (h *protocolHandler) lockDPC(key string) *dpcLock {
	h.dpcLockMtx.Lock()
	if h.dpcLocks == nil {
		h.dpcLocks = make(map[string]*dpcLock)
	}
	lock := h.dpcLocks[key]
	if lock == nil {
		lock = &dpcLock{}
		h.dpcLocks[key] = lock
	}
	lock.references++
	h.dpcLockMtx.Unlock()
	lock.Lock()
	return lock
}

func (h *protocolHandler) unlockDPC(key string, lock *dpcLock) {
	lock.Unlock()
	h.dpcLockMtx.Lock()
	lock.references--
	if lock.references == 0 && h.dpcLocks[key] == lock {
		delete(h.dpcLocks, key)
	}
	h.dpcLockMtx.Unlock()
}

func marshalCachedQueryResult(cached *cachedQueryResult) ([]byte, error) {
	if cached == nil || cached.result == nil {
		return nil, errors.New("nil MySQL cache result")
	}
	protoResult := sqltypes.ResultToProto3(cached.result)
	resultBytes, err := protoResult.MarshalVT()
	if err != nil {
		return nil, err
	}
	if len(cached.extents) > math.MaxUint32 || len(resultBytes) > math.MaxUint32 {
		return nil, errors.New("MySQL cache result is too large to encode")
	}
	headerSize := 4 + 1 + 2 + 2 + 4 + len(cached.extents)*16 + 4
	out := make([]byte, headerSize+len(resultBytes))
	copy(out, cacheEnvelopeMagic[:])
	out[4] = cacheEnvelopeVersion
	// Bytes 5:7 are reserved and zeroed. Vitess does not currently expose a
	// streamed result's warning count; a future version can place it there
	// without moving any other field.
	binary.BigEndian.PutUint16(out[7:9], cached.result.StatusFlags)
	// #nosec G115 -- both lengths were bounded by math.MaxUint32 above.
	binary.BigEndian.PutUint32(out[9:13], uint32(len(cached.extents)))
	position := 13
	for _, extent := range cached.extents {
		// #nosec G115 -- binary cache encoding deliberately preserves the signed
		// Unix nanosecond value's two's-complement bit pattern.
		binary.BigEndian.PutUint64(out[position:position+8], uint64(extent.Start.UnixNano()))
		// #nosec G115 -- see the corresponding signed decode below.
		binary.BigEndian.PutUint64(out[position+8:position+16], uint64(extent.End.UnixNano()))
		position += 16
	}
	// #nosec G115 -- both lengths were bounded by math.MaxUint32 above.
	binary.BigEndian.PutUint32(out[position:position+4], uint32(len(resultBytes)))
	copy(out[position+4:], resultBytes)
	return out, nil
}

func unmarshalCachedQueryResult(data []byte) (*cachedQueryResult, error) {
	if len(data) < 17 || !slices.Equal(data[:4], cacheEnvelopeMagic[:]) ||
		data[4] != cacheEnvelopeVersion {
		return nil, errors.New("invalid MySQL cache envelope")
	}
	statusFlags := binary.BigEndian.Uint16(data[7:9])
	extentCount := int(binary.BigEndian.Uint32(data[9:13]))
	if extentCount > (len(data)-17)/16 {
		return nil, errors.New("invalid MySQL cache extent count")
	}
	position := 13
	extents := make(timeseries.ExtentList, extentCount)
	for i := range extentCount {
		// #nosec G115 -- this reverses the intentional bit-preserving conversion
		// performed by marshalCachedQueryResult.
		start := int64(binary.BigEndian.Uint64(data[position : position+8]))
		// #nosec G115 -- see the corresponding signed encode above.
		end := int64(binary.BigEndian.Uint64(data[position+8 : position+16]))
		extents[i] = timeseries.Extent{Start: time.Unix(0, start), End: time.Unix(0, end)}
		position += 16
	}
	if len(data)-position < 4 {
		return nil, errors.New("truncated MySQL cache envelope")
	}
	resultSize := int(binary.BigEndian.Uint32(data[position : position+4]))
	position += 4
	if resultSize < 0 || resultSize != len(data)-position {
		return nil, errors.New("invalid MySQL cache result size")
	}
	protoResult := &querypb.QueryResult{}
	if err := protoResult.UnmarshalVT(data[position:]); err != nil {
		return nil, err
	}
	result := sqltypes.Proto3ToResult(protoResult)
	result.StatusFlags = statusFlags
	return &cachedQueryResult{result: result, extents: extents}, nil
}

func (h *protocolHandler) mergeResults(parts []*sqltypes.Result,
	plan *sqlanalyzer.QueryPlan,
) (*sqltypes.Result, error) {
	if len(parts) == 0 || parts[0] == nil {
		return nil, errors.New("empty MySQL delta result")
	}
	fields := parts[0].Fields
	timeIndex, groupIndexes, err := resultIndexes(fields, plan)
	if err != nil {
		return nil, err
	}
	type keyedRow struct {
		epoch int64
		row   []sqltypes.Value
	}
	comparator, err := h.newGroupComparator(fields, groupIndexes)
	if err != nil {
		return nil, err
	}
	rows := make([]keyedRow, 0, totalRows(parts))
	for _, part := range parts {
		if part == nil || !compatibleFields(fields, part.Fields) {
			return nil, errors.New("incompatible MySQL delta result fields")
		}
		for _, row := range part.Rows {
			if len(row) != len(fields) {
				return nil, errors.New("invalid MySQL delta result row")
			}
			if validateErr := comparator.validateRow(row); validateErr != nil {
				return nil, validateErr
			}
			epoch, parseErr := resultEpoch(row[timeIndex], plan.OutputUnit)
			if parseErr != nil {
				return nil, parseErr
			}
			rows = append(rows, keyedRow{epoch: epoch, row: row})
		}
	}
	var compareErr error
	slices.SortStableFunc(rows, func(a, b keyedRow) int {
		if a.epoch != b.epoch {
			return cmp.Compare(a.epoch, b.epoch)
		}
		if compareErr != nil {
			return 0
		}
		order, err := comparator.compare(a.row, b.row)
		if err != nil {
			compareErr = err
		}
		return order
	})
	if compareErr != nil {
		return nil, compareErr
	}
	// MySQL equality is defined by the group columns' types and collations, not
	// by their serialized bytes. Stable sorting preserves part order among
	// equal rows, so replacing the prior representative retains the latest row.
	compacted := rows[:0]
	for _, candidate := range rows {
		last := len(compacted) - 1
		if last < 0 || compacted[last].epoch != candidate.epoch {
			compacted = append(compacted, candidate)
			continue
		}
		order, err := comparator.compare(compacted[last].row, candidate.row)
		if err != nil {
			return nil, err
		}
		if order == 0 {
			compacted[last] = candidate
			continue
		}
		compacted = append(compacted, candidate)
	}
	rows = compacted
	out := cloneResultMetadata(parts[len(parts)-1])
	out.Fields = fields
	out.Rows = make([][]sqltypes.Value, len(rows))
	for i := range rows {
		out.Rows[i] = rows[i].row
	}
	return out, nil
}

func (h *protocolHandler) cropAndSortResult(result *sqltypes.Result,
	plan *sqlanalyzer.QueryPlan, extent timeseries.Extent,
) (*sqltypes.Result, error) {
	if result == nil {
		return nil, errors.New("nil MySQL delta result")
	}
	timeIndex, groupIndexes, err := resultIndexes(result.Fields, plan)
	if err != nil {
		return nil, err
	}
	comparator, err := h.newGroupComparator(result.Fields, groupIndexes)
	if err != nil {
		return nil, err
	}
	start, end := extent.Start.UnixNano(), extent.End.UnixNano()
	type timedRow struct {
		epoch int64
		row   []sqltypes.Value
	}
	rows := make([]timedRow, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) <= timeIndex {
			return nil, errors.New("invalid MySQL delta result row")
		}
		epoch, parseErr := resultEpoch(row[timeIndex], plan.OutputUnit)
		if parseErr != nil {
			return nil, parseErr
		}
		if epoch < start || epoch > end {
			continue
		}
		if validateErr := comparator.validateRow(row); validateErr != nil {
			return nil, validateErr
		}
		rows = append(rows, timedRow{epoch: epoch, row: row})
	}
	var compareErr error
	slices.SortStableFunc(rows, func(a, b timedRow) int {
		if a.epoch != b.epoch {
			return cmp.Compare(a.epoch, b.epoch)
		}
		if compareErr != nil {
			return 0
		}
		order, err := comparator.compare(a.row, b.row)
		if err != nil {
			compareErr = err
		}
		return order
	})
	if compareErr != nil {
		return nil, compareErr
	}
	out := cloneResultMetadata(result)
	out.Rows = make([][]sqltypes.Value, len(rows))
	for i := range rows {
		out.Rows[i] = rows[i].row
	}
	return out, nil
}

// cropSortedResult crops a result already ordered by (epoch, group), as
// guaranteed by mergeResults, without rebuilding group keys or sorting again.
func cropSortedResult(result *sqltypes.Result, timeIndex int,
	unit timeseries.FieldDataType, extent timeseries.Extent,
) (*sqltypes.Result, error) {
	start, err := sortedRowBoundary(result.Rows, timeIndex, unit, extent.Start.UnixNano(), false)
	if err != nil {
		return nil, err
	}
	end, err := sortedRowBoundary(result.Rows, timeIndex, unit, extent.End.UnixNano(), true)
	if err != nil {
		return nil, err
	}
	if start > end {
		return nil, errors.New("invalid MySQL delta result extent")
	}
	out := cloneResultMetadata(result)
	out.Rows = slices.Clone(result.Rows[start:end])
	return out, nil
}

func sortedRowBoundary(rows [][]sqltypes.Value, timeIndex int,
	unit timeseries.FieldDataType, target int64, after bool,
) (int, error) {
	low, high := 0, len(rows)
	for low < high {
		middle := low + (high-low)/2
		if len(rows[middle]) <= timeIndex {
			return 0, errors.New("invalid MySQL delta result row")
		}
		epoch, err := resultEpoch(rows[middle][timeIndex], unit)
		if err != nil {
			return 0, err
		}
		if epoch > target || (!after && epoch == target) {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return low, nil
}

func resultIndexes(fields []*querypb.Field,
	plan *sqlanalyzer.QueryPlan,
) (int, []int, error) {
	timeIndex := -1
	groups := make([]int, len(plan.GroupColumns))
	values := make([]int, len(plan.ValueColumns))
	for i := range groups {
		groups[i] = -1
	}
	for i := range values {
		values[i] = -1
	}
	for i, field := range fields {
		if field == nil {
			continue
		}
		if strings.EqualFold(field.Name, plan.OutputColumn) {
			if timeIndex >= 0 {
				return 0, nil, fmt.Errorf("MySQL result has duplicate time column %q", plan.OutputColumn)
			}
			timeIndex = i
		}
		for j, group := range plan.GroupColumns {
			if strings.EqualFold(field.Name, group) {
				if groups[j] >= 0 {
					return 0, nil, fmt.Errorf("MySQL result has duplicate group column %q", group)
				}
				groups[j] = i
			}
		}
		for j, value := range plan.ValueColumns {
			if strings.EqualFold(field.Name, value) {
				if values[j] >= 0 || !sqltypes.IsNumber(field.Type) {
					return 0, nil, fmt.Errorf("MySQL result value column %q is duplicate or non-numeric", value)
				}
				values[j] = i
			}
		}
	}
	if timeIndex < 0 {
		return 0, nil, fmt.Errorf("MySQL result has no time column %q", plan.OutputColumn)
	}
	for i, index := range groups {
		if index < 0 {
			return 0, nil, fmt.Errorf("MySQL result has no group column %q", plan.GroupColumns[i])
		}
	}
	for i, index := range values {
		if index < 0 {
			return 0, nil, fmt.Errorf("MySQL result has no numeric value column %q", plan.ValueColumns[i])
		}
	}
	return timeIndex, groups, nil
}

func resultEpoch(value sqltypes.Value, unit timeseries.FieldDataType) (int64, error) {
	n, err := strconv.ParseInt(value.ToString(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse MySQL time value: %w", err)
	}
	if unit == timeseries.DateTimeUnixSecs {
		return n * int64(time.Second), nil
	}
	if unit == timeseries.DateTimeUnixNano {
		return n, nil
	}
	return 0, fmt.Errorf("unsupported MySQL time unit %d", unit)
}

// groupCompareKind names the one MySQL comparison rule that orders a group
// column. Every rule is chosen from the field's declared type when the
// comparator is built, so the sort itself performs no lookups.
type groupCompareKind uint8

const (
	compareCollatedText groupCompareKind = iota
	compareBytes
	compareSigned
	compareUnsigned
	compareFloat
	compareDecimal
)

// groupColumn describes how to order one group column.
type groupColumn struct {
	index     int
	name      string
	fieldType querypb.Type
	kind      groupCompareKind
	collation colldata.Collation
}

// groupComparator orders rows by their group columns using MySQL's own rules:
// NULLs first in ascending order, numeric ordering for numbers, exact decimal
// ordering, and each text field's declared collation. Types it cannot order
// exactly are rejected outright, which costs DPC optimization rather than
// correctness because the caller falls back to the object cache.
type groupComparator struct {
	columns []groupColumn
}

func (h *protocolHandler) newGroupComparator(fields []*querypb.Field,
	indexes []int,
) (*groupComparator, error) {
	c := &groupComparator{columns: make([]groupColumn, len(indexes))}
	for i, index := range indexes {
		// resultIndexes has already proven every group index addresses a field.
		field := fields[index]
		column := groupColumn{index: index, name: field.Name, fieldType: field.Type}
		switch {
		case sqltypes.IsEnum(field.Type), sqltypes.IsSet(field.Type):
			// MySQL orders these by ordinal and bitmask, which live in the
			// column's declaration. A result header does not carry them, so
			// every value would otherwise compare equal.
			return nil, fmt.Errorf("MySQL group column %q of type %v needs declaration "+
				"values that the result metadata does not carry", field.Name, field.Type)
		case sqltypes.IsSigned(field.Type):
			column.kind = compareSigned
		case sqltypes.IsUnsigned(field.Type):
			column.kind = compareUnsigned
		case sqltypes.IsFloat(field.Type):
			column.kind = compareFloat
		case sqltypes.IsDecimal(field.Type):
			column.kind = compareDecimal
		case sqltypes.IsBinary(field.Type), sqltypes.IsDate(field.Type):
			// Binary strings order by raw bytes by definition, and MySQL renders
			// DATE, DATETIME, and TIMESTAMP zero-padded at a fixed width. TIME is
			// deliberately absent: it can be negative, which byte order gets wrong.
			column.kind = compareBytes
		case sqltypes.IsText(field.Type):
			if field.Charset > math.MaxUint16 {
				return nil, fmt.Errorf("MySQL group column %q uses collation %d, "+
					"which Trickster cannot order", field.Name, field.Charset)
			}
			collationID := collations.ID(field.Charset) //nolint:gosec // range checked above
			if collationID == collations.Unknown {
				collationID = h.collationEnv().DefaultConnectionCharset()
			}
			if collationID == collations.CollationBinaryID {
				column.kind = compareBytes
				break
			}
			column.collation = colldata.Lookup(collationID)
			if column.collation == nil {
				return nil, fmt.Errorf("MySQL group column %q uses collation %d, "+
					"which Trickster cannot order", field.Name, collationID)
			}
			column.kind = compareCollatedText
		default:
			return nil, fmt.Errorf("MySQL group column %q has type %v, "+
				"which Trickster cannot order", field.Name, field.Type)
		}
		c.columns[i] = column
	}
	return c, nil
}

// validateRow rejects a row whose group values do not carry their field's
// declared type. Each comparison rule was chosen from the field, so a value of
// some other type would be ordered by the wrong rule. Rows are validated once
// on the way in rather than on every comparison.
func (c *groupComparator) validateRow(row []sqltypes.Value) error {
	for i := range c.columns {
		column := &c.columns[i]
		value := row[column.index]
		if value.IsNull() || value.Type() == column.fieldType {
			continue
		}
		return fmt.Errorf("MySQL group column %q holds a %v value, want %v",
			column.name, value.Type(), column.fieldType)
	}
	return nil
}

// compare orders left before right by each group column in turn. Failures
// propagate rather than falling back to a byte order that would not match what
// the origin returned.
func (c *groupComparator) compare(left, right []sqltypes.Value) (int, error) {
	for i := range c.columns {
		column := &c.columns[i]
		l, r := left[column.index], right[column.index]
		// MySQL sorts NULL before every value in ascending order.
		switch {
		case l.IsNull() && r.IsNull():
			continue
		case l.IsNull():
			return -1, nil
		case r.IsNull():
			return 1, nil
		}
		order, err := column.compareValues(l, r)
		if err != nil {
			return 0, err
		}
		if order != 0 {
			return order, nil
		}
	}
	return 0, nil
}

func (g *groupColumn) compareValues(left, right sqltypes.Value) (int, error) {
	switch g.kind {
	case compareCollatedText:
		return g.collation.Collate(left.Raw(), right.Raw(), false), nil
	case compareBytes:
		return bytes.Compare(left.Raw(), right.Raw()), nil
	case compareSigned:
		l, r, err := twoValues(g, left, right, sqltypes.Value.ToInt64)
		return cmp.Compare(l, r), err
	case compareUnsigned:
		l, r, err := twoValues(g, left, right, sqltypes.Value.ToUint64)
		return cmp.Compare(l, r), err
	case compareFloat:
		l, r, err := twoValues(g, left, right, sqltypes.Value.ToFloat64)
		return cmp.Compare(l, r), err
	case compareDecimal:
		l, err := decimal.NewFromMySQL(left.Raw())
		if err != nil {
			return 0, g.valueError(err)
		}
		r, err := decimal.NewFromMySQL(right.Raw())
		if err != nil {
			return 0, g.valueError(err)
		}
		return l.Cmp(r), nil
	}
	return 0, fmt.Errorf("MySQL group column %q has no comparison rule", g.name)
}

func twoValues[T any](g *groupColumn, left, right sqltypes.Value,
	convert func(sqltypes.Value) (T, error),
) (T, T, error) {
	l, err := convert(left)
	if err != nil {
		var zero T
		return zero, zero, g.valueError(err)
	}
	r, err := convert(right)
	if err != nil {
		var zero T
		return zero, zero, g.valueError(err)
	}
	return l, r, nil
}

func (g *groupColumn) valueError(err error) error {
	return fmt.Errorf("compare MySQL group column %q: %w", g.name, err)
}

// collationEnv returns the environment used to order text group columns. A
// handler built without a Vitess environment falls back to the protocol's own
// advertised server version.
func (h *protocolHandler) collationEnv() *collations.Environment {
	if h.env != nil {
		return h.env.CollationEnv()
	}
	return defaultCollationEnv()
}

var defaultCollationEnv = sync.OnceValue(func() *collations.Environment {
	return collations.NewEnvironment(protocolVersion)
})

func compatibleFields(left, right []*querypb.Field) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] == nil || right[i] == nil {
			if left[i] != right[i] {
				return false
			}
			continue
		}
		// Charset carries the collation that orders a text column. Parts that
		// disagree would all be ordered by the first part's collation.
		if left[i].Name != right[i].Name || left[i].Type != right[i].Type ||
			left[i].Charset != right[i].Charset {
			return false
		}
	}
	return true
}

func cloneResultMetadata(result *sqltypes.Result) *sqltypes.Result {
	return &sqltypes.Result{
		Fields: result.Fields, RowsAffected: result.RowsAffected,
		InsertID: result.InsertID, InsertIDChanged: result.InsertIDChanged,
		SessionStateChanges: result.SessionStateChanges, StatusFlags: result.StatusFlags,
		Info: result.Info,
	}
}

func totalRows(results []*sqltypes.Result) int {
	total := 0
	for _, result := range results {
		if result != nil {
			total += len(result.Rows)
		}
	}
	return total
}

func (h *protocolHandler) applyRetentionSorted(result *sqltypes.Result,
	extents timeseries.ExtentList, plan *sqlanalyzer.QueryPlan, timeIndex int,
) (*sqltypes.Result, timeseries.ExtentList, error) {
	limit := h.config.RetentionPoints
	if limit <= 0 || result == nil || len(result.Rows) <= limit || len(extents) == 0 {
		return result, extents, nil
	}
	unique := 0
	start := 0
	var cutoff, previous int64
	havePrevious := false
	for i, row := range slices.Backward(result.Rows) {
		if len(row) <= timeIndex {
			return nil, nil, errors.New("invalid MySQL delta result row")
		}
		epoch, err := resultEpoch(row[timeIndex], plan.OutputUnit)
		if err != nil {
			return nil, nil, err
		}
		if !havePrevious || epoch != previous {
			unique++
			if unique > limit {
				start = i + 1
				break
			}
			cutoff = epoch
			previous = epoch
			havePrevious = true
		}
	}
	if unique <= limit {
		return result, extents, nil
	}
	retained := cloneResultMetadata(result)
	retained.Rows = slices.Clone(result.Rows[start:])
	return retained, extents.Crop(timeseries.Extent{
		Start: time.Unix(0, cutoff), End: extents[len(extents)-1].End,
	}), nil
}

func (h *protocolHandler) stableExtents(extents timeseries.ExtentList,
	plan *sqlanalyzer.QueryPlan, now time.Time,
) timeseries.ExtentList {
	window := max(h.config.BackfillWindow, time.Duration(h.config.BackfillPoints)*plan.Step,
		plan.BackfillTolerance)
	if window <= 0 || len(extents) == 0 {
		return extents
	}
	cutoff := now.Add(-window).Truncate(plan.Step)
	if cutoff.After(extents[len(extents)-1].End) {
		return extents
	}
	if !cutoff.After(extents[0].Start) {
		return timeseries.ExtentList{}
	}
	volatile := timeseries.ExtentList{{Start: cutoff, End: extents[len(extents)-1].End}}
	return extents.Remove(volatile, plan.Step)
}

func (h *protocolHandler) updateSessionStateParsed(session *upstreamSession, parsed parsedQuery) {
	session.mtx.Lock()
	defer session.mtx.Unlock()
	if parsed.statementType == sqlparser.StmtSelect {
		switch {
		case parsed.err != nil || parsed.statement == nil:
			session.cacheUnsafe = true
			session.stateful = true
		case selectChangesSessionState(parsed.statement):
			// SELECT ... INTO, user-variable assignment, and the advisory lock
			// functions all leave connection-scoped state behind.
			session.cacheUnsafe = true
			session.stateful = true
		}
	}
	if parsed.statementType == sqlparser.StmtExplain && parsed.responseShape == responseShapeOK {
		// EXPLAIN ... INTO stores its result in a user variable instead of
		// returning rows, so the successful statement creates unreplayable state.
		session.cacheUnsafe = true
		session.stateful = true
	}
	switch parsed.statementType {
	// Rolling back to or releasing a savepoint does not end its transaction.
	case sqlparser.StmtBegin, sqlparser.StmtSavepoint,
		sqlparser.StmtSRollback, sqlparser.StmtRelease:
		session.inTx = true
	case sqlparser.StmtCommit, sqlparser.StmtRollback:
		session.inTx = false
	case sqlparser.StmtUse:
		if use, ok := parsed.statement.(*sqlparser.Use); parsed.err == nil && ok {
			session.database = use.DBName.String()
			if !session.upstreamParamsReady {
				session.upstream = h.config.Upstream
				session.upstreamParamsReady = true
			}
			session.upstream.DbName = session.database
		}
	case sqlparser.StmtSet:
		if timeZone, ok := cacheSafeTimeZone(parsed.statement); parsed.err == nil && ok {
			session.timeZone = timeZone
		} else {
			// The time zone is the only SET replaySessionState reproduces.
			session.cacheUnsafe = true
			session.stateful = true
		}
	// The rows are durable at the origin, but the write also updates
	// connection-scoped diagnostics — LAST_INSERT_ID above all — that a
	// replacement connection reports differently.
	case sqlparser.StmtInsert, sqlparser.StmtReplace,
		sqlparser.StmtUpdate, sqlparser.StmtDelete,
		// Or Table locks are connection-scoped, and the analyzer does not parse
		// DDL closely enough to rule out CREATE TEMPORARY TABLE.
		sqlparser.StmtDDL, sqlparser.StmtLockTables, sqlparser.StmtUnlockTables:
		session.cacheUnsafe = true
		session.stateful = true
	default:
		switch parsed.statementType {
		case sqlparser.StmtSelect, sqlparser.StmtShow, sqlparser.StmtExplain,
			sqlparser.StmtAnalyze, sqlparser.StmtComment, sqlparser.StmtCommentOnly:
		case sqlparser.StmtUnknown:
			// AST-less row producers admitted by the response classifier are
			// read-only TABLE, CHECK TABLE, and CHECKSUM TABLE statements.
			if parsed.responseShape != responseShapeRows {
				session.cacheUnsafe = true
				session.stateful = true
			}
		default:
			session.cacheUnsafe = true
			session.stateful = true
		}
	}
}

// updateSessionStateFailed records state a failed statement may still have left
// on the connection. MySQL applies a multi-assignment SET left to right and can
// abort partway, and DDL is not transactional, so an error is not proof that
// nothing changed. Values the origin definitely did not apply — the database,
// the time zone, and the transaction flag — are deliberately left alone.
func (h *protocolHandler) updateSessionStateFailed(session *upstreamSession, parsed parsedQuery) {
	if parsed.statementType == sqlparser.StmtUnknown && parsed.responseShape == responseShapeRows {
		return
	}
	switch parsed.statementType {
	case sqlparser.StmtSelect:
		// A failed read leaves nothing behind unless its own AST assigns a
		// variable, selects INTO, or takes an advisory lock. A read with no
		// recorded AST has nothing to inspect.
		if parsed.statement == nil || parsed.err != nil ||
			!selectChangesSessionState(parsed.statement) {
			return
		}
	case sqlparser.StmtShow, sqlparser.StmtExplain, sqlparser.StmtAnalyze,
		sqlparser.StmtComment, sqlparser.StmtCommentOnly, sqlparser.StmtUse,
		sqlparser.StmtBegin, sqlparser.StmtCommit, sqlparser.StmtRollback,
		sqlparser.StmtSavepoint, sqlparser.StmtSRollback, sqlparser.StmtRelease:
		return
	}
	// Everything else — SET, DDL, table locks, and unrecognized statements —
	// may have applied part of its work before the origin reported the error.
	session.mtx.Lock()
	session.cacheUnsafe = true
	session.stateful = true
	session.mtx.Unlock()
}

func selectChangesSessionState(stmt sqlparser.Statement) bool {
	unsafe := false
	_ = sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		switch n := node.(type) {
		case *sqlparser.Select:
			unsafe = n.Into != nil || n.SQLCalcFoundRows
		case *sqlparser.Variable, *sqlparser.AssignmentExpr:
			unsafe = true
		case *sqlparser.LockingFunc:
			unsafe = true
		case *sqlparser.FuncExpr:
			switch strings.ToLower(n.Name.String()) {
			case "get_lock", "release_all_locks", "release_lock":
				unsafe = true
			}
		}
		return !unsafe, nil
	}, stmt)
	return unsafe
}

func cacheSafeTimeZone(stmt sqlparser.Statement) (string, bool) {
	set, ok := stmt.(*sqlparser.Set)
	if !ok || len(set.Exprs) != 1 || set.Exprs[0].Var == nil {
		return "", false
	}
	expr := set.Exprs[0]
	if !expr.Var.Name.EqualString("time_zone") ||
		(expr.Var.Scope != sqlparser.NoScope && expr.Var.Scope != sqlparser.SessionScope) {
		return "", false
	}
	literal, ok := expr.Expr.(*sqlparser.Literal)
	if !ok || literal.Type != sqlparser.StrVal || literal.Val == "" {
		return "", false
	}
	return literal.Val, true
}

func (h *protocolHandler) observeAnalysis(statementType sqlparser.StatementType,
	analysis sqlanalyzer.Analysis,
) {
	reason := string(analysis.Reason)
	if reason == "" {
		reason = "unknown"
	}
	key := analysisMetricKey{mode: analysis.Mode, reason: reason}
	if h.metricHandles != nil {
		if counter := h.metricHandles.analysis[key]; counter != nil {
			counter.Inc()
		} else {
			metrics.SQLQueryAnalysis.WithLabelValues(h.config.BackendName, mysqlDialect,
				analysis.Mode.String(), reason).Inc()
		}
	} else {
		metrics.SQLQueryAnalysis.WithLabelValues(h.config.BackendName, mysqlDialect,
			analysis.Mode.String(), reason).Inc()
	}
	if logger.Level() == level.Debug {
		logger.Debug("mysql query analyzed", logging.Pairs{
			logKeyBackendName: h.config.BackendName, "cache_mode": analysis.Mode.String(),
			"analysis_reason": reason, "statement_type": statementType.String(),
		})
	}
}

func (h *protocolHandler) observeRewriteFailure(reason string) {
	metrics.SQLQueryRewriteFailures.WithLabelValues(h.config.BackendName, mysqlDialect, reason).Inc()
	logger.Error("mysql query extent rewrite failed", logging.Pairs{
		logKeyBackendName: h.config.BackendName, "reason": reason,
	})
}

func (h *protocolHandler) observeCache(mode sqlanalyzer.CacheMode,
	status cachestatus.LookupStatus, points int, elapsed time.Duration,
) {
	handles, ok := cacheMetricHandles{}, false
	if h.metricHandles != nil {
		handles, ok = h.metricHandles.cache[cacheMetricKey{mode: mode, status: status}]
	}
	if !ok {
		handles = resolveCacheMetricHandles(h.config.BackendName, mode, status)
	}
	handles.native.Inc()
	handles.requests.Inc()
	handles.elements.Add(float64(points))
	handles.duration.Observe(elapsed.Seconds())
	if logger.Level() == level.Debug {
		logger.Debug("mysql query cache completed", logging.Pairs{
			logKeyBackendName: h.config.BackendName, "cache_mode": mode.String(),
			"cache_status": status.String(),
		})
	}
}

func newProtocolMetricHandles(backendName string) *protocolMetricHandles {
	handles := &protocolMetricHandles{
		connectLatency: metrics.MySQLCommandLatency.WithLabelValues(backendName, "connect"),
		queryLatency:   metrics.MySQLCommandLatency.WithLabelValues(backendName, metricPathQuery),
		analysis:       make(map[analysisMetricKey]prometheus.Counter, len(analysisMetricKeys)),
		cache:          make(map[cacheMetricKey]cacheMetricHandles, 10),
	}
	for _, key := range analysisMetricKeys {
		handles.analysis[key] = metrics.SQLQueryAnalysis.WithLabelValues(backendName, mysqlDialect,
			key.mode.String(), key.reason)
	}
	statuses := map[sqlanalyzer.CacheMode][]cachestatus.LookupStatus{
		sqlanalyzer.CacheModeObject: {
			cachestatus.LookupStatusHit,
			cachestatus.LookupStatusKeyMiss,
			cachestatus.LookupStatusProxyError,
			cachestatus.LookupStatusProxyOnly,
		},
		sqlanalyzer.CacheModeDelta: {
			cachestatus.LookupStatusHit,
			cachestatus.LookupStatusPartialHit,
			cachestatus.LookupStatusRangeMiss,
			cachestatus.LookupStatusKeyMiss,
			cachestatus.LookupStatusProxyError,
			cachestatus.LookupStatusProxyOnly,
		},
	}
	for mode, values := range statuses {
		for _, status := range values {
			key := cacheMetricKey{mode: mode, status: status}
			handles.cache[key] = resolveCacheMetricHandles(backendName, mode, status)
		}
	}
	return handles
}

func resolveCacheMetricHandles(backendName string, mode sqlanalyzer.CacheMode,
	status cachestatus.LookupStatus,
) cacheMetricHandles {
	httpStatus := metricHTTPStatusOK
	if status == cachestatus.LookupStatusProxyError || status == cachestatus.LookupStatusError {
		httpStatus = metricHTTPStatusInternalError
	}
	statusLabel := status.String()
	return cacheMetricHandles{
		native: metrics.SQLQueryCache.WithLabelValues(backendName, mysqlDialect,
			mode.String(), statusLabel),
		requests: metrics.ProxyRequestStatus.WithLabelValues(backendName, mysqlDialect,
			metricMethodQuery, statusLabel, httpStatus, metricPathQuery),
		elements: metrics.ProxyRequestElements.WithLabelValues(backendName, mysqlDialect,
			statusLabel, metricPathQuery),
		duration: metrics.ProxyRequestDuration.WithLabelValues(backendName, mysqlDialect,
			metricMethodQuery, statusLabel, httpStatus, metricPathQuery),
	}
}
