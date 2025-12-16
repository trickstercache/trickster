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
	"context"
	"net/http"
	"sync"
	"sync/atomic"
)

// Pool defines the interface for a load balancer pool
type Pool interface {
	// Healthy returns the full list of Healthy Targets as http.Handlers
	Healthy() []http.Handler
	// HealthyTargets returns the full list of Healthy Targets as *Targets
	HealthyTargets() Targets
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
	healthyFloor    int
	ctx             context.Context
	stopper         context.CancelFunc
	ch              chan bool
	mtx             sync.Mutex
}

func (p *pool) RefreshHealthy() {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	hh := make([]http.Handler, len(p.targets))
	ht := make(Targets, len(p.targets))

	var k int
	for _, t := range p.targets {
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

func (p *pool) SetHealthy(h []http.Handler) {
	p.healthyHandlers.Store(&h)
}

func (p *pool) Stop() {
	if p.stopper != nil {
		p.stopper()
	}
}
