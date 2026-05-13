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

package tsm

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// A panicking pool member must not crash the request. RecoverFanoutPanic("tsm",
// ...) at time_series_merge.go must catch it and mark the slot failed so the
// merge surfaces the partial-failure (phit) signal.
func TestTSMPanicMemberDoesNotCrashRequest(t *testing.T) {
	panicker := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("simulated upstream nil deref")
	})

	p, _, st := albpool.New(-1, []http.Handler{
		http.HandlerFunc(tu.BasicHTTPHandler),
		panicker,
	})
	st[0].Set(healthcheck.StatusPassing)
	st[1].Set(healthcheck.StatusPassing)
	time.Sleep(250 * time.Millisecond)

	rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
	rsc.IsMergeMember = true
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
	r = request.SetResources(r, rsc)

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)
	w := httptest.NewRecorder()

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("unrecovered panic crossed ServeHTTP: %v", rec)
		}
	}()

	done := make(chan struct{})
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Errorf("unrecovered panic in goroutine: %v", rec)
			}
			close(done)
		}()
		h.ServeHTTP(w, r)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return after panic")
	}
}

func TestTSMPanicAllMembersDoesNotCrashRequest(t *testing.T) {
	panicker := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("simulated upstream nil deref")
	})

	p, _, st := albpool.New(-1, []http.Handler{panicker, panicker})
	st[0].Set(healthcheck.StatusPassing)
	st[1].Set(healthcheck.StatusPassing)
	time.Sleep(250 * time.Millisecond)

	rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
	rsc.IsMergeMember = true
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
	r = request.SetResources(r, rsc)

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)
	w := httptest.NewRecorder()

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("unrecovered panic crossed ServeHTTP: %v", rec)
		}
	}()

	done := make(chan struct{})
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Errorf("unrecovered panic in goroutine: %v", rec)
			}
			close(done)
		}()
		h.ServeHTTP(w, r)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return after all-panic fanout")
	}
}
