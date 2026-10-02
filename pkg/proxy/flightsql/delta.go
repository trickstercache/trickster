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

package flightsql

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	checksum "github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	dsarrow "github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/arrow"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// DeltaConfig enables the delta-proxy-cache tier for statement queries. When
// configured, statements the analyzer classifies as delta-cacheable — and
// whose Arrow schemas the dataset model can represent — are cached by extent,
// with only missing sub-ranges fetched from the upstream; everything else
// falls open to the verbatim-IPC object tier or to a plain proxy.
type DeltaConfig struct {
	// Analyzer classifies statements for the upstream's SQL dialect.
	Analyzer sqlanalyzer.DialectAnalyzer
	// CacheClient resolves the delta tier's cache at call time.
	CacheClient func() cache.Cache
	// CacheTTL bounds delta entry lifetimes (typically the backend's
	// timeseries TTL). Zero uses the engine default.
	CacheTTL time.Duration
	// MaxObjectSize rejects oversized entries when positive.
	MaxObjectSize int64
	// RetentionPoints is the backend's timeseries_retention_factor: the newest buckets an entry
	// keeps, and the bound above which a request's range is reported as exceeding it.
	RetentionPoints int
	// VolatileWindow widens the volatile tail excluded from cache storage.
	VolatileWindow time.Duration
	// PartialBucketTTL bounds the lifetime of cached partial buckets.
	PartialBucketTTL time.Duration
	// StepAlignment is the backend's configured step alignment mode; zero uses the default.
	StepAlignment timeseries.StepAlignment
}

// WithDeltaCache enables the delta tier on a Server.
func WithDeltaCache(cfg DeltaConfig) ServerOption {
	return func(s *Server) {
		if cfg.Analyzer != nil && cfg.CacheClient != nil {
			s.deltaConfig = &cfg
		}
	}
}

type ipcCodec struct{} // serializes the object tier's payloads, which are verbatim Arrow IPC streams

func (ipcCodec) Marshal(b []byte) ([]byte, error) {
	if b == nil {
		return nil, errors.New("flight object payload is not cacheable")
	}
	return b, nil
}

func (ipcCodec) Unmarshal(data []byte) ([]byte, error) {
	// the tier gives a codec data of its own, so the payload is that data, capped at its length
	return slices.Clip(data), nil
}

func (ipcCodec) Size(b []byte) int {
	return len(b)
}

// deltaRunner routes statement queries across the delta, object, and proxy
// tiers.
type deltaRunner struct {
	cfg     DeltaConfig
	engine  *nativedelta.Engine[[]byte]
	schemas schemaMemo
}

// a server sees few distinct result schemas, but the memo is bounded in case it sees many
const maxMemoSchemas = 256

// holds entry schemas deserialized, by their serialized form, which every hit would otherwise
// deserialize anew; an Arrow schema is immutable, so hits share one
type schemaMemo struct {
	mu sync.RWMutex
	m  map[string]*arrow.Schema
}

func (sm *schemaMemo) get(header []byte) (*arrow.Schema, error) {
	sm.mu.RLock()
	schema, ok := sm.m[string(header)]
	sm.mu.RUnlock()
	if ok {
		return schema, nil
	}
	schema, err := flight.DeserializeSchema(header, memory.DefaultAllocator)
	if err != nil {
		return nil, err
	}
	sm.mu.Lock()
	if sm.m == nil || len(sm.m) >= maxMemoSchemas {
		sm.m = make(map[string]*arrow.Schema)
	}
	sm.m[string(header)] = schema
	sm.mu.Unlock()
	return schema, nil
}

