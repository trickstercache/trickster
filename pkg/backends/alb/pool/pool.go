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

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

// Pool defines the interface for a load balancer pool
type Pool interface {
	// Healthy returns the full list of Healthy Targets
	Healthy() []http.Handler
	// SetHealthy sets the Healthy Targets List
	SetHealthy([]http.Handler)
	// Stop stops the pool and its health checker goroutines
	Stop()
	// RefreshHealthy forces a refresh of the pool's healthy handlers list
	RefreshHealthy()
}

// Target defines an alb pool target
type Target struct {
	hcStatus *healthcheck.Status
	handler  http.Handler
}

// New returns a new Pool
func New(targets []*Target, healthyFloor int) Pool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &pool{
		targets:      targets,
		ctx:          ctx,
		stopper:      cancel,
		ch:           make(chan bool, 16),
		healthyFloor: healthyFloor,
	}
	p.ch <- true

	for _, t := range targets {
		t.hcStatus.RegisterSubscriber(p.ch)
	}
	go p.checkHealth()
	return p
}

// NewTarget returns a new Target using the provided inputs
func NewTarget(handler http.Handler, hcStatus *healthcheck.Status) *Target {
	return &Target{
		hcStatus: hcStatus,
		handler:  handler,
	}
}

// pool implements Pool
type pool struct {
	targets      []*Target
	healthy      atomic.Pointer[[]http.Handler]
	healthyFloor int
	ctx          context.Context
	stopper      context.CancelFunc
	ch           chan bool
	mtx          sync.Mutex
}

func (p *pool) RefreshHealthy() {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	h := make([]http.Handler, len(p.targets))
	var k int
	for _, t := range p.targets {
		if int(t.hcStatus.Get()) >= p.healthyFloor {
			h[k] = t.handler
			k++
		}
	}
	h = h[:k]
	p.healthy.Store(&h)
}

func (p *pool) Healthy() []http.Handler {
	t := p.healthy.Load()
	if t != nil {
		return *t
	}
	return nil
}

func (p *pool) SetHealthy(h []http.Handler) {
	p.healthy.Store(&h)
}

func (p *pool) Stop() {
	if p.stopper != nil {
		p.stopper()
	}
}
