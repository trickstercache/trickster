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

import (
	"testing"
)

func TestErrorMissingURLParam(t *testing.T) {
	if ErrorMissingURLParam("test").Error() != "missing URL parameter: [test]" {
		t.Errorf("ErrorStepParse failed")
	}
}

func TestErrorTimeArrayEmpty(t *testing.T) {
	if ErrorTimeArrayEmpty("test").Error() != "time array is nil or empty: [test]" {
		t.Errorf("ErrorStepParse failed")
	}
}

func TestErrorStepParse(t *testing.T) {
	if ErrorStepParse().Error() != "unable to parse timeseries step from downstream request" {
		t.Errorf("ErrorStepParse failed")
	}
}

func TestErrorNotSelectStatement(t *testing.T) {
	if ErrorNotSelectStatement().Error() != "not a select statement" {
		t.Errorf("ErrorNotSelectStatement failed")
	}
}
