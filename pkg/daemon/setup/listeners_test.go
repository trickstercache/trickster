/*
 * Copyright 2026 The Trickster Authors
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

package setup

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
)

func TestServerEnabledOn(t *testing.T) {
	tests := []struct {
		configuredServer string
		listenerName     string
		want             bool
	}{
		{mgmt.ServerNameMgmt, mgmt.ServerNameMgmt, true},
		{mgmt.ServerNameMgmt, mgmt.ServerNameMetrics, false},
		{mgmt.ServerNameMetrics, mgmt.ServerNameMgmt, false},
		{mgmt.ServerNameMetrics, mgmt.ServerNameMetrics, true},
		{mgmt.ServerNameBoth, mgmt.ServerNameMgmt, true},
		{mgmt.ServerNameBoth, mgmt.ServerNameMetrics, true},
		{mgmt.ServerNameOff, mgmt.ServerNameMgmt, false},
		{mgmt.ServerNameOff, mgmt.ServerNameMetrics, false},
	}

	for _, test := range tests {
		if got := serverEnabledOn(test.configuredServer, test.listenerName); got != test.want {
			t.Errorf("serverEnabledOn(%q, %q) = %t, want %t",
				test.configuredServer, test.listenerName, got, test.want)
		}
	}
}
