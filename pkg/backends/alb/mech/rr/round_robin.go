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

package rr

import (
	"net/http"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
)

const (
	ID   types.ID   = 0
	Name types.Name = "round_robin"
)

type handler struct {
	mech.PoolHolder
	pos atomic.Uint64
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{ID: ID, Name: Name, ShortName: names.MechanismRR, New: New}
}

func New(_ *options.Options, _ rt.Lookup) (types.Mechanism, error) {
	return &handler{}, nil
}

func (h *handler) ID() types.ID {
	return ID
}

func (h *handler) Name() types.Name {
	return names.MechanismRR
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := h.Pool()
	if p == nil {
		failures.HandleBadGateway(w, r)
		return
	}
	if t := h.nextTarget(p); t != nil {
		t.ServeHTTP(w, r)
		return
	}
	failures.HandleBadGateway(w, r)
}

func (h *handler) StopPool() {
	if p := h.Pool(); p != nil {
		p.Stop()
	}
}

func (h *handler) nextTarget(p pool.Pool) http.Handler {
	targets := p.LiveTargets()
	n := uint64(len(targets))
	if n == 0 {
		return nil
	}
	return targets[h.pos.Add(1)%n].Handler()
}
