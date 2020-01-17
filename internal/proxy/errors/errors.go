/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package errors

import (
	"errors"
	"fmt"
	"time"
)

// ErrStepParse indicates an error parsing the step interval of a time series request
var ErrStepParse = errors.New("unable to parse timeseries step from downstream request")

// ErrNotSelectStatement indicates an error that the time series request is not a read-only select query
var ErrNotSelectStatement = errors.New("not a select statement")

// ErrNotTimeRangeQuery indicates an error that the time series request does not contain a query
var ErrNotTimeRangeQuery = errors.New("not a time range query")

// ErrNoRanges indicates an error that the range request does not contain any usable ranges
var ErrNoRanges = errors.New("no usable ranges")

// MissingURLParam returns a Formatted Error
func MissingURLParam(param string) error {
	return fmt.Errorf("missing URL parameter: [%s]", param)
}

// TimeArrayEmpty returns a Formatted Error
func TimeArrayEmpty(param string) error {
	return fmt.Errorf("time array is nil or empty: [%s]", param)
}

// InvalidPath returns an error indicating the request path is not valid.
func InvalidPath(path string) error {
	return fmt.Errorf("invalid request path: %s", path)
}

// ParseDuration returns a Duration Parsing Error
func ParseDuration(input string) (time.Duration, error) {
	return time.Duration(0), fmt.Errorf("unable to parse duration: %s", input)
}

// ParseRequestBody returns an error indicating the request body could not
// parsed into a valid value.
func ParseRequestBody(err error) error {
	return fmt.Errorf("unable to parse request body: %v", err)
}

// MissingRequestParam returns an error indicating the request is missing a
// required parameter.
func MissingRequestParam(param string) error {
	return fmt.Errorf("missing request parameter: %s", param)
}
