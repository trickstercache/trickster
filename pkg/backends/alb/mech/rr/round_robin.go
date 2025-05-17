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

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
)

const ID types.ID = 0
const ShortName types.Name = "rr"
const Name types.Name = "round_robin"

type handler struct {
	pool pool.Pool
	pos  atomic.Uint64
}

func New(_ *options.Options, _ rt.Lookup) (types.Mechanism, error) {
	return &handler{}, nil
}

func (h *handler) SetPool(p pool.Pool) {
	h.pool = p
}

func (h *handler) ID() types.ID {
	return ID
}

func (h *handler) Name() types.Name {
	return ShortName
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		handlers.HandleBadGateway(w, r)
		return
	}
	t := h.nextTarget()
	if t != nil {
		t.ServeHTTP(w, r)
		return
	}
	handlers.HandleBadGateway(w, r)
}

func (h *handler) StopPool() {
	if h.pool != nil {
		h.pool.Stop()
	}
}

func (h *handler) nextTarget() http.Handler {
	healthy := h.pool.Healthy()
	if len(healthy) == 0 {
		return nil
	}
	i := h.pos.Add(1) % uint64(len(healthy))
	return healthy[i]
}
