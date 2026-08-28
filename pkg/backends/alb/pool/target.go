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

package pool

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

// Target defines an alb pool target
type Target struct {
	hcStatus *healthcheck.Status
	handler  http.Handler
	backend  backends.Backend
	name     string
	group    string
	weight   int
	probed   bool
}

type Targets []*Target

// New returns a new Pool
func New(targets Targets, healthyFloor int) Pool {
	p := &pool{
		targets:      targets,
		done:         make(chan struct{}),
		statusCh:     make(chan bool, 1),
		ch:           make(chan bool, 1),
		healthyFloor: healthyFloor,
	}
	p.scheduleRefresh()

	for _, t := range targets {
		if t == nil || t.hcStatus == nil {
			continue
		}
		t.hcStatus.RegisterSubscriber(p.statusCh)
	}
	// populate the healthy snapshot synchronously so a pool installed by a
	// runtime membership swap is dispatchable the moment SetPool returns,
	// rather than 502ing until the async refresh worker's first pass
	p.RefreshHealthy()
	p.workers.Add(2)
	go p.listenStatusUpdates()
	go p.checkHealth()
	return p
}

// NewTarget returns a new Target with the default weight of 1
func NewTarget(handler http.Handler, hcStatus *healthcheck.Status,
	backend backends.Backend,
) *Target {
	return NewWeightedTarget(handler, hcStatus, backend, 1)
}

// NewWeightedTarget returns a new Target with the provided load-balancing
// weight; weights < 1 are normalized to 1
func NewWeightedTarget(handler http.Handler, hcStatus *healthcheck.Status,
	backend backends.Backend, weight int,
) *Target {
	t := &Target{
		hcStatus: hcStatus,
		handler:  handler,
		backend:  backend,
		weight:   max(weight, 1),
		probed:   true,
	}
	if backend != nil {
		t.name, t.group = backendIdentity(backend)
		if cfg := backend.Configuration(); cfg != nil &&
			!backends.IsVirtual(cfg.Provider) {
			// non-virtual members are probed only when an active health
			// check interval is configured; unprobed members can never
			// leave Unchecked and factor into healthy-floor resets
			t.probed = cfg.HealthCheck != nil && cfg.HealthCheck.Interval > 0
		}
	}
	if t.group == "" {
		t.group = t.name
	}
	return t
}

// WithExternalHealth marks the target's health status as externally driven
// (e.g., by discovery-provider readiness), so it counts as probed for
// healthy-floor purposes even without an active health check interval.
// It returns the target for chaining.
func (t *Target) WithExternalHealth() *Target {
	t.probed = true
	return t
}

// Probed returns true when the target's status is driven by an active
// health check probe, an external health source, or is synthetic (virtual
// backends); false means the status can never leave Unchecked.
func (t *Target) Probed() bool {
	return t.probed
}

func backendIdentity(backend backends.Backend) (name, group string) {
	if cfg := backend.Configuration(); cfg != nil {
		name = cfg.Name
		group = cfg.ReplicaGroup
		if group == "" {
			group = cfg.Name
		}
	}
	return
}

func (t *Target) HealthStatus() *healthcheck.Status {
	return t.hcStatus
}

func (t *Target) Handler() http.Handler {
	return t.handler
}

func (t *Target) Backend() backends.Backend {
	return t.backend
}

// Name returns the configured backend name captured when the target was built.
func (t *Target) Name() string {
	return t.name
}

// ReplicaGroup returns the immutable effective replica-group identity captured
// when the target was built.
func (t *Target) ReplicaGroup() string {
	return t.group
}

// Weight returns the target's load-balancing weight (always >= 1). Weights
// apply to mechanisms that select one member per request; fan-out mechanisms
// ignore them.
func (t *Target) Weight() int {
	return t.weight
}
