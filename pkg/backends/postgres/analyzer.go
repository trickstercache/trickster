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
package postgres

import (
	"errors"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
)

var (
	errVolatile     = errors.New("statement calls a volatile function")
	errGapfill      = errors.New("gapfill result depends on the whole requested range")
	errSetReturning = errors.New("statement calls a set-returning function")
)

const timestampPrecision = time.Microsecond

var analyzer = &sessionAnalyzer{utc: newDialectAnalyzer(true), zoned: newDialectAnalyzer(false)}

func newDialectAnalyzer(utc bool) *dialectAnalyzer {
	matchers := bucketMatchers{utc: utc}
	buckets := []cockroach.BucketMatcher{matchers.timeBucket, matchers.dateBin}
	if utc {
		buckets = append(buckets, dateTrunc)
	}
	return &dialectAnalyzer{inner: cockroach.NewAnalyzer(cockroach.Options{
		BucketMatchers:     buckets,
		ExprBucketMatchers: []cockroach.ExprBucketMatcher{epochFloor},
		// Grafana sends live, unaligned ranges; round them to the bucket cadence
		// instead of failing closed to the object cache
		RoundUnalignedTimeBounds: true,
		NakedIntIsInt4:           true,
		// timestamps hold microseconds; a finer literal is rounded, up into the next bucket
		BoundPrecision:       timestampPrecision,
		RejectZonelessBounds: !utc,
		PostRender:           postRender,
	})}
}

type sessionAnalyzer struct {
	utc   *dialectAnalyzer
	zoned *dialectAnalyzer
}

var (
	_ sqlanalyzer.DialectAnalyzer = (*sessionAnalyzer)(nil)
	_ pgwire.SessionAnalyzer      = (*sessionAnalyzer)(nil)
)

func (a *sessionAnalyzer) Analyze(statement string, now time.Time) sqlanalyzer.Analysis {
	// without a session to vouch for UTC, the zone-safe rules apply
	return a.zoned.Analyze(statement, now)
}

func (a *sessionAnalyzer) ForSession(view pgwire.SessionView) sqlanalyzer.DialectAnalyzer {
	if view.UTC {
		return a.utc
	}
	return a.zoned
}

type dialectAnalyzer struct {
	inner *cockroach.Analyzer
}

func (a *dialectAnalyzer) Analyze(statement string, now time.Time) sqlanalyzer.Analysis {
	analysis := a.inner.Analyze(statement, now)
	if analysis.Mode == sqlanalyzer.CacheModeNone {
		return analysis
	}
	facts := scanFacts(statement)
	if analysis.Mode == sqlanalyzer.CacheModeDelta && facts.clock && !facts.volatile {
		// a clock read that was a time bound is already resolved out of the canonical statement
		facts.clock = scanFacts(cockroach.MaskPlaceholders(analysis.Plan.CanonicalSQL)).clock
	}
	switch {
	case facts.volatile || facts.clock:
		return sqlanalyzer.Analysis{
			Mode: sqlanalyzer.CacheModeNone, Reason: sqlanalyzer.ReasonNondeterministic, Err: errVolatile,
		}
	case analysis.Mode != sqlanalyzer.CacheModeDelta:
		return analysis
	case facts.unfaithful:
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedFormat, errUnrenderable)
	case facts.setReturning:
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedFormat, errSetReturning)
	case facts.gapfill && (facts.carries || len(analysis.Plan.GroupColumns) > 0 || analysis.Plan.UpperBound == nil):
		// a sub-range sees neither the carried value, the series absent from it, nor the client's finish
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedBucket, errGapfill)
	}
	return analysis
}
