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

package sqlparser

import "errors"

// ErrNotTimeRangeQuery is an error used when the query does not appear to be a time range for a series
var ErrNotTimeRangeQuery = errors.New("query could not be identified as a time range query")

// ErrMissingTimeseries is an error for when the query does not include timeseries-specific tokens
var ErrMissingTimeseries = errors.New("no timeseries tokens found in SELECT fields")

// ErrNoLowerBound is an error used when the lower bound is missing from the time range query
var ErrNoLowerBound = errors.New("no lower bound found in time range query")

// ErrNoUpperBound is an error used when the upper bound is missing from the time range query
var ErrNoUpperBound = errors.New("no upper bound found in time range query")

// ErrStepParse indicates the Time Range Query's step value could not be parsed from the input
var ErrStepParse = errors.New("unable to parse step duration")

// ErrTimerangeParse indicates the Time Range could not be parsed from the query
var ErrTimerangeParse = errors.New("unable to parse time range")
