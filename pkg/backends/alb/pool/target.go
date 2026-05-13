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
	go p.listenStatusUpdates()
	go p.checkHealth()
	return p
}

// NewTarget returns a new Target using the provided inputs
func NewTarget(handler http.Handler, hcStatus *healthcheck.Status,
	backend backends.Backend,
) *Target {
	return &Target{
		hcStatus: hcStatus,
		handler:  handler,
		backend:  backend,
	}
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
