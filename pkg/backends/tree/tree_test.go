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
	"errors"
	"strings"
	"testing"
)

func TestEntriesValidate(t *testing.T) {
	tests := []struct {
		name        string
		entries     Entries
		wantErr     bool
		errContains string
		wantTarget  map[string]string // entry name -> expected TargetProvider
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
			name:    "empty entries",
			entries: Entries{},
			wantErr: false,
		},
		{
			name: "self in UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"A"}},
			},
			wantErr:     true,
			errContains: "cannot use itself as a destination",
		},
		{
			name: "self in Pool",
			entries: Entries{
				{Name: "A", Type: "alb", Pool: []string{"A"}},
			},
			wantErr:     true,
			errContains: "cannot use itself as a pool member",
		},
		{
			name: "nested self in Pool via UserRouterPool follow",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B"}},
				{Name: "B", Type: "rule", Pool: []string{"B"}},
			},
			wantErr:     true,
			errContains: "cannot include itself in its Pool",
		},
		{
			name: "simple cycle UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B"}},
				{Name: "B", Type: "rule", UserRouterPool: []string{"A"}},
			},
			wantErr:     true,
			errContains: "endless loop detected",
		},
		{
			name: "simple cycle Pool",
			entries: Entries{
				{Name: "A", Type: "alb", Pool: []string{"B"}},
				{Name: "B", Type: "rule", Pool: []string{"A"}},
			},
			wantErr:     true,
			errContains: "endless loop detected",
		},
		{
			name: "indirect cycle UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B"}},
				{Name: "B", Type: "rule", UserRouterPool: []string{"C"}},
				{Name: "C", Type: "alb", UserRouterPool: []string{"A"}},
			},
			wantErr:     true,
			errContains: "endless loop detected",
		},
		{
			name: "invalid member in UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"Z"}},
			},
			wantErr:     true,
			errContains: "invalid destination backend name",
		},
		{
			name: "invalid member in Pool",
			entries: Entries{
				{Name: "A", Type: "alb", Pool: []string{"Z"}},
			},
			wantErr:     true,
			errContains: "invalid pool member backend name",
		},
		{
			name: "unknown nested Pool member via UserRouterPool follow",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B"}},
				{Name: "B", Type: "rule", Pool: []string{"Z"}},
			},
			wantErr:     true,
			errContains: "unknown entry",
		},
		{
			name: "multiple non-virtual types in UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B", "C"}},
				{Name: "B", Type: "custom1"},
				{Name: "C", Type: "custom2"},
			},
			wantErr:     true,
			errContains: "multiple non-virtual types",
		},
		{
			name: "single non-virtual type in UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B", "C"}},
				{Name: "B", Type: "custom1"},
				{Name: "C", Type: "custom1"},
			},
			wantErr:    false,
			wantTarget: map[string]string{"A": "custom1"},
		},
		{
			name: "non-virtual type via nested Pool under UserRouterPool",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B"}},
				{Name: "B", Type: "rule", Pool: []string{"C"}},
				{Name: "C", Type: "prometheus"},
			},
			wantErr:    false,
			wantTarget: map[string]string{"A": "prometheus"},
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
			name: "empty type skipped when collecting non-virtual types",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B", "C"}},
				{Name: "B", Type: ""},
				{Name: "C", Type: "custom1"},
			},
			wantErr:    false,
			wantTarget: map[string]string{"A": "custom1"},
		},
		{
			name: "diamond dependency visits shared child once",
			entries: Entries{
				{Name: "A", Type: "alb", UserRouterPool: []string{"B", "C"}},
				{Name: "B", Type: "rule", Pool: []string{"D"}},
				{Name: "C", Type: "rule", Pool: []string{"D"}},
				{Name: "D", Type: "prometheus"},
			},
			wantErr:    false,
			wantTarget: map[string]string{"A": "prometheus"},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.entries.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.errContains != "" {
				if err == nil {
					t.Fatalf("Validate() expected error containing %q, got nil", tt.errContains)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Validate() error = %v, want containing %q", err, tt.errContains)
				}
				if _, ok := errors.AsType[*InvalidBackendRoutingError](err); !ok {
					t.Errorf("Validate() error type = %T, want *InvalidBackendRoutingError", err)
				}
			}
			for name, want := range tt.wantTarget {
				ent := findEntry(tt.entries, name)
				if ent == nil {
					t.Fatalf("entry %q not found", name)
				}
				if ent.TargetProvider != want {
					t.Errorf("entry %q TargetProvider = %q, want %q", name, ent.TargetProvider, want)
				}
			}
		})
	}
}

func findEntry(entries Entries, name string) *Entry {
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	return nil
}

func TestNewErrInvalidBackendRouting(t *testing.T) {
	err := NewErrInvalidBackendRouting("entry '%s' bad", "x")
	if _, ok := errors.AsType[*InvalidBackendRoutingError](err); !ok {
		t.Fatalf("expected *InvalidBackendRoutingError, got %T", err)
	}
	if got := err.Error(); got != "entry 'x' bad" {
		t.Errorf("Error() = %q, want %q", got, "entry 'x' bad")
	}
}
