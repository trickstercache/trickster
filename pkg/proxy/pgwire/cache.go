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
package pgwire

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	keySuffixFallback = ".fallback"
	keySuffixEmpty    = ".empty"
	keySuffixBypass   = ".bypass"

	rewriteOriginRejected = "origin_rejected"
	rewriteResultSize     = "result_size"
	textFormat            = 0
)

var errTimeColumn = errors.New("the bucket column cannot be identified in the result")

type cacheMetricKey struct {
	mode   sqlanalyzer.CacheMode
	status status.LookupStatus
}

type cacheMetricHandles struct {
	native   prometheus.Counter
	requests prometheus.Counter
	elements prometheus.Counter
	duration prometheus.Observer
}

type cacheMetrics struct {
	backend  string
	provider string
	dialect  string
	handles  sync.Map
}

func (m *cacheMetrics) observe(mode sqlanalyzer.CacheMode, lookup status.LookupStatus, rows int, elapsed time.Duration) {
	key := cacheMetricKey{mode: mode, status: lookup}
	value, ok := m.handles.Load(key)
	if !ok {
		httpStatus := metricHTTPStatusOK
		if lookup == status.LookupStatusProxyError || lookup == status.LookupStatusError {
			httpStatus = metricHTTPStatusInternalError
		}
		label := lookup.String()
		value, _ = m.handles.LoadOrStore(key, cacheMetricHandles{
			native: metrics.SQLQueryCache.WithLabelValues(m.backend, m.dialect, mode.String(), label),
			requests: metrics.ProxyRequestStatus.WithLabelValues(m.backend, m.provider,
				metricMethodQuery, label, httpStatus, metricPathQuery),
			elements: metrics.ProxyRequestElements.WithLabelValues(m.backend, m.provider, label, metricPathQuery),
			duration: metrics.ProxyRequestDuration.WithLabelValues(m.backend, m.provider,
				metricMethodQuery, label, httpStatus, metricPathQuery),
		})
	}
	handles := value.(cacheMetricHandles)
	handles.native.Inc()
	handles.requests.Inc()
	handles.elements.Add(float64(rows))
	handles.duration.Observe(elapsed.Seconds())
}

func (s *Server) cacheClient() cache.Cache {
	if s.config.CacheProvider != nil {
		return s.config.CacheProvider.Cache()
	}
	return s.config.Cache
}

func (s *Server) newDeltaEngine() *nativedelta.Engine[*Result] {
	return nativedelta.New[*Result](nativedelta.Config{
		Protocol: cacheKeyProtocol, BackendName: s.config.BackendName, CacheClient: s.cacheClient,
		CacheTTL: s.config.CacheTTL, MaxObjectSize: s.config.MaxObjectSize,
		RetentionPoints: s.config.RetentionPoints,
		ObserveCacheFailure: func(reason string) {
			if client := s.cacheClient(); client != nil && client.Configuration() != nil {
				configuration := client.Configuration()
				metrics.CacheEvents.WithLabelValues(configuration.Name, configuration.Provider,
					keys.Error, cacheKeyProtocol+"_"+reason).Inc()
			}
		},
		ObserveRewriteFailure: s.observeRewriteFailure,
	}, resultCodec{})
}

func (s *Server) observeRewriteFailure(reason string) {
	metrics.SQLQueryRewriteFailures.WithLabelValues(s.config.BackendName, s.config.Dialect, reason).Inc()
}

type deltaPlan struct {
	plan *sqlanalyzer.QueryPlan
}

