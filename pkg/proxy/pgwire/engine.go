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
// Package pgwire is an engine-agnostic PostgreSQL wire-protocol reverse proxy.
// Providers that speak the protocol plug in as engines behind one listener adapter.
package pgwire

import (
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
)

// Engine describes one backend provider served over the PostgreSQL wire protocol.
type Engine interface {
	// Name returns the canonical provider name.
	Name() string
	// DefaultPort is the origin port assumed when an origin_url names none.
	DefaultPort() string
}

// Engines is the explicit registry of engines, keyed by canonical provider name.
type Engines map[string]Engine

// NewEngines returns a registry holding the provided engines.
func NewEngines(engines ...Engine) Engines {
	out := make(Engines, len(engines))
	for _, e := range engines {
		out[e.Name()] = e
	}
	return out
}

// Get returns the engine serving a provider name or alias, or nil.
func (e Engines) Get(provider string) Engine {
	return e[providers.Canonical(provider)]
}

// Names returns the sorted provider names and aliases the registry serves.
func (e Engines) Names() []string {
	out := make([]string, 0, len(e))
	for name := range e {
		out = append(out, name)
		out = append(out, providers.Aliases(name)...)
	}
	slices.Sort(out)
	return out
}
