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
// Package pool provides an application load balancer pool
package pool

import (
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/observe"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Pool defines the interface for a load balancer pool
type Pool interface {
	// Targets returns the current set of dispatchable targets: the members whose health
	// status meets the pool's floor. A health transition is reflected by the time the
	// status change returns. The slice is shared; callers must not modify it.
	Targets() Targets
	// ConfiguredLen returns the number of pool members as configured, regardless
	// of current health. Mechanisms compare this against len(Targets()) to
	// detect a pool degraded to a subset of its configured members.
	ConfiguredLen() int
	// ConfiguredTargets returns the configured target topology in pool order.
	// The returned slice is a shallow copy and is safe for callers to retain.
	ConfiguredTargets() Targets
	// Core returns the protocol-neutral pool that selection strategies pick from.
	Core() *lb.Pool
	// Stop ends the pool's health subscriptions; its dispatchable set is then frozen.
	Stop()
	// RefreshHealthy forces a rebuild of the pool's dispatchable set.
	RefreshHealthy()
	// Alignment returns the step alignment the pool's members answer under, fixed when it was built.
	Alignment() Alignment
}

// Alignment is the step alignment mode that requests dispatched through one pool are answered under,
// and the warning their merged responses carry
type Alignment struct {
	Mode    timeseries.StepAlignment
	Warning string
}

// pool implements Pool over the protocol-neutral core
type pool struct {
	targets   Targets
	core      *lb.Pool
	view      atomic.Pointer[targetsView]
	alignment Alignment
}

// targetsView is the Targets form of one core snapshot, built once per snapshot
type targetsView struct {
	snap    *lb.Snapshot
	targets Targets
}

// observers hands each event to every observer in turn
type observers []lb.Observer

func (o observers) Observe(ev lb.Event) {
	for _, obs := range o {
		obs.Observe(ev)
	}
}

// New returns a new Pool. The observers are told of its events, the first snapshot included,
// which is published before New returns.
func New(targets Targets, healthyFloor int, extra ...lb.Observer) Pool {
	return NewAligned(targets, healthyFloor, Alignment{}, extra...)
}

// NewAligned returns a new Pool, as New does, whose members answer under the step alignment a
func NewAligned(targets Targets, healthyFloor int, a Alignment, extra ...lb.Observer) Pool {
	p := &pool{targets: targets, alignment: a}
	members := make([]*lb.Member, 0, len(targets))
	names := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		// a target with no health status can never be dispatched to
		if t == nil || t.hcStatus == nil {
			continue
		}
		if t.name != "" {
			if _, dup := names[t.name]; dup {
				logger.Warn("alb pool member listed more than once; keeping the first",
					logging.Pairs{"member": t.name})
				continue
			}
			names[t.name] = struct{}{}
		}
		if t.member == nil {
			// a target assembled without a constructor
			t.bind(nil)
		}
		members = append(members, t.member)
	}
	observer := observe.Pool()
	if len(extra) > 0 {
		observer = append(observers{observer}, extra...)
	}
	core, err := lb.NewPool(members, healthyFloor, lb.PoolOptions{Observer: observer, Value: p})
	if err != nil {
		// unreachable: nil and repeated members were filtered above
		logger.Error("alb pool could not be built", logging.Pairs{"error": err.Error()})
		core, _ = lb.NewPool(nil, healthyFloor, lb.PoolOptions{Value: p})
	}
	p.core = core
	p.Targets()
	return p
}

func (p *pool) Core() *lb.Pool {
	return p.core
}

func (p *pool) Alignment() Alignment {
	return p.alignment
}

// ModeOf returns the step alignment mode of the pool a pick was made from: zero for a pick from a
// core pool that no Pool built
func ModeOf(pk lb.Pick) timeseries.StepAlignment {
	if cp := pk.Pool(); cp != nil {
		if p, ok := cp.Value().(*pool); ok {
			return p.alignment.Mode
		}
	}
	return 0
}

func (p *pool) RefreshHealthy() {
	p.core.Refresh()
}

func (p *pool) ConfiguredLen() int {
	return len(p.targets)
}

func (p *pool) ConfiguredTargets() Targets {
	return append(Targets(nil), p.targets...)
}

func (p *pool) Targets() Targets {
	snap := p.core.Snapshot()
	if v := p.view.Load(); v != nil && v.snap == snap {
		return v.targets
	}
	return p.buildView(snap)
}

// buildView runs once per snapshot, off the steady-state path. Racing builders store equal
// views; one that stores a superseded view is corrected by the next call.
func (p *pool) buildView(snap *lb.Snapshot) Targets {
	targets := make(Targets, 0, len(snap.Members))
	for _, m := range snap.Members {
		if t, ok := m.Value.(*Target); ok {
			targets = append(targets, t)
		}
	}
	p.view.Store(&targetsView{snap: snap, targets: targets})
	return targets
}

func (p *pool) Stop() {
	p.core.Stop()
}
