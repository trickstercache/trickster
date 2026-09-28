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

package greptimedb

import (
	"errors"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlguard"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
)

var (
	errVolatile  = errors.New("statement reads changing state")
	compactUnits = map[string]time.Duration{
		"ns": time.Nanosecond, "us": time.Microsecond, "ms": time.Millisecond,
		"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
	}
	guardedWords = map[string]sqlguard.WordClass{
		"random": sqlguard.Volatile, "rand": sqlguard.Volatile, "uuid": sqlguard.Volatile,
		"uuid_v4": sqlguard.Volatile, "uuid_v7": sqlguard.Volatile, "gen_random_uuid": sqlguard.Volatile,
		"jev": sqlguard.Volatile, "flush_flow": sqlguard.Volatile, "procedure_state": sqlguard.Volatile,
		"now": sqlguard.Clock, "today": sqlguard.Clock,
		"current_timestamp": sqlguard.BareClock, "current_date": sqlguard.BareClock,
		"current_time": sqlguard.BareClock, "localtimestamp": sqlguard.BareClock, "localtime": sqlguard.BareClock,
		"unnest": sqlguard.SetReturning, "generate_series": sqlguard.SetReturning,
		"only": sqlguard.Unfaithful,
	}
	analyzer = &sessionAnalyzer{utc: newAnalyzer(true), zoned: newAnalyzer(false)}
)

type sessionAnalyzer struct{ utc, zoned *dialectAnalyzer }

var _ pgwire.SessionAnalyzer = (*sessionAnalyzer)(nil)

func (a *sessionAnalyzer) Analyze(statement string, now time.Time) sqlanalyzer.Analysis {
	return a.zoned.Analyze(statement, now)
}

func (a *sessionAnalyzer) ForSession(view pgwire.SessionView) sqlanalyzer.DialectAnalyzer {
	if view.UTC {
		return a.utc
	}
	return a.zoned
}

type dialectAnalyzer struct{ inner *cockroach.Analyzer }

func newAnalyzer(utc bool) *dialectAnalyzer {
	matchers := []cockroach.BucketMatcher{cockroach.DateBinMatcher, cockroach.CompactDateBinMatcher(compactUnits)}
	if utc {
		matchers = append(matchers, cockroach.DateTruncMatcher)
	}
	return &dialectAnalyzer{inner: cockroach.NewAnalyzer(cockroach.Options{
		BucketMatchers: matchers, ExprBucketMatchers: []cockroach.ExprBucketMatcher{cockroach.EpochFloorMatcher},
		RoundUnalignedTimeBounds: true, NakedIntIsInt4: true,
		// DataFusion truncates finer bounds instead of rounding up as PostgreSQL does.
		BoundPrecision: time.Nanosecond, RejectZonelessBounds: !utc, PostRender: postRender,
	})}
}

func (a *dialectAnalyzer) Analyze(statement string, now time.Time) sqlanalyzer.Analysis {
	analysis := a.inner.Analyze(statement, now)
	facts := sqlguard.Scan(statement, guardedWords)
	if analysis.Mode == sqlanalyzer.CacheModeDelta && facts.Clock && !facts.Volatile {
		facts.Clock = sqlguard.Scan(cockroach.MaskPlaceholders(analysis.Plan.CanonicalSQL), guardedWords).Clock
	}
	switch {
	case facts.Volatile || facts.Clock:
		return sqlanalyzer.Analysis{Mode: sqlanalyzer.CacheModeNone, Reason: sqlanalyzer.ReasonNondeterministic, Err: errVolatile}
	case analysis.Mode != sqlanalyzer.CacheModeDelta:
		return analysis
	case facts.Unfaithful || facts.SetReturning:
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedFormat, errUnrenderable)
	case analysis.Plan.Step < time.Microsecond || analysis.Plan.Phase%time.Microsecond != 0:
		// PostgreSQL text timestamps carry microseconds even for nanosecond columns.
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedBucket, errUnrenderable)
	}
	return analysis
}
