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
	"bytes"
	"strings"
	"sync"
	"time"

	checksum "github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"

	"github.com/prometheus/client_golang/prometheus"
)

// Reasons a row-returning statement is relayed without being analyzed. They
// share the analysis metric so one panel explains every uncached query.
const (
	reasonMultiStatement sqlanalyzer.AnalysisReason = "multi_statement"
	reasonInTransaction  sqlanalyzer.AnalysisReason = "in_transaction"
	reasonPipelined      sqlanalyzer.AnalysisReason = "pipelined"
	reasonSessionState   sqlanalyzer.AnalysisReason = "session_state"
	reasonQuerySize      sqlanalyzer.AnalysisReason = "query_size"
	reasonUnknown        sqlanalyzer.AnalysisReason = "unknown"

	txStatusIdle = 'I'

	cacheKeySeparator = "."
	cacheKeyProtocol  = "pgwire"
	cacheEngineObject = "opc"
	cacheEngineDelta  = "dpc"

	logKeyCacheMode = "cache_mode"
	logKeyReason    = "analysis_reason"
	logKeyCacheKey  = "cache_key"
)

type analysisMetricKey struct {
	mode   sqlanalyzer.CacheMode
	reason sqlanalyzer.AnalysisReason
}

type analysisMetrics struct {
	backend  string
	dialect  string
	counters sync.Map
}

func (m *analysisMetrics) count(mode sqlanalyzer.CacheMode, reason sqlanalyzer.AnalysisReason) {
	if reason == "" {
		reason = reasonUnknown
	}
	key := analysisMetricKey{mode: mode, reason: reason}
	counter, ok := m.counters.Load(key)
	if !ok {
		counter, _ = m.counters.LoadOrStore(key, metrics.SQLQueryAnalysis.WithLabelValues(
			m.backend, m.dialect, mode.String(), string(reason)))
	}
	counter.(prometheus.Counter).Inc()
}

type gateOutcome struct {
	analysis sqlanalyzer.Analysis
	eligible bool
	key      string
	sql      string
}

func (s *session) gateQuery(body []byte) gateOutcome {
	// classifies one Query message. Whatever the verdict, the message is
	// relayed: a statement the gate cannot vouch for is simply not cached.
	sql := string(bytes.TrimSuffix(body, []byte{0}))
	class := classify(sql, s.tracker.lexicalOptions())
	if class.kind == stmtEmpty {
		return gateOutcome{}
	}
	txIdle := s.txStatus.Load() == txStatusIdle
	settled := s.outstanding.Load() == 0
	s.tracker.observe(&class, txIdle, settled, false)
	if class.kind != stmtRead {
		return gateOutcome{}
	}
	reason := sqlanalyzer.AnalysisReason("")
	cacheable, _ := s.tracker.cacheable()
	switch {
	case class.multi:
		reason = reasonMultiStatement
	case !cacheable || class.unsafe || s.tracker.lexicalOptions():
		// the analyzer reads strings by the standard rules, which backslash escapes change
		reason = reasonSessionState
	case !txIdle:
		reason = reasonInTransaction
	case !settled:
		reason = reasonPipelined
	}
	if reason != "" {
		s.server.analysis.count(sqlanalyzer.CacheModeNone, reason)
		return gateOutcome{}
	}
	analyzer := s.server.config.Analyzer
	if sessioned, ok := analyzer.(SessionAnalyzer); ok {
		analyzer = sessioned.ForSession(SessionView{UTC: s.tracker.utc()})
	}
	analysis := analyzer.Analyze(sql, time.Now())
	s.server.analysis.count(analysis.Mode, analysis.Reason)
	// eligible: analyzed with the session state fully known and nothing in
	// flight, so an answer stored under this key would be safe to send
	outcome := gateOutcome{analysis: analysis, eligible: analysis.Mode != sqlanalyzer.CacheModeNone, sql: sql}
	if outcome.eligible {
		outcome.key = s.cacheKey(analysis, sql)
	}
	if logger.Level() == level.Debug {
		logger.Debug("postgres query analyzed", logging.Pairs{
			logKeyBackend: s.server.config.BackendName, logKeyCacheMode: analysis.Mode.String(),
			logKeyReason: string(analysis.Reason), logKeyCacheKey: outcome.key,
		})
	}
	return outcome
}

func (s *session) observeParse(body []byte) {
	// follows the session effects of an extended-protocol statement.
	// Extended statements are never cached; they only must not go unnoticed.
	_, rest, ok := bytes.Cut(body, []byte{0})
	if !ok {
		s.tracker.disable(unsafeStatement)
		return
	}
	query, _, ok := bytes.Cut(rest, []byte{0})
	if !ok {
		s.tracker.disable(unsafeStatement)
		return
	}
	class := classify(string(query), s.tracker.lexicalOptions())
	s.tracker.observe(&class, s.txStatus.Load() == txStatusIdle, s.outstanding.Load() == 0, true)
}

func (s *session) cacheKey(analysis sqlanalyzer.Analysis, sql string) string {
	// derives the key for an analyzed statement in this session. Every
	// field is length-prefixed, so no two distinct identities can collide.
	config := &s.server.config
	engine, statement, suffix := cacheEngineObject, sql, ""
	if analysis.Mode == sqlanalyzer.CacheModeDelta && analysis.Plan != nil {
		engine, statement, suffix = cacheEngineDelta, analysis.Plan.CanonicalSQL, analysis.Plan.IdentitySuffix
	}
	var identity strings.Builder
	identity.WriteByte(cacheIdentityVersion)
	appendIdentityField(&identity, config.BackendName)
	appendIdentityField(&identity, config.CacheKeyPrefix)
	appendIdentityField(&identity, config.Dialect)
	identity.WriteString(s.tracker.sessionIdentity())
	appendIdentityField(&identity, engine)
	appendIdentityField(&identity, statement)
	appendIdentityField(&identity, suffix)
	return strings.Join([]string{
		config.BackendName, config.CacheKeyPrefix, cacheKeyProtocol, engine, checksum.Checksum(identity.String()),
	}, cacheKeySeparator)
}
