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
	// Targets returns the current set of dispatchable targets, re-filtered
	// against each target's atomic hcStatus. This closes the race window
	// between a status flip and the asynchronous healthy-list refresh, so it
	// is the correct method for request dispatch.
	Targets() Targets
	// SetHealthy seeds the pool's healthy set from a handler list. Intended
	// for tests and bootstrap paths that don't drive status updates through
	// healthcheck subscribers.
	SetHealthy([]http.Handler)
	// Stop stops the pool and its health checker goroutines.
	Stop()
	// RefreshHealthy forces a refresh of the pool's healthy handlers list.
	RefreshHealthy()
}

// pool implements Pool
type pool struct {
	targets         Targets
	healthyTargets  atomic.Pointer[Targets]
	liveTargets     atomic.Pointer[Targets]
	healthyHandlers atomic.Pointer[[]http.Handler]
	refreshPending  atomic.Bool // sticky dirty flag indicating healthyTargets must be rebuilt
	healthyFloor    int
	done            chan struct{}
	statusCh        chan bool // receives raw health status change notifications from targets
	ch              chan bool
	mtx             sync.Mutex
	stopOnce        sync.Once
	workers         sync.WaitGroup
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
	lt := ht
	p.liveTargets.Store(&lt)
}

// snapshot returns the eventually-consistent healthy-targets snapshot. Snapshots
// can lag behind atomic status flips; only the refresh worker and internal tests
// should read this directly. Dispatch callers must use Targets().
func (p *pool) snapshot() Targets {
	t := p.healthyTargets.Load()
	if t != nil {
		return *t
	}
	return nil
}

func (p *pool) Targets() Targets {
	if lt := p.liveTargets.Load(); lt != nil && !p.refreshPending.Load() {
		cached := *lt
		allLive := true
		for _, t := range cached {
			if t == nil || t.hcStatus == nil || int(t.hcStatus.Get()) < p.healthyFloor {
				allLive = false
				break
			}
		}
		if allLive {
			return cached
		}
	}
	hl := p.snapshot()
	live := make(Targets, 0, len(hl))
	for _, t := range hl {
		if t == nil || t.hcStatus == nil || int(t.hcStatus.Get()) < p.healthyFloor {
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
	lt := t
	p.liveTargets.Store(&lt)
}

func (p *pool) Stop() {
	p.stopOnce.Do(func() {
		close(p.done)
		for _, t := range p.targets {
			if t != nil && t.hcStatus != nil {
				t.hcStatus.UnregisterSubscriber(p.statusCh)
			}
		}
		// Wait for refresh goroutines so SetHealthy after Stop cannot
		// be overwritten by a late RefreshHealthy.
		p.workers.Wait()
	})
}
