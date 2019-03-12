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

package md5

import (
	"testing"
)

func TestTableChecksum(t *testing.T) {
	var tests = []struct {
		input    string
		expected string
	}{
		{"abcd", "e2fc714c4727ee9395f324cd2e7f331f"},
		{"1234", "81dc9bdb52d04dc20036dbd8313ed055"},
		{"trickster", "a16c8420c9a58d63f141fee498205ea1"},
	}

	for _, test := range tests {
		if output := Checksum(test.input); output != test.expected {
			t.Error("Test Failed: {} inputted, {} expected, recieved: {}", test.input, test.expected, output)
		}
	}
}
