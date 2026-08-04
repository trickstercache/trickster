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

package registry

import (
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
)

func TestCompileSupportedByNamePanicsOnDuplicate(t *testing.T) {
	t.Parallel()
	noop := func(_ *options.Options, _ rt.Lookup) (types.Mechanism, error) {
		return nil, nil
	}
	entries := []types.RegistryEntry{
		{Name: "alpha_long", ShortName: "alpha", New: noop},
		{Name: "beta_long", ShortName: "alpha", New: noop},
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate short name, got none")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "duplicate mechanism name") {
			t.Fatalf("unexpected panic value: %v", r)
		}
	}()
	_ = compileSupportedByName(entries)
}

func TestCompileSupportedByNamePanicsOnNameShortNameCollision(t *testing.T) {
	t.Parallel()
	noop := func(_ *options.Options, _ rt.Lookup) (types.Mechanism, error) {
		return nil, nil
	}
	entries := []types.RegistryEntry{
		{Name: "round_robin", ShortName: "rr", New: noop},
		{Name: "rr", ShortName: "rr2", New: noop},
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on Name colliding with prior ShortName")
		}
	}()
	_ = compileSupportedByName(entries)
}
