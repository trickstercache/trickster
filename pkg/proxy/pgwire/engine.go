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
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
)

// TimeAxisKind says how a result column's text value decodes onto the time axis.
type TimeAxisKind uint8

const (
	// TimeAxisTimestampTZ is an instant rendered in the session time zone.
	TimeAxisTimestampTZ TimeAxisKind = iota + 1
	// TimeAxisTimestamp is a zone-less timestamp; see TimeSemantics.
	TimeAxisTimestamp
	// TimeAxisDate is a calendar date.
	TimeAxisDate
	// TimeAxisEpochInteger is an integer count of the plan's epoch unit.
	TimeAxisEpochInteger
	// TimeAxisEpochFloat is a floating-point count of the plan's epoch unit.
	TimeAxisEpochFloat
	// TimeAxisEpochNumeric is an arbitrary-precision count of the plan's epoch unit.
	TimeAxisEpochNumeric
)

// PostgreSQL's built-in type OIDs that can carry a time axis.
const (
	OIDInt8        = 20
	OIDInt2        = 21
	OIDInt4        = 23
	OIDFloat8      = 701
	OIDDate        = 1082
	OIDTimestamp   = 1114
	OIDTimestampTZ = 1184
	OIDNumeric     = 1700
)

var standardTimeAxis = map[uint32]TimeAxisKind{
	OIDTimestampTZ: TimeAxisTimestampTZ, OIDTimestamp: TimeAxisTimestamp, OIDDate: TimeAxisDate,
	OIDInt2: TimeAxisEpochInteger, OIDInt4: TimeAxisEpochInteger, OIDInt8: TimeAxisEpochInteger,
	OIDFloat8: TimeAxisEpochFloat, OIDNumeric: TimeAxisEpochNumeric,
}

// StandardTimeAxis is the time-axis mapping of PostgreSQL's built-in types,
// for engines that keep PostgreSQL's type OIDs.
func StandardTimeAxis(oid uint32) (TimeAxisKind, bool) {
	kind, ok := standardTimeAxis[oid]
	return kind, ok
}

// TimeSemantics describes how an engine's time values relate to real instants.
type TimeSemantics struct {
	// NaiveTimestampsAreUTC means TIMESTAMP WITHOUT TIME ZONE values are UTC
	// instants whatever the session time zone is. PostgreSQL gives them no zone.
	NaiveTimestampsAreUTC bool
}

// EngineDefaults are the connection settings assumed when a backend sets none.
type EngineDefaults struct {
	// UpstreamTLSMode is one of the options package's TLS modes; empty means disable.
	UpstreamTLSMode string
}

// SessionView is what an analyzer may know about the session a statement runs in.
type SessionView struct {
	// UTC means the session TimeZone is UTC, so zone-less times are UTC instants.
	UTC bool
}

// SessionAnalyzer is implemented by an engine analyzer whose verdict depends on
// session settings. ForSession must be cheap; it runs for every analyzed statement.
type SessionAnalyzer interface {
	ForSession(view SessionView) sqlanalyzer.DialectAnalyzer
}

// Engine describes one backend provider served over the PostgreSQL wire protocol.
type Engine interface {
	// Name returns the canonical provider name.
	Name() string
	// DefaultPort is the origin port assumed when an origin_url names none.
	DefaultPort() string
	// Dialect labels the engine's SQL metrics and partitions its cache keys.
	Dialect() string
	// Analyzer returns the engine's SQL analyzer, or nil for an engine that only relays.
	Analyzer() sqlanalyzer.DialectAnalyzer
	// Defaults returns the engine's connection defaults.
	Defaults() EngineDefaults
	// TimeAxis reports how a result column of the given type OID decodes onto
	// the time axis, or false when the type cannot carry one.
	TimeAxis(oid uint32) (TimeAxisKind, bool)
	// TimeSemantics returns the engine's timestamp semantics.
	TimeSemantics() TimeSemantics
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