func (p *deltaPlan) rowReader(s *session, rowDescription []byte) (*rowReader, error) {
	var description pgproto3.RowDescription
	if err := description.Decode(rowDescription); err != nil {
		return nil, nativedelta.Unmergeable(err)
	}
	column := -1
	for i := range description.Fields {
		if string(description.Fields[i].Name) != p.plan.OutputColumn {
			continue
		}
		if column >= 0 {
			return nil, nativedelta.Unmergeable(errTimeColumn)
		}
		column = i
	}
	if column < 0 || description.Fields[column].Format != textFormat {
		return nil, nativedelta.Unmergeable(errTimeColumn)
	}
	engine := s.server.config.Engine
	kind, ok := engine.TimeAxis(description.Fields[column].DataTypeOID)
	if !ok {
		return nil, nativedelta.Unmergeable(errTimeColumn)
	}
	decoder, err := newTimeAxisDecoder(kind, p.plan.OutputUnit,
		engine.TimeSemantics().NaiveTimestampsAreUTC, s.tracker.setting)
	if err != nil {
		return nil, nativedelta.Unmergeable(err)
	}
	return &rowReader{timeColumn: column, decoder: decoder, step: p.plan.Step, phase: p.plan.Phase}, nil
}

func orderable(plan *sqlanalyzer.QueryPlan) bool {
	// buckets merge in time order and each keeps its origin's row
	// order, so ORDER BY can be honored only when the bucket is its leading term.
	return len(plan.Ordering) == 0 || plan.Ordering[0].Column == plan.OutputColumn
}

func descending(plan *sqlanalyzer.QueryPlan) bool {
	return len(plan.Ordering) > 0 && plan.Ordering[0].Descending
}

func (s *session) serveCached(outcome gateOutcome) (bool, error) {
	// answers from the cache, fetching only what is missing. false means
	// relay the client's statement instead, which is always safe; an error ends the session.
	engine := s.server.delta
	if engine == nil || s.server.cacheClient() == nil {
		return false, nil
	}
	mode, plan := outcome.analysis.Mode, outcome.analysis.Plan
	if mode == sqlanalyzer.CacheModeDelta && (plan == nil || !orderable(plan)) {
		mode = sqlanalyzer.CacheModeObject
	}
	if _, bypassed := engine.Retrieve(outcome.key + keySuffixBypass); bypassed {
		return false, nil
	}
	if !s.acquireUpstream() {
		return false, net.ErrClosed
	}
	defer s.releaseUpstream()
	started := time.Now()
	var (
		result *Result
		lookup status.LookupStatus
		err    error
	)
	if mode == sqlanalyzer.CacheModeDelta {
		result, lookup, err = s.executeDelta(outcome, plan)
	} else {
		result, lookup, err = s.executeObject(outcome.sql)
	}
	var rejected *originError
	switch {
	case err == nil:
		if !writeAll(s.client, result.encode(mode == sqlanalyzer.CacheModeDelta && descending(plan)),
			s.server.config.WriteTimeout) {
			return false, net.ErrClosed
		}
		s.server.cache.observe(mode, lookup, result.Rows(), time.Since(started))
		return true, nil
	case errors.Is(err, errRelayResumed) && s.relayResumed:
		// this session's own oversized result is already flowing to the client
		s.relayResumed = false
		s.bypass(outcome.key, rewriteResultSize)
		return true, nil
	case errors.As(err, &rejected) && (!rejected.rendered || rejected.code == sqlstateQueryCanceled):
		// the origin's answer to the client's own statement, or to its cancel
		reply := appendFrame(appendFrame(nil, msgErrorResponse, rejected.body), msgReadyForQuery,
			[]byte{byte(s.txStatus.Load())}) // #nosec G115 -- the stored value is one status byte
		if !writeAll(s.client, reply, s.server.config.WriteTimeout) {
			return false, net.ErrClosed
		}
		s.server.cache.observe(mode, status.LookupStatusProxyError, 0, time.Since(started))
		return true, nil
	case rejected != nil:
		// only the rewritten statement failed; the client's own may still succeed
		s.bypass(outcome.key, rewriteOriginRejected)
		return false, nil
	case errors.Is(err, errResultTooLarge), errors.Is(err, errRelayResumed), errors.Is(err, nativedelta.ErrUnmergeable):
		s.bypass(outcome.key, rewriteResultSize)
		return false, nil
	}
	return false, err
}

func (s *session) bypass(key, reason string) {
	// makes later executions of a statement skip the cache until the marker
	// expires, so a plan that cannot be served is not retried on every refresh.
	s.server.observeRewriteFailure(reason)
	s.server.delta.Store(key+keySuffixBypass, &nativedelta.Entry[*Result]{Marker: true})
	logger.Debug("postgres statement will bypass the cache", logging.Pairs{
		logKeyBackend: s.server.config.BackendName, logKeyReason: reason,
	})
}

