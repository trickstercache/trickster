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

package clickhouse

import "github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/aftership"

var (
	// ErrInvalidSQL indicates that the ClickHouse parser rejected the statement.
	ErrInvalidSQL = aftership.ErrInvalidSQL
	// ErrNotTimeRangeQuery indicates that the statement cannot use delta caching.
	ErrNotTimeRangeQuery = aftership.ErrNotTimeRangeQuery
	// ErrMissingTimeseries indicates that no supported bucket expression was found.
	ErrMissingTimeseries = aftership.ErrMissingTimeseries
	// ErrNoLowerBound indicates that the query has no usable lower time bound.
	ErrNoLowerBound = aftership.ErrNoLowerBound
	// ErrNoUpperBound indicates that the query has no usable upper time bound.
	ErrNoUpperBound = aftership.ErrNoUpperBound
	// ErrInvalidGroupByClause indicates that GROUP BY is unsafe for delta caching.
	ErrInvalidGroupByClause = aftership.ErrInvalidGroupByClause
	// ErrUnsafePredicate indicates that a time predicate cannot be safely rewritten.
	ErrUnsafePredicate = aftership.ErrUnsafePredicate
	// ErrAmbiguousTimeAxis indicates that more than one primary time range was found.
	ErrAmbiguousTimeAxis = aftership.ErrAmbiguousTimeAxis
	// ErrUnsupportedStatement indicates a SELECT shape outside the analyzer subset.
	ErrUnsupportedStatement = aftership.ErrUnsupportedStatement
	// ErrLimitUnsupported indicates the input a LIMIT keyword, which is currently unsupported
	// in the caching layer
	ErrLimitUnsupported = aftership.ErrLimitUnsupported
	// ErrUnsupportedOutputFormat indicates the FORMAT value for the query is not supported
	ErrUnsupportedOutputFormat = aftership.ErrUnsupportedOutputFormat
)
