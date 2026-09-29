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
	"context"
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

	"github.com/prometheus/client_golang/prometheus"
)

const (
	keySuffixFallback = ".fallback"
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

var primedCacheStatuses = map[sqlanalyzer.CacheMode][]status.LookupStatus{
	sqlanalyzer.CacheModeObject: {status.LookupStatusHit, status.LookupStatusKeyMiss, status.LookupStatusProxyError},
	sqlanalyzer.CacheModeDelta: {
		status.LookupStatusHit, status.LookupStatusPartialHit, status.LookupStatusRangeMiss,
		status.LookupStatusKeyMiss, status.LookupStatusProxyError,
	},
}

func (m *cacheMetrics) prime() {
	// exports every expected series at zero. A counter first seen at 1 has no earlier
	// sample, so rate() and delta() would miss a backend's first partial hit after a restart.
	for mode, statuses := range primedCacheStatuses {
		for _, lookup := range statuses {
			m.resolve(mode, lookup)
		}
	}
}

func (m *cacheMetrics) resolve(mode sqlanalyzer.CacheMode, lookup status.LookupStatus) cacheMetricHandles {
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
	return value.(cacheMetricHandles)
}

func (m *cacheMetrics) observe(mode sqlanalyzer.CacheMode, lookup status.LookupStatus, rows int, elapsed time.Duration) {
	handles := m.resolve(mode, lookup)
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
		RetentionPoints: s.config.RetentionPoints, VolatileWindow: s.config.BackfillWindow,
		VolatileWindowPoints: s.config.BackfillPoints, PartialBucketTTL: s.config.PartialBucketTTL,
		Provider: s.config.Provider,
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
	var unaligned bool
	switch {
	case mode != sqlanalyzer.CacheModeDelta:
	case s.server.config.StepAlignment == timeseries.StepAlignmentOff:
		// off answers with the origin's result to the client's statement, keyed on its raw range
		mode, unaligned = sqlanalyzer.CacheModeObject, true
	case plan == nil || !orderable(plan):
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
		answer nativedelta.Outcome[*Result]
		lookup status.LookupStatus
		err    error
	)
	if mode == sqlanalyzer.CacheModeDelta {
		answer, lookup, err = s.executeDelta(outcome, plan)
	} else {
		answer.Object, lookup, err = s.executeObject(outcome.sql, unaligned)
	}
	var rejected *originError
	switch {
	case err == nil:
		var response []byte
		var rows int
		if answer.Delta != nil {
			response, rows = encodeDelta(answer.Delta, plan), answer.Delta.Rows()
		} else {
			response, rows = answer.Object.encode(), answer.Object.Rows()
		}
		// counted before the client can see the answer, so a reader of both never finds the count behind
		s.server.cache.observe(mode, lookup, rows, time.Since(started))
		if !writeAll(s.client, response, s.server.config.WriteTimeout) {
			return false, net.ErrClosed
		}
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
		s.server.cache.observe(mode, status.LookupStatusProxyError, 0, time.Since(started))
		if !writeAll(s.client, reply, s.server.config.WriteTimeout) {
			return false, net.ErrClosed
		}
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

func (s *session) executeObject(sql string, unaligned bool) (*Result, status.LookupStatus, error) {
	engine, ttl := cacheEngineObject, time.Duration(0)
	if unaligned {
		engine, ttl = cacheEngineUnaligned, timeseries.StepAlignmentOffTTL
	}
	return s.objectTier(engine, sql, ttl, true)
}

func (s *session) objectTier(engine, sql string, ttl time.Duration, original bool,
) (*Result, status.LookupStatus, error) {
	return s.server.delta.ExecuteObject(s.identityKey(engine, sql, ""), ttl, func() (*Result, error) {
		return s.fetch(sql, original, nil)
	})
}

func (s *session) model(plan *sqlanalyzer.QueryPlan, result *Result) (*nativedelta.Delta, error) {
	// an object-tier result's rows, modeled as a fetch's are
	sink := newRowSink(plan)
	if err := sink.describe(s, result.RowDescription); err != nil {
		return nil, err
	}
	for i := range result.Rows() {
		if err := sink.row(result.row(i)); err != nil {
			return nil, err
		}
	}
	return sink.finish()
}

func (s *session) executeDelta(outcome gateOutcome, plan *sqlanalyzer.QueryPlan,
) (nativedelta.Outcome[*Result], status.LookupStatus, error) {
	config := &s.server.config
	ops := nativedelta.DeltaOps[*Result]{
		Fetch: func(statement string) (*nativedelta.Delta, error) {
			sink := newRowSink(plan)
			_, err := s.fetch(statement, false, sink)
			var rejected *originError
			switch {
			case errors.As(err, &rejected):
				rejected.rendered = true
			case errors.Is(err, errTimeAxis), errors.Is(err, errResultRow):
				err = nativedelta.Unmergeable(err)
			}
			if err != nil {
				return nil, err
			}
			rows, err := sink.finish()
			if err != nil {
				return nil, nativedelta.Unmergeable(err)
			}
			return rows, nil
		},
		FetchOriginal:  func() (*Result, error) { return s.fetch(outcome.sql, true, nil) },
		ObjectFallback: func() (*Result, status.LookupStatus, error) { return s.executeObject(outcome.sql, false) },
		// the session's one upstream connection runs its fetches in turn, so none is started early
		FetchPartial: func(_ context.Context, statement string, ttl time.Duration) (*Result, status.LookupStatus, error) {
			return s.objectTier(cacheEnginePartial, statement, ttl, statement == outcome.sql)
		},
		Model: func(result *Result) (*nativedelta.Delta, error) { return s.model(plan, result) },
	}
	if config.DoesShard {
		ops.Shard = func(missing timeseries.ExtentList) timeseries.ExtentList {
			out := make(timeseries.ExtentList, 0, len(missing))
			for _, extent := range missing {
				out = append(out, timeseries.ExtentList{extent}.Splice(plan.Step, plan.Phase,
					config.ShardMaxRange, config.ShardStep, config.ShardMaxPoints)...)
			}
			return out
		}
	}
	return s.server.delta.ExecuteDelta(nativedelta.DeltaRequest[*Result]{
		Key: outcome.key, FallbackKey: outcome.key + keySuffixFallback, Statement: outcome.sql,
		Plan: plan, Now: time.Now(), StepAlignment: config.StepAlignment, Ops: ops,
	})
}