func (s *session) executeObject(sql string) (*Result, status.LookupStatus, error) {
	key := s.cacheKey(sqlanalyzer.Analysis{Mode: sqlanalyzer.CacheModeObject}, sql)
	return s.server.delta.ExecuteObject(key, func() (*Result, error) {
		return s.fetch(sql, true, nil)
	})
}

func (s *session) executeDelta(outcome gateOutcome, plan *sqlanalyzer.QueryPlan) (*Result, status.LookupStatus, error) {
	config := &s.server.config
	reader := &deltaPlan{plan: plan}
	ops := nativedelta.DeltaOps[*Result]{
		Fetch: func(statement string) (*Result, error) {
			result, err := s.fetch(statement, false, reader)
			var rejected *originError
			switch {
			case errors.As(err, &rejected):
				rejected.rendered = true
			case errors.Is(err, errTimeAxis), errors.Is(err, errResultRow):
				err = nativedelta.Unmergeable(err)
			}
			return result, err
		},
		FetchOriginal: func() (*Result, error) { return s.fetch(outcome.sql, true, nil) },
		Merge:         mergeResults,
		CropResponse: func(payload *Result, requested timeseries.Extent) (*Result, error) {
			if payload == nil || payload.times == nil {
				return nil, errResultRow
			}
			return payload.crop(requested), nil
		},
		Finalize: func(merged *Result, all timeseries.ExtentList, requested timeseries.Extent,
			now time.Time,
		) (*Result, *Result, timeseries.ExtentList, error) {
			return finalizeDelta(config, plan, merged, all, requested, now)
		},
		ObjectFallback: func() (*Result, status.LookupStatus, error) { return s.executeObject(outcome.sql) },
	}
	if config.DoesShard {
		ops.Shard = func(missing timeseries.ExtentList) timeseries.ExtentList {
			out := make(timeseries.ExtentList, 0, len(missing))
			for _, extent := range missing {
				out = append(out, timeseries.ExtentList{extent}.Splice(plan.Step,
					config.ShardMaxRange, config.ShardStep, config.ShardMaxPoints)...)
			}
			return out
		}
	}
	return s.server.delta.ExecuteDelta(nativedelta.DeltaRequest[*Result]{
		Key: outcome.key, FallbackKey: outcome.key + keySuffixFallback, EmptyKey: outcome.key + keySuffixEmpty,
		Plan: plan, Now: time.Now(), Ops: ops,
	})
}

func finalizeDelta(config *Config, plan *sqlanalyzer.QueryPlan, merged *Result, all timeseries.ExtentList,
	requested timeseries.Extent, now time.Time,
) (*Result, *Result, timeseries.ExtentList, error) {
	// shapes the response and what is kept. Retention and the
	// volatile tail bound only the stored object, never the client's response.
	if merged == nil || merged.times == nil {
		return nil, nil, nil, errResultRow
	}
	response := merged.crop(requested)
	retained, extents := merged, all
	if kept, first, trimmed := merged.retain(config.RetentionPoints); trimmed && len(all) > 0 {
		retained = kept
		extents = all.Crop(timeseries.Extent{Start: time.Unix(0, first), End: all[len(all)-1].End})
	}
	window := max(config.BackfillWindow, time.Duration(config.BackfillPoints)*plan.Step, plan.BackfillTolerance)
	if plan.UpperBound == nil {
		// an open-ended range runs to now, whose bucket is still filling
		window = max(window, plan.Step)
	}
	stable := nativedelta.StableExtents(extents, plan.Step, window, now)
	if len(stable) == 0 {
		return response, retained.slice(0, 0), stable, nil
	}
	// rows newer than the stable coverage would outlive a bucket the origin later empties
	retained = retained.crop(timeseries.Extent{Start: stable[0].Start, End: stable[len(stable)-1].End})
	return response, retained, stable, nil
}
