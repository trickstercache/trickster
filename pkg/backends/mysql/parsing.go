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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/vitess"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Statement analysis lives in the shared Vitess adapter under
// pkg/parsing/sqlanalyzer/vitess; this backend consumes its
// dialect-independent analyses and never touches Vitess ASTs directly.
// The error variables are aliased here so protocol- and cache-level code
// classifies analysis failures without importing the adapter package.
var (
	ErrInvalidSQL             = vitess.ErrInvalidSQL
	ErrUnsupportedStatement   = vitess.ErrUnsupportedStatement
	ErrNotTimeRangeQuery      = vitess.ErrNotTimeRangeQuery
	ErrUnsupportedBucket      = vitess.ErrUnsupportedBucket
	ErrUnsafePredicate        = vitess.ErrUnsafePredicate
	ErrAmbiguousTimeAxis      = vitess.ErrAmbiguousTimeAxis
	ErrUnsupportedResultShape = vitess.ErrUnsupportedResultShape
	ErrUnsupportedGrouping    = vitess.ErrUnsupportedGrouping
)

// Analyzer is the shared Vitess-based MySQL dialect analyzer.
type Analyzer = vitess.Analyzer

// NewAnalyzer returns an analyzer configured for MySQL 8 syntax.
func NewAnalyzer() (*Analyzer, error) {
	return vitess.NewAnalyzer()
}

var defaultAnalyzer = vitess.MustNewAnalyzer()

func mustNewAnalyzer() *Analyzer {
	return vitess.MustNewAnalyzer()
}

// Parse converts a MySQL statement into the same TimeRangeQuery contract used
// by the HTTP SQL backend. The protocol handler can use the returned cacheability
// flag to select DPC, OPC, or pass-through behavior without exposing Vitess ASTs.
func Parse(statement string, now time.Time) (*timeseries.TimeRangeQuery, bool, error) {
	analysis := defaultAnalyzer.Analyze(statement, now)
	if analysis.Mode == sqlanalyzer.CacheModeNone {
		return nil, false, analysis.Err
	}
	query := sqlanalyzer.NewTimeRangeQuery(statement)
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		return query, true, analysis.Err
	}
	plan := analysis.Plan
	plan.ApplyToQuery(query)
	if plan.IdentitySuffix != "" {
		query.CacheKeyElements["mysql_directives"] = plan.IdentitySuffix
	}
	window, windowErr := buildDeltaRequestWindow(plan)
	if windowErr != nil {
		return query, true, windowErr
	}
	query.Extent = window.output
	return query, true, nil
}
