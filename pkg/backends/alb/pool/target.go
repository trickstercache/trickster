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
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
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
	dialable bool
	addr     string
	member   *lb.Member
}

type Targets []*Target

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
		if cfg := backend.Configuration(); cfg != nil {
			t.addr = cfg.Host
			t.dialable = t.addr != "" && !hostnames.Reserved(t.addr)
		}
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
	t.bind(nil)
	return t
}

// bind builds the target's core member, which points back at the target
func (t *Target) bind(stats *lb.Stats) {
	o := lb.MemberOptions{Name: t.name, Group: t.group, Weight: t.weight, Stats: stats, Value: t}
	if t.hcStatus != nil {
		// a nil *Status must not become a non-nil Health
		o.Health = t.hcStatus
	}
	t.member = lb.NewMember(o)
}

// WithStats gives the target runtime stats kept from an earlier target of the same member,
// such as across a config reload; nil leaves its own. It returns the target.
func (t *Target) WithStats(stats *lb.Stats) *Target {
	if stats != nil {
		t.bind(stats)
	}
	return t
}

// WithStatsOf carries prev's runtime stats over to the target that replaces it, so a member
// rebuilt in place, such as for a weight change, is not reset. It returns the target.
func (t *Target) WithStatsOf(prev *Target) *Target {
	if prev != nil && prev.member != nil {
		t.bind(prev.member.Stats())
	}
	return t
}

// Member returns the target's protocol-neutral pool member, whose Value is the target.
func (t *Target) Member() *lb.Member {
	return t.member
}

// Picker returns the balancer of a target whose backend is itself a load balancer, which is
// how a pool of pools is followed to a member that can be dialed. It is nil for any other
// target, and for a load balancer whose mechanism does not select one member.
func (t *Target) Picker() lb.Picker {
	if pp, ok := t.backend.(lb.PickerProvider); ok {
		return pp.Picker()
	}
	return nil
}

// Dialable reports whether the target has an origin address that can be connected to. One
// without an address, or under the reserved .invalid domain, holds its share of a stream
// pool's flows and refuses them. It is decided once, when the target is built.
func (t *Target) Dialable() bool {
	return t.dialable
}

// Addr returns the host:port of the target's origin, captured when the target was built;
// empty when its backend has none.
func (t *Target) Addr() string {
	return t.addr
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
