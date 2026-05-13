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
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

// Pool defines the interface for a load balancer pool
type Pool interface {
	// Healthy returns the full list of Healthy Targets as http.Handlers
	Healthy() []http.Handler
	// HealthyTargets returns the snapshot of Healthy Targets. Snapshots can
	// lag behind atomic status flips; callers that dispatch traffic should
	// prefer LiveTargets.
	HealthyTargets() Targets
	// LiveTargets returns the snapshot of Healthy Targets re-filtered against
	// each target's current hcStatus. This closes the race window between
	// an atomic status flip and the asynchronous healthy-list refresh.
	LiveTargets() Targets
	// SetHealthy sets the Healthy Targets List
	SetHealthy([]http.Handler)
	// Stop stops the pool and its health checker goroutines
	Stop()
	// RefreshHealthy forces a refresh of the pool's healthy handlers list
	RefreshHealthy()
}

// pool implements Pool
type pool struct {
	targets         Targets
	healthyTargets  atomic.Pointer[Targets]
	healthyHandlers atomic.Pointer[[]http.Handler]
	refreshPending  atomic.Bool // sticky dirty flag indicating HealthyTargets must be rebuilt
	healthyFloor    int
	done            chan struct{}
	statusCh        chan bool // receives raw health status change notifications from targets
	ch              chan bool
	mtx             sync.Mutex
}

// scheduleRefresh marks the healthy list as dirty and coalesces wakeups for
// the refresh worker. The pending flag preserves refresh intent even when
// bursty status changes saturate the channel.
func (p *pool) scheduleRefresh() {
	p.refreshPending.Store(true)
	select {
	case p.ch <- true:
	default:
	}
}

func (p *pool) RefreshHealthy() {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	hh := make([]http.Handler, len(p.targets))
	ht := make(Targets, len(p.targets))

	var k int
	for _, t := range p.targets {
		if t == nil || t.hcStatus == nil {
			continue
		}
		if int(t.hcStatus.Get()) >= p.healthyFloor {
			hh[k] = t.handler
			ht[k] = t
			k++
		}
	}
	hh = hh[:k]
	ht = ht[:k]
	p.healthyHandlers.Store(&hh)
	p.healthyTargets.Store(&ht)
}

func (p *pool) HealthyTargets() Targets {
	t := p.healthyTargets.Load()
	if t != nil {
		return *t
	}
	return nil
}

func (p *pool) Healthy() []http.Handler {
	t := p.healthyHandlers.Load()
	if t != nil {
		return *t
	}
	return nil
}

func (p *pool) LiveTargets() Targets {
	hl := p.HealthyTargets()
	live := make(Targets, 0, len(hl))
	for _, t := range hl {
		if t == nil || int(t.hcStatus.Get()) < p.healthyFloor {
			continue
		}
		live = append(live, t)
	}
	return live
}

func (p *pool) SetHealthy(h []http.Handler) {
	p.healthyHandlers.Store(&h)
	// Materialize parallel Targets each backed by a synthetic Passing status
	// so dispatch-time re-checks against HealthyFloor won't reject them.
	t := make(Targets, len(h))
	for i, hh := range h {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		t[i] = NewTarget(hh, st, nil)
	}
	p.healthyTargets.Store(&t)
}

func (p *pool) Stop() {
	select {
	case <-p.done:
		// already stopped
	default:
		close(p.done)
		for _, t := range p.targets {
			if t != nil && t.hcStatus != nil {
				t.hcStatus.UnregisterSubscriber(p.statusCh)
			}
		}
	}
}
