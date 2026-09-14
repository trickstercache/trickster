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

func TestParseSortWrapper(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		wantInner      string
		wantDescending bool
		wantFound      bool
	}{
		{
			name:      "ascending",
			query:     "sort(sum(up))",
			wantInner: "sum(up)",
			wantFound: true,
		},
		{
			name:           "descending",
			query:          "sort_desc(count by (service) (up))",
			wantInner:      "count by (service) (up)",
			wantDescending: true,
			wantFound:      true,
		},
		{
			name:           "surrounding whitespace",
			query:          "  sort_desc \n (label_replace(up, \"dst\", \"$1\", \"src\", \"(.*)\"))  ",
			wantInner:      "label_replace(up, \"dst\", \"$1\", \"src\", \"(.*)\")",
			wantDescending: true,
			wantFound:      true,
		},
		{
			name:           "nested wrappers use outer direction",
			query:          "sort_desc(sort(sum(up)))",
			wantInner:      "sum(up)",
			wantDescending: true,
			wantFound:      true,
		},
		{
			name:      "redundant parentheses",
			query:     "((sort(((sort_desc((sum(up))))))))",
			wantInner: "sum(up)",
			wantFound: true,
		},
		{name: "function names are case-sensitive", query: "SORT(sum(up))"},
		{name: "plain expression", query: "sum(up)"},
		{name: "empty wrapper", query: "sort()"},
		{name: "trailing expression", query: "sort(sum(up)) + 1"},
		{name: "different function", query: "sort_by_label(up, \"job\")"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := ParseSortWrapper(parseOrZero(tt.query))
			if found != tt.wantFound {
				t.Fatalf("found got %v want %v", found, tt.wantFound)
			}
			if got.Inner.String() != tt.wantInner {
				t.Errorf("inner query got %q want %q", got.Inner.String(), tt.wantInner)
			}
			if got.Descending != tt.wantDescending {
				t.Errorf("descending got %v want %v", got.Descending, tt.wantDescending)
			}
		})
	}
}
