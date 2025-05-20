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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

func TestHandleRoundRobin(t *testing.T) {

	w := httptest.NewRecorder()
	h := &handler{}
	h.ServeHTTP(w, nil)
	if w.Code != http.StatusBadGateway {
		t.Error("expected 502 got", w.Code)
	}

	p, _, hsts := albpool.New(0,
		[]http.Handler{http.HandlerFunc(tu.BasicHTTPHandler)})

	h.pool = p

	hsts[0].Set(0)
	time.Sleep(250 * time.Millisecond)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, nil)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}

	h.pool, _, hsts = albpool.New(0,
		[]http.Handler{http.HandlerFunc(failures.HandleBadGateway)})
	hsts[0].Set(-1)
	time.Sleep(250 * time.Millisecond)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, nil)
	if w.Code != http.StatusBadGateway {
		t.Error("expected 502 got", w.Code)
	}

}

func TestNextTarget(t *testing.T) {
	h := &handler{
		pool: pool.New(nil, -1),
	}
	h.StopPool()
	h.pool.SetHealthy([]http.Handler{http.NotFoundHandler()})
	n := h.nextTarget()
	if n == nil {
		t.Error("expected non-nil target")
	}
}
