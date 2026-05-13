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

	h.SetPool(p)

	hsts[0].Set(0)
	time.Sleep(250 * time.Millisecond)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, nil)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}

	var p2 pool.Pool
	p2, _, hsts = albpool.New(0,
		[]http.Handler{http.HandlerFunc(failures.HandleBadGateway)})
	h.SetPool(p2)
	hsts[0].Set(-1)
	time.Sleep(250 * time.Millisecond)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, nil)
	if w.Code != http.StatusBadGateway {
		t.Error("expected 502 got", w.Code)
	}
}

func TestNextTarget(t *testing.T) {
	p := pool.New(nil, -1)
	h := &handler{}
	h.SetPool(p)
	h.StopPool()
	p.SetHealthy([]http.Handler{http.NotFoundHandler()})
	n := h.nextTarget(p)
	if n == nil {
		t.Error("expected non-nil target")
	}
}

func TestRoundRobinProgression(t *testing.T) {
	h0 := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("0"))
	})
	h1 := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("1"))
	})
	h2 := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("2"))
	})

	p, _, st := albpool.New(-1, []http.Handler{h0, h1, h2})
	st[0].Set(0)
	st[1].Set(0)
	st[2].Set(0)
	time.Sleep(250 * time.Millisecond)

	rr := &handler{}
	rr.SetPool(p)

	// Fire 6 requests and verify rotation through all 3 backends.
	// Each backend must appear exactly twice.
	seen := make([]string, 6)
	for i := range 6 {
		w := httptest.NewRecorder()
		rr.ServeHTTP(w, nil)
		seen[i] = w.Body.String()
	}

	counts := map[string]int{}
	for _, s := range seen {
		counts[s]++
	}
	for _, id := range []string{"0", "1", "2"} {
		if counts[id] != 2 {
			t.Errorf("backend %s called %d times (expected 2); sequence: %v",
				id, counts[id], seen)
		}
	}
}
