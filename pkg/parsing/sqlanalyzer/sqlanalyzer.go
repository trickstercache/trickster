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
//
// # Contract Version
//
// This contract is frozen at version 1, reviewed against the completed
// ClickHouse implementation. Changes must be additive (new reason constants,
// new optional QueryPlan fields); existing fields, methods, and semantics
// below must not change without revisiting every dialect adapter. See
// docs/developer/sql-dialect-adapters.md for adapter requirements.
//
// # Extent Convention
//
// Trickster extents are inclusive sequences of bucket timestamps:
// timeseries.Extent.Start and .End are the first and final INCLUDED bucket
// start times. Analyzers converting an exclusive SQL upper bound (col < X)
// record Bound{Value: X, Inclusive: false}; the adapter subtracts one step
// when deriving the request extent and adds it back when rendering, so the
// original comparator round-trips exactly.
//
// Bound rules: predicates on the raw timestamp column use an inclusive lower
// bound and an exclusive upper bound. Aligned bounds describe complete cache
// buckets directly. A dialect may accept unaligned bounds by rounding the
// lower bound up and the upper bound down to the query cadence when the client
// consumes only complete buckets, or by proving equivalent partial-edge
// handling. When no complete bucket remains, both bounds normalize to the
// rounded-up lower boundary. Predicates on the discrete bucket output may use
// any comparator and are normalized to the first and last included buckets.
// Everything else fails closed.
//
// # QueryPlan Ownership and Lifecycle
//
// Analyze constructs a QueryPlan; after Analyze returns, the plan and its
// embedded Renderer are immutable and owned by the caller. Plans are stored
// on TimeRangeQuery.ParsedQuery, may outlive the analyzer, and must support
// concurrent RenderExtent calls for sharded and partial-hit origin requests.
// Renderer is embedded in the plan rather than expressed as a method on
// DialectAnalyzer so a plan is self-contained: the engine renders extents
// without retaining the analyzer, and each renderer privately closes over
// its own immutable template state.
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

// String returns the stable, low-cardinality metric and log representation of
// a cache mode.
func (m CacheMode) String() string {
	switch m {
	case CacheModeNone:
		return "none"
	case CacheModeObject:
		return "object"
	case CacheModeDelta:
		return "delta"
	default:
		return "unknown"
	}
}

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
	ReasonNondeterministic     AnalysisReason = "nondeterministic"
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

// Bound records a cadence-normalized time boundary without discarding its
// comparator semantics.
type Bound struct {
	Value     time.Time
	Inclusive bool
}

// ExtentRenderer renders a dialect query for an origin cache-miss extent.
// Extents contain the first and final included bucket timestamps. Implementations
// own and keep their dialect AST representation private.
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
	// ValueColumns names deterministic numeric result fields consumed as
	// time-series values. Dialect adapters validate expressions statically and
	// may validate concrete result types when rows arrive.
	ValueColumns []string
	// BackfillTolerance is an optional normalized query directive. Zero uses
	// backend defaults.
	BackfillTolerance time.Duration
	// IdentitySuffix contains normalized result- or cache-policy-affecting
	// directives that are intentionally kept outside executable SQL.
	IdentitySuffix string
	// OutputFormat is a dialect-opaque response-format selector carried from
	// analysis into timeseries.RequestOptions.OutputFormat, which shares the
	// same convention: the byte's meaning is defined by the backend that set
	// it, and no shared code interprets it.
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
