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

package alb

import (
	"net/http"
	"net/http/httptest"
	"testing"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
)

type countingHandler struct{ hits int }

func (c *countingHandler) ServeHTTP(_ http.ResponseWriter, _ *http.Request) {
	c.hits++
}

func passingStatus() *healthcheck.Status {
	st := &healthcheck.Status{}
	st.Set(healthcheck.StatusPassing)
	return st
}

func newRRALB(t *testing.T, name string) *Client {
	t.Helper()
	o := bo.New()
	o.Provider = providers.ALB
	o.ALBOptions = &ao.Options{MechanismName: "rr"}
	cl, err := NewClient(name, o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cl.(*Client)
}

func TestSetDynamicTargets(t *testing.T) {
	c := newRRALB(t, "test-alb")

	h1 := &countingHandler{}
	target := pool.NewWeightedTarget(h1, passingStatus(), nil, 1)
	if !c.SetDynamicTargets(pool.Targets{target}) {
		t.Fatal("expected swap to be accepted")
	}

	// requests dispatch to the swapped-in member with no restart
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	c.Handlers()[providers.ALB].ServeHTTP(w, r)
	if h1.hits != 1 {
		t.Fatalf("expected 1 hit on dynamic member, got %d", h1.hits)
	}

	// a second swap replaces the membership entirely
	h2 := &countingHandler{}
	if !c.SetDynamicTargets(pool.Targets{
		pool.NewWeightedTarget(h2, passingStatus(), nil, 1)}) {
		t.Fatal("expected second swap to be accepted")
	}
	c.Handlers()[providers.ALB].ServeHTTP(w, r)
	if h1.hits != 1 || h2.hits != 1 {
		t.Fatalf("expected dispatch to move to new member; got h1=%d h2=%d",
			h1.hits, h2.hits)
	}

	names := c.DynamicPoolNames()
	if len(names) != 1 {
		t.Fatalf("expected 1 dynamic pool name, got %d", len(names))
	}

	// swaps after StopPool are rejected
	c.StopPool()
	if c.SetDynamicTargets(pool.Targets{target}) {
		t.Fatal("expected swap after StopPool to be rejected")
	}
}
