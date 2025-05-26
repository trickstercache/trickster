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

package tree

import (
	"testing"
)

func TestEntriesValidate(t *testing.T) {
	tests := []struct {
		name    string
		entries Entries
		wantErr bool
	}{
		{
			name: "valid no cycles",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B"}},
				{Name: "B", Type: "rule"},
			},
			wantErr: false,
		},
		{
			name: "self in UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"A"}},
			},
			wantErr: true,
		},
		{
			name: "simple cycle UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B"}},
				{Name: "B", Type: "rule", UserRouterPool: []string{"A"}},
			},
			wantErr: true,
		},
		{
			name: "indirect cycle UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B"}},
				{Name: "B", Type: "rule", UserRouterPool: []string{"C"}},
				{Name: "C", Type: "alb", UserRouterPool: []string{"A"}},
			},
			wantErr: true,
		},
		{
			name: "invalid member in UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"Z"}},
			},
			wantErr: true,
		},
		{
			name: "multiple non-virtual types in UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B", "C"}},
				{Name: "B", Type: "custom1"},
				{Name: "C", Type: "custom2"},
			},
			wantErr: true,
		},
		{
			name: "single non-virtual type in UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B", "C"}},
				{Name: "B", Type: "custom1"},
				{Name: "C", Type: "custom1"},
			},
			wantErr: false,
		},
		{
			name: "virtual types only in UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B", "C"}},
				{Name: "B", Type: "rule"},
				{Name: "C", Type: "alb"},
			},
			wantErr: false,
		},
		{
			name: "allow multiple non-virtual types in Pool (not UserRouterPool)",
			entries: Entries{
				{Name: "A", Type: "alb", Pool: []string{"B", "C"}},
				{Name: "B", Type: "custom1"},
				{Name: "C", Type: "custom2"},
			},
			wantErr: false,
		},
		{
			name: "disallow non-virtual types in Pool",
			entries: Entries{
				{Name: "A", Type: "alb", Pool: []string{"B", "C"}},
				{Name: "B", Type: "custom1"},
				{Name: "C", Type: "custom2"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.entries.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
