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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func testPool(mech pool.Mechanism, healthyFloor int, hs []http.Handler) (pool.Pool,
	[]*pool.Target, []*healthcheck.Status) {
	var targets []*pool.Target
	var statuses []*healthcheck.Status
	if len(hs) > 0 {
		targets = make([]*pool.Target, 0, len(hs))
		statuses = make([]*healthcheck.Status, 0, len(hs))
		for _, h := range hs {
			hst := &healthcheck.Status{}
			statuses = append(statuses, hst)
			targets = append(targets, pool.NewTarget(h, hst))
		}
	}
	pool := pool.New(mech, targets, healthyFloor)
	return pool, targets, statuses
}

func TestHandleRoundRobin(t *testing.T) {

	w := httptest.NewRecorder()
	c := &Client{}
	c.handleRoundRobin(w, nil)
	if w.Code != http.StatusBadGateway {
		t.Error("expected 502 got", w.Code)
	}

	p, _, hsts := testPool(pool.RoundRobin, 0,
		[]http.Handler{http.HandlerFunc(tu.BasicHTTPHandler)})

	c.pool = p

	hsts[0].Set(0)
	time.Sleep(250 * time.Millisecond)

	w = httptest.NewRecorder()
	c.handleRoundRobin(w, nil)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}

	c.pool, _, hsts = testPool(pool.RoundRobin, 0,
		[]http.Handler{http.HandlerFunc(handlers.HandleBadGateway)})
	hsts[0].Set(-1)
	time.Sleep(250 * time.Millisecond)
	w = httptest.NewRecorder()
	c.handleRoundRobin(w, nil)
	if w.Code != http.StatusBadGateway {
		t.Error("expected 502 got", w.Code)
	}

}
