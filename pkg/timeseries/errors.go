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

package timeseries

import "errors"

// ErrUnmarshalEpoch is an error for invalid epoch timestamp format
var ErrUnmarshalEpoch = errors.New("could not convert value to epoch timestamp")

// ErrTableHeader is an error for could not deserialize table header
var ErrTableHeader = errors.New("could not deserialize table header")

// ErrInvalidExtent is an error for invalid extent (e.g., end is before start)
var ErrInvalidExtent = errors.New("invalid extent")

// ErrInvalidBody is an error for when a provided body cannot be deserialized to a DataSet
var ErrInvalidBody = errors.New("could not deserialize result body")

// ErrUnknownFormat is an error for when a provided TimeSeries cannot be cast to a DataSet
var ErrUnknownFormat = errors.New("unknown timeseries format")

// ErrNoTimerangeQuery is an error for when a method is provided a nil *TimeRangeQuery
var ErrNoTimerangeQuery = errors.New("no timerange query")

// ErrInvalidTimeFormat is an error for when the provided time is not in the expected format
var ErrInvalidTimeFormat = errors.New("invalid time format")
