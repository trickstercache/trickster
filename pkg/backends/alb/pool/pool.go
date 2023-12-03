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
	Next() []http.Handler
}

type selectionFunc func(*pool) []http.Handler

// Target defines an alb pool target
type Target struct {
	hcStatus *healthcheck.Status
	handler  http.Handler
}

// New returns a new pool
func New(mechanism Mechanism, targets []*Target, healthyFloor int) Pool {
	f, ok := mechsToFuncs()[mechanism]
	if !ok {
		return nil
	}
	p := &pool{
		mechanism:    mechanism,
		targets:      targets,
		f:            f,
		ctx:          context.Background(),
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

type pool struct {
	mechanism    Mechanism
	f            selectionFunc
	targets      []*Target
	healthy      []http.Handler
	healthyFloor int
	pos          atomic.Uint64
	mtx          sync.RWMutex
	ctx          context.Context
	ch           chan bool
}

func (p *pool) Next() []http.Handler {
	return p.f(p)
}

func mechsToFuncs() map[Mechanism]selectionFunc {
	return map[Mechanism]selectionFunc{
		RoundRobin:         nextRoundRobin,
		FirstResponse:      nextFanout,
		FirstGoodResponse:  nextFanout,
		NewestLastModified: nextFanout,
		TimeSeriesMerge:    nextFanout,
	}
}
