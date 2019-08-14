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
	"fmt"
	"time"
)

// MissingURLParam returns a Formatted Error
func MissingURLParam(param string) error {
	return fmt.Errorf("missing URL parameter: [%s]", param)
}

// TimeArrayEmpty returns a Formatted Error
func TimeArrayEmpty(param string) error {
	return fmt.Errorf("time array is nil or empty: [%s]", param)
}

// StepParse returns a timeseries Parsing Error
func StepParse() error {
	return fmt.Errorf("unable to parse timeseries step from downstream request")
}

// NotSelectStatement returns a timeseries Parsing Error
func NotSelectStatement() error {
	return fmt.Errorf("not a select statement")
}

// NotTimeRangeQuery returns an error indicating the request is does not contain
// a time range query.
func NotTimeRangeQuery() error {
	return fmt.Errorf("not a time range query")
}

// InvalidPath returns an error indicating the request path is not valid.
func InvalidPath(path string) error {
	return fmt.Errorf("invalid request path: %s", path)
}

// ParseDuration returns a Duration Parsing Error
func ParseDuration(input string) (time.Duration, error) {
	return time.Duration(0), fmt.Errorf("unable to parse duration: %s", input)
}