func newDeltaRunner(cfg DeltaConfig, keyPrefix string) *deltaRunner {
	engineCfg := nativedelta.Config{
		Protocol:         flightsqlDialect,
		BackendName:      keyPrefix,
		CacheClient:      cfg.CacheClient,
		CacheTTL:         cfg.CacheTTL,
		MaxObjectSize:    cfg.MaxObjectSize,
		RetentionPoints:  cfg.RetentionPoints,
		VolatileWindow:   cfg.VolatileWindow,
		PartialBucketTTL: cfg.PartialBucketTTL,
		ObserveCacheFailure: func(reason string) {
			observeCacheFailure(cfg.CacheClient, reason)
		},
		ObserveRewriteFailure: func(reason string) {
			metrics.SQLQueryRewriteFailures.WithLabelValues(
				keyPrefix, flightsqlDialect, reason).Inc()
		},
	}
	if engineCfg.CacheTTL <= 0 {
		engineCfg.CacheTTL = DefaultCacheTTL
	}
	return &deltaRunner{cfg: cfg, engine: nativedelta.New(engineCfg, ipcCodec{})}
}

// observeCacheFailure records an engine cache failure against the configured
// cache's own name and provider, matching the shared cache-event convention.
func observeCacheFailure(cacheClient func() cache.Cache, reason string) {
	if cacheClient == nil {
		return
	}
	resolved := cacheClient()
	if resolved == nil || resolved.Configuration() == nil {
		return
	}
	configuration := resolved.Configuration()
	metrics.CacheEvents.WithLabelValues(configuration.Name, configuration.Provider,
		keys.Error, flightsqlDialect+"_"+reason).Inc()
}

// serve executes one statement query through the three-tier cache, recording
// the analysis mode and cache outcome to the native SQL cache metrics under
// the flightsql dialect.
func (d *deltaRunner) serve(ctx context.Context, s *Server,
	query string,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	now := time.Now()
	analysis := d.cfg.Analyzer.Analyze(query, now)
	d.observeAnalysis(s.keyPrefix, analysis)
	// nondeterministic statements are never cached; the substring check backs
	// up the analyzer for statements it cannot parse
	if analysis.Mode == sqlanalyzer.CacheModeNone ||
		(analysis.Plan == nil && volatileQuery(query)) {
		b, err := s.upstream.Execute(ctx, query)
		if err != nil {
			d.observeCache(s.keyPrefix, sqlanalyzer.CacheModeNone,
				cachestatus.LookupStatusProxyError, 0, time.Since(now))
			return nil, nil, fmt.Errorf("upstream execute: %w", err)
		}
		d.observeCache(s.keyPrefix, sqlanalyzer.CacheModeNone,
			cachestatus.LookupStatusProxyOnly, 0, time.Since(now))
		return s.streamIPCBytes(ctx, b)
	}
	// off answers with the origin's result to the client's statement, keyed on its raw range
	unaligned := nativedelta.RequestStepAlignment(d.cfg.StepAlignment, analysis.Plan) == timeseries.StepAlignmentOff
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil || unaligned {
		kind, ttl := statementKeyKind, s.cacheTTL
		if unaligned && analysis.Mode == sqlanalyzer.CacheModeDelta {
			kind, ttl = unalignedStatementKeyKind, timeseries.StepAlignmentOffTTL
		}
		b, lookupStatus, err := s.objectTierFor(ctx, kind, query, ttl)
		d.observeCache(s.keyPrefix, sqlanalyzer.CacheModeObject,
			lookupStatus, 0, time.Since(now))
		if err != nil {
			return nil, nil, err
		}
		return s.streamIPCBytes(ctx, b)
	}

	plan := analysis.Plan
	trq := planTimeRangeQuery(plan)
	// the separator once preceded directive text, which keys no longer hold; it stays so keys don't change
	baseKey := s.tenantKey(ctx) + ":dpc:" + checksum.Checksum(plan.CanonicalSQL+"|")
	answer, lookupStatus, err := d.engine.ExecuteDelta(nativedelta.DeltaRequest[[]byte]{
		Key:         baseKey,
		FallbackKey: baseKey + ":fallback",
		Statement:   query, StepAlignment: nativedelta.RequestStepAlignment(d.cfg.StepAlignment, plan),
		Context: ctx,
		Plan:    plan,
		Now:     now,
		Ops:     d.ops(ctx, s, query, plan, trq),
	})
	// a verbatim result's row count is unknown without decoding it, so it reports none
	d.observeCache(s.keyPrefix, sqlanalyzer.CacheModeDelta,
		lookupStatus, answer.Delta.Rows(), time.Since(now))
	if err != nil {
		return nil, nil, err
	}
	if answer.Delta == nil {
		return s.streamIPCBytes(ctx, answer.Object)
	}
	return d.respond(ctx, s, answer.Delta, trq, sortKeys(plan))
}

