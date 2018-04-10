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

package main

import "testing"

func TestParseTime(t *testing.T) {
	fixtures := []struct {
		input  string
		output string
	}{
		{"2018-04-07T05:08:53.200Z", "2018-04-07 05:08:53.2 +0000 UTC"},
		{"1523077733", "2018-04-07 05:08:53 +0000 UTC"},
		{"1523077733.2", "2018-04-07 05:08:53.200000047 +0000 UTC"},
	}

	for _, f := range fixtures {
		out, err := parseTime(f.input)
		if err != nil {
			t.Error(err)
		}

		outStr := out.UTC().String()
		if outStr != f.output {
			t.Errorf("Expected %s, got %s for input %s", f.output, outStr, f.input)
		}
	}
}
