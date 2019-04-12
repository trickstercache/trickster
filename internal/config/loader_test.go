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

package config

import (
	"testing"
	"time"
)

func TestLoadConfiguration(t *testing.T) {
	a := []string{}
	// it should not error if config path is not set
	err := Load("trickster-test", "0", a)
	if err != nil {
		t.Error(err)
	}

	if Origins["default"].MaxValueAge == 0 {
		t.Errorf("Expected 86400, got %s", Origins["default"].MaxValueAge)
	}

	if Caches["default"].FastForwardTTL != time.Duration(15)*time.Second {
		t.Errorf("Expected 15, got %s", Caches["default"].FastForwardTTL)
	}

	if Caches["default"].Index.ReapInterval != time.Duration(3)*time.Second {
		t.Errorf("Expected 3, got %s", Caches["default"].Index.ReapInterval)
	}

}
