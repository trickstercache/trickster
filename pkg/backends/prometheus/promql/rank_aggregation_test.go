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

import (
	"slices"
	"testing"
)

func TestParseRankAggregation(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantFound   bool
		wantOp      string
		wantK       int
		wantInner   string
		wantLabels  []string
		wantWithout bool
		wantSortSet bool
		wantSortDir bool
	}{
		{
			name:      "topk direct",
			query:     "topk(5, up)",
			wantFound: true,
			wantOp:    "topk",
			wantK:     5,
			wantInner: "up",
		},
		{
			name:      "bottomk direct",
			query:     "bottomk(2, rate(http_requests_total[5m]))",
			wantFound: true,
			wantOp:    "bottomk",
			wantK:     2,
			wantInner: "rate(http_requests_total[5m])",
		},
		{
			name:       "topk with pre by grouping",
			query:      "topk by (job, region) (3, up)",
			wantFound:  true,
			wantOp:     "topk",
			wantK:      3,
			wantInner:  "up",
			wantLabels: []string{"job", "region"},
		},
		{
			name:        "topk with post without grouping",
			query:       "topk(4, up) without (instance)",
			wantFound:   true,
			wantOp:      "topk",
			wantK:       4,
			wantInner:   "up",
			wantLabels:  []string{"instance"},
			wantWithout: true,
		},
		{
			name:        "sort_desc wrapper",
			query:       "sort_desc(topk(7, up))",
			wantFound:   true,
			wantOp:      "topk",
			wantK:       7,
			wantInner:   "up",
			wantSortSet: true,
			wantSortDir: true,
		},
		{
			name:        "sort wrapper",
			query:       "sort(bottomk(8, up))",
			wantFound:   true,
			wantOp:      "bottomk",
			wantK:       8,
			wantInner:   "up",
			wantSortSet: true,
		},
		{
			name:      "inner expression contains commas",
			query:     `topk(5, label_replace(up, "dst", "$1", "src", "(.*)"))`,
			wantFound: true,
			wantOp:    "topk",
			wantK:     5,
			wantInner: `label_replace(up, "dst", "$1", "src", "(.*)")`,
		},
		{
			name:      "nested under non-sort expression is not final rank aggregation",
			query:     "sum(topk(5, up))",
			wantFound: false,
		},
		{
			name:      "non rank aggregator",
			query:     "max(up)",
			wantFound: false,
		},
		{
			name:      "non literal k is not finalized",
			query:     "topk(k, up)",
			wantFound: false,
		},
		{
			name:      "fractional k is not finalized",
			query:     "topk(1.5, up)",
			wantFound: false,
		},
		{
			name:      "overflowing k is not finalized",
			query:     "topk(9223372036854775808, up)",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := ParseRankAggregation(tt.query)
			if found != tt.wantFound {
				t.Fatalf("found got %v want %v", found, tt.wantFound)
			}
			if !found {
				return
			}
			if got.Operator != tt.wantOp {
				t.Errorf("operator got %q want %q", got.Operator, tt.wantOp)
			}
			if got.K != tt.wantK {
				t.Errorf("k got %d want %d", got.K, tt.wantK)
			}
			if got.InnerQuery != tt.wantInner {
				t.Errorf("inner query got %q want %q", got.InnerQuery, tt.wantInner)
			}
			if got.Grouping.Without != tt.wantWithout {
				t.Errorf("without got %v want %v", got.Grouping.Without, tt.wantWithout)
			}
			if !slices.Equal(got.Grouping.Labels, tt.wantLabels) {
				t.Errorf("labels got %v want %v", got.Grouping.Labels, tt.wantLabels)
			}
			if got.SortSet != tt.wantSortSet {
				t.Errorf("sort set got %v want %v", got.SortSet, tt.wantSortSet)
			}
			if got.SortDescending != tt.wantSortDir {
				t.Errorf("sort direction got %v want %v", got.SortDescending, tt.wantSortDir)
			}
		})
	}
}
