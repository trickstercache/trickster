/*
 * Copyright 2026 The Trickster Authors
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
package pick

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
)

func newRR() *handler {
	return New(names.MechanismRR, rr.New()).(*handler)
}

// nextTarget selects as a request would, without dispatching
func nextTarget(h *handler) http.Handler {
	pk, ok := h.balancer.Pick(lb.Flow{})
	if !ok {
		return nil
	}
	return pk.Member().Value.(*pool.Target).Handler()
}