// object tier key kinds; off and partial buckets keep their own statement objects, so one stored for
// another TTL never answers them
const (
	statementKeyKind          = ":stmt:"
	unalignedStatementKeyKind = ":off:"
	partialStatementKeyKind   = ":partial:"
)

// Metric label constants shared with the other native SQL protocols.
const (
	flightsqlDialect  = "flightsql"
	metricMethodQuery = "QUERY"
	metricPathQuery   = "query"
)

// observeAnalysis records the analyzer's classification of one statement,
// which is the only signal for why a statement was not delta-cacheable.
func (d *deltaRunner) observeAnalysis(backend string, analysis sqlanalyzer.Analysis) {
	reason := string(analysis.Reason)
	if reason == "" {
		reason = "unknown"
	}
	metrics.SQLQueryAnalysis.WithLabelValues(backend, flightsqlDialect,
		analysis.Mode.String(), reason).Inc()
	if logger.Level() == level.Debug {
		logger.Debug("flightsql query analyzed", logging.Pairs{
			keys.BackendName: backend, keys.Cache_Mode: analysis.Mode.String(),
			keys.Reason: reason,
		})
	}
}

// observeCache records one statement execution's cache outcome to the native
// SQL cache counter and the standard proxy request metrics.
func (d *deltaRunner) observeCache(backend string, mode sqlanalyzer.CacheMode,
	lookupStatus cachestatus.LookupStatus, points int, elapsed time.Duration,
) {
	httpStatus := "200"
	if lookupStatus == cachestatus.LookupStatusProxyError {
		httpStatus = "500"
	}
	metrics.SQLQueryCache.WithLabelValues(backend, flightsqlDialect,
		mode.String(), lookupStatus.String()).Inc()
	metrics.ProxyRequestStatus.WithLabelValues(backend, flightsqlDialect,
		metricMethodQuery, lookupStatus.String(), httpStatus, metricPathQuery).Inc()
	metrics.ProxyRequestElements.WithLabelValues(backend, flightsqlDialect,
		lookupStatus.String(), metricPathQuery).Add(float64(points))
	metrics.ProxyRequestDuration.WithLabelValues(backend, flightsqlDialect,
		metricMethodQuery, lookupStatus.String(), httpStatus, metricPathQuery).
		Observe(elapsed.Seconds())
}

// sortKeys translates a plan's ORDER BY terms into the reconstruction's sort
// keys, so rows rebuilt from merged cache parts come back in the order the
// statement asked for rather than the model's time-major default.
func sortKeys(plan *sqlanalyzer.QueryPlan) []dsarrow.SortKey {
	if plan == nil || len(plan.Ordering) == 0 {
		return nil
	}
	keys := make([]dsarrow.SortKey, len(plan.Ordering))
	for i, term := range plan.Ordering {
		keys[i] = dsarrow.SortKey{
			Column:     term.Column,
			Descending: term.Descending,
			NullsFirst: term.NullsFirst,
		}
	}
	return keys
}

