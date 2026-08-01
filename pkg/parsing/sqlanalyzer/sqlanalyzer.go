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

// Package sqlanalyzer defines the dialect-independent SQL facts needed by
// Trickster's caching engine. Dialect parser and AST types must not appear in
// this package.
package sqlanalyzer

import (
	"errors"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// CacheMode describes the strongest cache strategy supported by an analyzed
// SQL statement.
type CacheMode uint8

const (
	CacheModeNone CacheMode = iota
	CacheModeObject
	CacheModeDelta
)

// AnalysisReason is a stable, low-cardinality reason for an analysis outcome.
type AnalysisReason string

const (
	ReasonDeltaCacheable       AnalysisReason = "delta_cacheable"
	ReasonInvalidSQL           AnalysisReason = "invalid_sql"
	ReasonUnsupportedStatement AnalysisReason = "unsupported_statement"
	ReasonNotTimeRange         AnalysisReason = "not_time_range"
	ReasonUnsupportedBucket    AnalysisReason = "unsupported_bucket"
	ReasonUnsafePredicate      AnalysisReason = "unsafe_predicate"
	ReasonAmbiguousTimeAxis    AnalysisReason = "ambiguous_time_axis"
	ReasonUnsupportedGrouping  AnalysisReason = "unsupported_grouping"
	ReasonUnsupportedFormat    AnalysisReason = "unsupported_format"
	ReasonUnsupportedLimit     AnalysisReason = "unsupported_limit"
)

// Analysis is the semantic result of parsing a dialect SQL statement.
type Analysis struct {
	Mode   CacheMode
	Reason AnalysisReason
	Plan   *QueryPlan
	Err    error
}

// DialectAnalyzer converts dialect SQL into the small set of time-series facts
// needed by Trickster.
type DialectAnalyzer interface {
	Analyze(statement string, now time.Time) Analysis
}

// Bound records a requested time boundary without discarding its comparator
// semantics.
type Bound struct {
	Value     time.Time
	Inclusive bool
}

// ExtentRenderer renders a dialect query for an origin cache-miss extent.
// Implementations own and keep their dialect AST representation private.
type ExtentRenderer interface {
	RenderExtent(extent timeseries.Extent) (string, error)
}

// QueryPlan contains only database-independent facts consumed by Trickster.
type QueryPlan struct {
	CanonicalSQL string
	TimeColumn   string
	OutputColumn string
	Step         time.Duration
	Phase        time.Duration
	OutputUnit   timeseries.FieldDataType
	InputUnit    timeseries.FieldDataType
	LowerBound   *Bound
	UpperBound   *Bound
	GroupColumns []string
	OutputFormat byte
	Renderer     ExtentRenderer
}

// ErrMissingRenderer indicates that a plan cannot produce an origin query.
var ErrMissingRenderer = errors.New("missing SQL query extent renderer")

// RenderExtent renders the query for an origin cache-miss extent.
func (p *QueryPlan) RenderExtent(extent timeseries.Extent) (string, error) {
	if p == nil || p.Renderer == nil {
		return "", ErrMissingRenderer
	}
	return p.Renderer.RenderExtent(extent)
}
