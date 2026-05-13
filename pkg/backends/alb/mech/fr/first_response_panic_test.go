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
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// A panicking pool member must not crash the request or process. errgroup.Go
// does not recover from panics; the panic propagates through eg.Wait() and
// kills the test goroutine running ServeHTTP.
func TestFRPanicMemberDoesNotCrashRequest(t *testing.T) {
	healthy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("body-ok"))
	})
	panicker := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("simulated upstream nil deref")
	})

	p, _, st := albpool.New(-1, []http.Handler{healthy, panicker})
	st[0].Set(healthcheck.StatusPassing)
	st[1].Set(healthcheck.StatusPassing)
	time.Sleep(250 * time.Millisecond)

	h := &handler{}
	h.SetPool(p)
	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("unrecovered panic crossed ServeHTTP boundary: %v", rec)
		}
	}()

	done := make(chan struct{})
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Errorf("unrecovered panic in ServeHTTP goroutine: %v", rec)
			}
			close(done)
		}()
		h.ServeHTTP(w, r)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return within 5s after pool member panic")
	}

	// either outcome is acceptable: 200 from the healthy member, or a 5xx if
	// the mech surfaces the failure as a gateway error. the bar is no panic.
	if w.Code != http.StatusOK && w.Code < 500 {
		t.Errorf("expected 200 or 5xx got %d", w.Code)
	}
}

// All members panic: ServeHTTP must still return (likely 502) without
// propagating the panic.
func TestFRPanicAllMembersDoesNotCrashRequest(t *testing.T) {
	panicker := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("simulated upstream nil deref")
	})

	p, _, st := albpool.New(-1, []http.Handler{panicker, panicker})
	st[0].Set(healthcheck.StatusPassing)
	st[1].Set(healthcheck.StatusPassing)
	time.Sleep(250 * time.Millisecond)

	h := &handler{}
	h.SetPool(p)
	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("unrecovered panic crossed ServeHTTP boundary: %v", rec)
		}
	}()

	done := make(chan struct{})
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Errorf("unrecovered panic in ServeHTTP goroutine: %v", rec)
			}
			close(done)
		}()
		h.ServeHTTP(w, r)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return within 5s after all members panicked")
	}
}
