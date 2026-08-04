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

package mech

import (
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
)

// PoolHolder embeds in mech handlers to give a race-free pool reference
// across concurrent SetPool (config reload) and reads (in-flight requests).
// The interface value is wrapped in a struct because atomic.Pointer requires
// a non-interface type.
type PoolHolder struct {
	p atomic.Pointer[heldPool]
}

type heldPool struct {
	p pool.Pool
}

func (h *PoolHolder) SetPool(p pool.Pool) {
	h.p.Store(&heldPool{p: p})
}

func (h *PoolHolder) Pool() pool.Pool {
	if v := h.p.Load(); v != nil {
		return v.p
	}
	return nil
}
