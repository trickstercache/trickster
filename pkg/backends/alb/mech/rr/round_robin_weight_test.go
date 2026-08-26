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
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

type countingHandler struct {
	hits int
}

func (c *countingHandler) ServeHTTP(_ http.ResponseWriter, _ *http.Request) {
	c.hits++
}

func passingStatus() *healthcheck.Status {
	st := &healthcheck.Status{}
	st.Set(healthcheck.StatusPassing)
	return st
}

// TestWeightedRoundRobinExactApportionment verifies that over any
// totalWeight consecutive selections against a stable pool, each member is
// selected exactly Weight times.
func TestWeightedRoundRobinExactApportionment(t *testing.T) {
	weights := []int{1, 3, 2}
	handlers := make([]*countingHandler, len(weights))
	targets := make(pool.Targets, len(weights))
	for i, w := range weights {
		handlers[i] = &countingHandler{}
		targets[i] = pool.NewWeightedTarget(handlers[i], passingStatus(), nil, w)
	}
	p := pool.New(targets, 0)
	defer p.Stop()
	h := &handler{}
	h.SetPool(p)

	const cycles = 5
	total := 0
	for _, w := range weights {
		total += w
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	for range cycles * total {
		h.ServeHTTP(w, r)
	}
	for i, want := range weights {
		if handlers[i].hits != cycles*want {
			t.Errorf("handler %d: expected %d hits, got %d",
				i, cycles*want, handlers[i].hits)
		}
	}
}

// TestUniformWeightsUseRotation verifies the unweighted fast path still
// distributes evenly.
func TestUniformWeightsUseRotation(t *testing.T) {
	handlers := make([]*countingHandler, 3)
	targets := make(pool.Targets, 3)
	for i := range handlers {
		handlers[i] = &countingHandler{}
		targets[i] = pool.NewTarget(handlers[i], passingStatus(), nil)
	}
	p := pool.New(targets, 0)
	defer p.Stop()
	h := &handler{}
	h.SetPool(p)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	for range 30 {
		h.ServeHTTP(w, r)
	}
	for i := range handlers {
		if handlers[i].hits != 10 {
			t.Errorf("handler %d: expected 10 hits, got %d", i, handlers[i].hits)
		}
	}
}
