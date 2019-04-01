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

package proxy

import "fmt"

// ErrorMissingURLParam returns a Formatted Error
func ErrorMissingURLParam(param string) error {
	return fmt.Errorf("missing URL parameter: [%s]", param)
}

// ErrorTimeArrayEmpty returns a Formattted Error
func ErrorTimeArrayEmpty(param string) error {
	return fmt.Errorf("time array is nil or empty: [%s]", param)
}

// ErrorStepParse returns a timeseries Parsing Error
func ErrorStepParse() error {
	return fmt.Errorf("unable to parse timeseries step from downstream request")
}

// ErrorNotSelectStatement returns a timeseries Parsing Error
func ErrorNotSelectStatement() error {
	return fmt.Errorf("not a select statement")
}
