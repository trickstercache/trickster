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

package types_test

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/fr"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/nlm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/rr"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
)

// TestPoolMechanismMembership pins which mechs implement PoolMechanism and
// which do not. UR is the only mech without a pool; the rest must satisfy
// both Mechanism and PoolMechanism so the alb client can drive them.
func TestPoolMechanismMembership(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		newMech   func(*testing.T) types.Mechanism
		wantsPool bool
	}{
		{"rr", func(t *testing.T) types.Mechanism {
			m, err := rr.New(nil, nil)
			if err != nil {
				t.Fatalf("rr.New: %v", err)
			}
			return m
		}, true},
		{"fr", func(t *testing.T) types.Mechanism {
			m, err := fr.New(nil, nil)
			if err != nil {
				t.Fatalf("fr.New: %v", err)
			}
			return m
		}, true},
		{"nlm", func(t *testing.T) types.Mechanism {
			m, err := nlm.New(&options.Options{}, nil)
			if err != nil {
				t.Fatalf("nlm.New: %v", err)
			}
			return m
		}, true},
		{"ur", func(t *testing.T) types.Mechanism {
			m, err := ur.New(&options.Options{UserRouter: &uropt.Options{}}, nil)
			if err != nil {
				t.Fatalf("ur.New: %v", err)
			}
			return m
		}, false},
		{"tsm", func(t *testing.T) types.Mechanism {
			o := &options.Options{OutputFormat: providers.Prometheus}
			factories := rt.Lookup{providers.Prometheus: prometheus.NewClient}
			m, err := tsm.New(o, factories)
			if err != nil {
				t.Fatalf("tsm.New: %v", err)
			}
			return m
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := tc.newMech(t)
			if _, ok := any(m).(types.Mechanism); !ok {
				t.Fatalf("%s does not satisfy Mechanism", tc.name)
			}
			_, isPool := any(m).(types.PoolMechanism)
			if isPool != tc.wantsPool {
				t.Fatalf("%s: PoolMechanism membership = %v, want %v",
					tc.name, isPool, tc.wantsPool)
			}
		})
	}
}
