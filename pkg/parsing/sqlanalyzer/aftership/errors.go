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

package aftership

import "errors"

var (
	// ErrInvalidSQL indicates that the ClickHouse parser rejected the statement.
	ErrInvalidSQL = errors.New("invalid ClickHouse SQL")
	// ErrNotTimeRangeQuery indicates that the statement cannot use delta caching.
	ErrNotTimeRangeQuery = errors.New("query could not be identified as a time range query")
	// ErrMissingTimeseries indicates that no supported bucket expression was found.
	ErrMissingTimeseries = errors.New("no supported timeseries expression found")
	// ErrNoLowerBound indicates that the query has no usable lower time bound.
	ErrNoLowerBound = errors.New("no lower bound found in time range query")
	// ErrNoUpperBound indicates that the query has no usable upper time bound.
	ErrNoUpperBound = errors.New("no upper bound found in time range query")
	// ErrInvalidGroupByClause indicates that GROUP BY is unsafe for delta caching.
	ErrInvalidGroupByClause = errors.New("invalid or unsupported GROUP BY clause")
	// ErrUnsafePredicate indicates that a time predicate cannot be safely rewritten.
	ErrUnsafePredicate = errors.New("time predicate cannot be safely rewritten")
	// ErrAmbiguousTimeAxis indicates that more than one primary time range was found.
	ErrAmbiguousTimeAxis = errors.New("query has multiple or ambiguous time axes")
	// ErrUnsupportedStatement indicates a SELECT shape outside the analyzer subset.
	ErrUnsupportedStatement = errors.New("unsupported ClickHouse SELECT statement")
	// ErrLimitUnsupported indicates the input a LIMIT keyword, which is currently unsupported
	// in the caching layer
	ErrLimitUnsupported = errors.New("limit queries are not supported")
	// ErrUnsupportedOutputFormat indicates the FORMAT value for the query is not supported
	ErrUnsupportedOutputFormat = errors.New("unsupported output format requested")
)
