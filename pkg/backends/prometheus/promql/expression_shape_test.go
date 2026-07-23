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

func TestNonShardLocalFunction(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"absent(up)", "absent"},
		{"sum(absent_over_time(up[5m]))", "absent_over_time"},
		{"histogram_quantile(0.9, buckets)", "histogram_quantile"},
		{"scalar(up)", "scalar"},
		{"sort_by_label(up, \"job\")", "sort_by_label"},
		{"absent_total", ""},
		{`label_replace(up, "note", "absent(up)", "src", ".*")`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got, found := NonShardLocalFunction(tt.query)
			if got != tt.want || found != (tt.want != "") {
				t.Fatalf("got (%q, %v) want (%q, %v)", got, found, tt.want, tt.want != "")
			}
		})
	}
}

func TestContainsBinaryExpression(t *testing.T) {
	tests := []struct {
		query string
		want  bool
	}{
		{"up + on (job) group_left down", true},
		{"up-2", true},
		{"metric1e-2", true},
		{"rate(requests[5m]) / rate(errors[5m])", true},
		{"up and on (job) ready", true},
		{"up unless down", true},
		{"up == bool down", true},
		{"rate(up[5m])", false},
		{"rate(up[5m] offset -1h)", false},
		{"clamp_min(up, -1e-3)", false},
		{"clamp_min(up, -0x1p-3)", false},
		{"1e-3", false},
		{`label_replace(up{method=~"GET|POST"}, "dst", "a+b", "src", ".*")`, false},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			if got := ContainsBinaryExpression(tt.query); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
