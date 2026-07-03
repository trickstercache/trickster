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

package fr

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// FR fanout clones the parent request N ways. For POST/PUT/PATCH the body
// must be primed before fanout, else N goroutines race on r.Body and
// rsc.RequestBody inside GetBody. Run under -race to catch any regression
// in the priming step.
func TestFRPostBodyFanoutIsRaceFree(t *testing.T) {
	const body = `{"query":"sum(rate(metric[5m]))","start":"2024-01-01T00:00:00Z","end":"2024-01-01T01:00:00Z","step":"15s"}`
	albpool.RunPostBodyFanoutRace(t, func(p pool.Pool) http.Handler {
		h := &handler{}
		h.SetPool(p)
		return h
	}, body, 4, 16, nil)
}