func (d *deltaRunner) respond(ctx context.Context, s *Server, delta *nativedelta.Delta,
	trq *timeseries.TimeRangeQuery, keys []dsarrow.SortKey,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	// delta rows rebuilt into batches of the preserved schema, in the statement's ORDER BY order
	schema, err := d.schemas.get(delta.Header)
	if err != nil {
		return nil, nil, fmt.Errorf("flight delta schema: %w", err)
	}
	// the rows may be shared with the cache, so the plan's time column is named on a new set of them
	ds := &dataset.DataSet{TimeRangeQuery: trq, ExtentList: delta.DS.ExtentList, Results: delta.DS.Results}
	records, err := dsarrow.ToRecords(schema, ds, keys...)
	if err != nil {
		return nil, nil, fmt.Errorf("flight delta rebuild: %w", err)
	}
	return s.streamRecords(ctx, schema, records)
}

// ops builds the engine callbacks for one request.
func (d *deltaRunner) ops(ctx context.Context, s *Server, query string,
	plan *sqlanalyzer.QueryPlan, trq *timeseries.TimeRangeQuery,
) nativedelta.DeltaOps[[]byte] {
	return nativedelta.DeltaOps[[]byte]{
		Fetch: func(statement string) (*nativedelta.Delta, error) {
			b, err := s.upstream.Execute(ctx, statement)
			if err != nil {
				return nil, fmt.Errorf("upstream execute: %w", err)
			}
			return decodeToDelta(b, plan, trq)
		},
		FetchOriginal: func() ([]byte, error) {
			b, err := s.upstream.Execute(ctx, query)
			if err != nil {
				return nil, fmt.Errorf("upstream execute: %w", err)
			}
			return b, nil
		},
		ObjectFallback: func() ([]byte, cachestatus.LookupStatus, error) {
			return s.objectTier(ctx, query)
		},
		FetchPartial: func(fetchCtx context.Context, statement string, ttl time.Duration,
		) ([]byte, cachestatus.LookupStatus, error) {
			return s.objectTierFor(fetchCtx, partialStatementKeyKind, statement, ttl)
		},
		Model: func(b []byte) (*nativedelta.Delta, error) {
			return decodeToDelta(b, plan, trq)
		},
		// Flight calls share no connection, so a request's partial buckets are fetched beside its interior
		ConcurrentPartials: true,
	}
}

func decodeToDelta(ipcBytes []byte, plan *sqlanalyzer.QueryPlan,
	trq *timeseries.TimeRangeQuery,
) (*nativedelta.Delta, error) {
	// an upstream IPC response as delta rows headed by its serialized schema, or ErrUnmergeable,
	// which sends the request to the object tier, when the rows can't be modeled
	schema, records, err := DecodeRecords(ipcBytes)
	defer func() {
		for _, record := range records {
			record.Release()
		}
	}()
	if err != nil {
		return nil, nativedelta.Unmergeable(err)
	}
	if !dsarrow.Representable(schema, plan.OutputColumn) {
		return nil, nativedelta.Unmergeable(
			fmt.Errorf("%w: %v", dsarrow.ErrNotRepresentable, schema))
	}
	ds, err := dsarrow.FromRecords(schema, records, trq)
	if err != nil {
		return nil, nativedelta.Unmergeable(err)
	}
	return &nativedelta.Delta{
		Header: flight.SerializeSchema(schema, memory.DefaultAllocator),
		DS:     ds,
	}, nil
}

// planTimeRangeQuery carries the plan's response-shape facts to the dataset
// conversion: the output timestamp column and the tag columns that partition
// series.
func planTimeRangeQuery(plan *sqlanalyzer.QueryPlan) *timeseries.TimeRangeQuery {
	trq := &timeseries.TimeRangeQuery{
		Statement: plan.CanonicalSQL,
		Step:      plan.Step,
		TimestampDefinition: timeseries.FieldDefinition{
			Name: plan.OutputColumn, Role: timeseries.RoleTimestamp,
		},
	}
	trq.TagFieldDefintions = make(timeseries.FieldDefinitions, len(plan.GroupColumns))
	for i, name := range plan.GroupColumns {
		trq.TagFieldDefintions[i] = timeseries.FieldDefinition{
			Name: name, Role: timeseries.RoleTag,
		}
	}
	return trq
}
