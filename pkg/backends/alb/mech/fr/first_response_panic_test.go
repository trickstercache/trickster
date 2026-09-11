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

	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// A panicking pool member must not crash the request or process. errgroup.Go
// does not recover from panics; the panic propagates through eg.Wait() and
// kills the test goroutine running ServeHTTP.
func TestFRPanicMemberDoesNotCrashRequest(t *testing.T) {
	p, _, _ := albpool.NewHealthy([]http.Handler{
		albpool.PanicHandler(),
		albpool.StatusHandler(http.StatusOK, "body-ok"),
	})
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	// Drain the panic before the winning response so its metric cannot leak into the next test.
	limit := 1
	h := &handler{}
	h.options.ConcurrencyOptions.QueryConcurrencyLimit = &limit
	h.SetPool(p)
	w := httptest.NewRecorder()
	albpool.RequireFanoutFailureDelta(t, "fr", "", "panic", 1, func() {
		albpool.ServeAndWait(t, h, w, albpool.NewParentGET(t))
	})

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, w.Code)
	}
}

// All members panic: ServeHTTP must still return (likely 502) without
// propagating the panic.
func TestFRPanicAllMembersDoesNotCrashRequest(t *testing.T) {
	p, _, _ := albpool.NewHealthy([]http.Handler{albpool.PanicHandler(), albpool.PanicHandler()})
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	h := &handler{}
	h.SetPool(p)
	w := httptest.NewRecorder()
	albpool.RequireFanoutFailureDelta(t, "fr", "", "panic", 2, func() {
		albpool.ServeAndWait(t, h, w, albpool.NewParentGET(t))
	})
	if w.Code < 500 {
		t.Errorf("expected 5xx, got %d", w.Code)
	}
}
