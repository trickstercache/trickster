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

package promql

import "testing"

func TestIsScalarCall(t *testing.T) {
	tests := map[string]bool{
		"scalar(count(up))":          true,
		" SCALAR (sum(up)) ":         true,
		`scalar(count({label="("}))`: true,
		"scalar_value(up)":           false,
		"scalar(up) + up":            false,
		"scalar(up":                  false,
		"vector(1)":                  false,
		"sum(up)":                    false,
		"scalar":                     false,
	}
	for query, want := range tests {
		if got := IsScalarCall(query); got != want {
			t.Errorf("IsScalarCall(%q) = %v, want %v", query, got, want)
		}
	}
}
