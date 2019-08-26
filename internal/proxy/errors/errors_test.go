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
	"testing"
	"time"
)

func TestMissingURLParam(t *testing.T) {
	if MissingURLParam("test").Error() != "missing URL parameter: [test]" {
		t.Errorf("ErrorStepParse failed")
	}
}

func TestTimeArrayEmpty(t *testing.T) {
	if TimeArrayEmpty("test").Error() != "time array is nil or empty: [test]" {
		t.Errorf("ErrorStepParse failed")
	}
}

func TestStepParse(t *testing.T) {
	if StepParse().Error() != "unable to parse timeseries step from downstream request" {
		t.Errorf("ErrorStepParse failed")
	}
}

func TestNotSelectStatement(t *testing.T) {
	if NotSelectStatement().Error() != "not a select statement" {
		t.Errorf("ErrorNotSelectStatement failed")
	}
}

func TestParseDurationError(t *testing.T) {
	_, err := ParseDuration("test")
	if err.Error() != "unable to parse duration: test" {
		t.Errorf("ErrorParseDuration failed")
	}
}

// ParseDurationError returns a Duration Parsing Error
func ParseDurationError(input string) (time.Duration, error) {
	return time.Duration(0), fmt.Errorf("unable to parse duration: %s", input)
}

func TestNotTimeRangeQuery(t *testing.T) {
	err := NotTimeRangeQuery()
	if err.Error() != "not a time range query" {
		t.Errorf("ErrorParseDuration failed, got: %v", err.Error())
	}
}

func TestInvalidPath(t *testing.T) {
	err := InvalidPath("test")
	if err.Error() != "invalid request path: test" {
		t.Errorf("ErrorInvalidPath failed, got: %v", err.Error())
	}
}

func TestParseRequestBody(t *testing.T) {
	err := ParseRequestBody(fmt.Errorf("test"))
	if err.Error() != "unable to parse request body: test" {
		t.Errorf("ErrorParseDuration failed, got: %v", err.Error())
	}
}

func TestMissingRequestParam(t *testing.T) {
	err := MissingRequestParam("test")
	if err.Error() != "missing request parameter: test" {
		t.Errorf("ErrorMissingRequestParam failed, got: %v", err.Error())
	}
}
