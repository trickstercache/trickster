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

// Package resolution predicts the step a Graphite origin will use to answer a
// render request, by learning each metric's Whisper archive ladder empirically.
package resolution

import (
	"errors"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
)

// Confidence is the resolver's verdict on a predicted step; the String values
// are the frozen `confidence` label values of the resolution lookups metric.
type Confidence uint8

const (
	// Unknown: no usable step; the request must take the unaccelerated lane
	Unknown Confidence = iota
	// Exact: the step was read from an origin response for this leaf set
	// and age, or computed from ladders that were
	Exact
	// Derived: computed from known leaf ladders and a step-altering function
	// Trickster understands (summarize, LCM over a wildcard expansion)
	Derived
	// Configured: from static_retentions only, not yet probe-confirmed
	Configured
)

func (c Confidence) String() string {
	switch c {
	case Exact:
		return "exact"
	case Derived:
		return "derived"
	case Configured:
		return "configured"
	}
	return "unknown"
}

// Source values: the frozen `source` label values of
// trickster_graphite_resolution_lookups_total
const (
	SourceRegistry = "registry"
	SourceResponse = "response"
	SourceProbe    = "probe"
	SourceStatic   = "static"
	SourceFunction = "function"
	SourceNone     = "none"
)

// Probe kinds and results: the frozen `kind` and `result` label values of
// trickster_graphite_probes_total
const (
	KindNarrow = "narrow"
	KindWide   = "wide"
	KindFind   = "find"

	ResultStep  = "step"
	ResultEmpty = "empty"
	ResultError = "error"
)

// Registry layers: the frozen `layer` label values of
// trickster_graphite_registry_entries
const (
	LayerLeaf     = "leaf"
	LayerLadder   = "ladder"
	LayerTarget   = "target"
	LayerNegative = "negative"
)

var (
	// ErrSpeculative is returned when something tries to record a step that
	// was not read from an origin response or operator configuration
	ErrSpeculative = errors.New("refusing to record a speculative step")
	// ErrMissingMetric means /metrics/find reports no such leaf
	ErrMissingMetric = errors.New("metric does not exist at the origin")
	// ErrProbeBudget means a learning run exhausted its probe budget
	ErrProbeBudget = errors.New("probe budget exhausted")
	// ErrInconsistent means the origin answered probes in a way no Whisper
	// ladder can explain (e.g., a step that decreases with age)
	ErrInconsistent = errors.New("inconsistent probe results")
	// ErrBusy means the learner is at its concurrency cap
	ErrBusy = errors.New("learner at concurrency cap")
	// ErrInvalidLadder means a ladder definition violates Whisper's rules
	ErrInvalidLadder = errors.New("invalid ladder")
)

// Observer receives resolution events for metrics emission; label values
// passed here are the frozen values above.
type Observer interface {
	// Lookup is called once per resolution with its confidence and source
	Lookup(confidence, source string)
	// Probe is called once per upstream probe
	Probe(kind, result string)
	// Ladders reports the number of distinct complete ladders known
	Ladders(n int)
	// RegistryEntries reports the size of one registry layer
	RegistryEntries(layer string, n int)
	// Fallback is called once per render request routed to the
	// unaccelerated lane, with the frozen reason value
	Fallback(reason string)
	// Misprediction is called when an origin response contradicts a
	// predicted step
	Misprediction()
}

// Tracers carries the backend's tracer: resolution components are built before
// any request exists, so the first request carrying a tracer publishes it here.
type Tracers struct {
	p atomic.Pointer[tracing.Tracer]
}

// Set publishes a tracer the first time one is seen
func (t *Tracers) Set(tr *tracing.Tracer) {
	if t != nil && tr != nil {
		t.p.CompareAndSwap(nil, tr)
	}
}

// Get returns the published tracer, or nil
func (t *Tracers) Get() *tracing.Tracer {
	if t == nil {
		return nil
	}
	return t.p.Load()
}

// NopObserver discards every event
type NopObserver struct{}

func (NopObserver) Lookup(string, string)       {}
func (NopObserver) Probe(string, string)        {}
func (NopObserver) Ladders(int)                 {}
func (NopObserver) RegistryEntries(string, int) {}
func (NopObserver) Fallback(string)             {}
func (NopObserver) Misprediction()              {}
