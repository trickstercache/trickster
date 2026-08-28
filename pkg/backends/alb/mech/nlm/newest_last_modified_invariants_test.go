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

package nlm

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// TestNLMInvariants consolidates the prior panic, truncated, fallback, and
// post_race tests. Subtests run sequentially because they share Prometheus
// metric vectors and the package-level mech registry.
func TestNLMInvariants(t *testing.T) {
	t.Run("panic_member_serves_healthy_body", testNLMPanicMember)
	t.Run("panic_all_members_returns_5xx", testNLMPanicAllMembers)
	t.Run("truncated_member_is_disqualified", testNLMSkipsTruncated)
	t.Run("all_truncated_returns_502", testNLMAllTruncated)
	t.Run("all_failed_returns_502", testNLMAllFailed)
	t.Run("post_body_fanout_is_race_free", testNLMPostBodyRaceFree)
}

func testNLMPanicMember(t *testing.T) {
	const lm = "Sun, 06 Nov 1994 08:49:37 GMT"
	healthy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameLastModified, lm)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("body-ok"))
	})

	p, _, _ := albpool.NewHealthy([]http.Handler{albpool.PanicHandler(), healthy})
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	h := &handler{}
	h.SetPool(p)
	w := httptest.NewRecorder()
	albpool.ServeAndWait(t, h, w, albpool.NewParentGET(t))

	if w.Body.String() != "body-ok" {
		t.Errorf("expected healthy member body, got %q", w.Body.String())
	}
}

func testNLMPanicAllMembers(t *testing.T) {
	p, _, _ := albpool.NewHealthy([]http.Handler{albpool.PanicHandler(), albpool.PanicHandler()})
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	h := &handler{}
	h.SetPool(p)
	w := httptest.NewRecorder()
	albpool.RequireFanoutFailureDelta(t, "nlm", "", "panic", 2, func() {
		albpool.ServeAndWait(t, h, w, albpool.NewParentGET(t))
	})
	if w.Code < 500 {
		t.Errorf("expected 5xx, got %d", w.Code)
	}
}

func testNLMSkipsTruncated(t *testing.T) {
	older := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	const smallBody = "ok"
	bigBody := strings.Repeat("X", 4096)

	smallH := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameLastModified, older.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(smallBody))
	})
	bigH := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameLastModified, newer.UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(bigBody))
	})

	p, _, _ := albpool.NewHealthy([]http.Handler{smallH, bigH})
	defer p.Stop()
	p.RefreshHealthy()
	albpool.WaitHealthy(t, p, 2)

	h := &handler{maxCaptureBytes: 10}
	h.SetPool(p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, albpool.NewParentGET(t))

	if got := w.Body.String(); got != smallBody {
		t.Fatalf("expected truncated upstream disqualified; want body %q, got %q (len=%d)",
			smallBody, got, len(got))
	}
	if cl := w.Header().Get("Content-Length"); cl != "" && cl != "2" {
		t.Errorf("served Content-Length %q does not match served body length %d", cl, w.Body.Len())
	}
}

func testNLMAllTruncated(t *testing.T) {
	const maxBytes = 8
	const bodySize = 4096

	oversized := func() http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			body := make([]byte, bodySize)
			for i := range body {
				body[i] = 'a'
			}
			w.Header().Set("Content-Length", strconv.Itoa(bodySize))
			w.WriteHeader(http.StatusOK)
			w.Write(body)
		})
	}

	hs := []http.Handler{oversized(), oversized(), oversized()}
	p, _, _ := albpool.NewHealthy(hs)
	defer p.Stop()
	p.SetHealthy(hs)

	h := &handler{maxCaptureBytes: maxBytes}
	h.SetPool(p)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, albpool.NewParentGET(t))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when all members are truncated, got %d (body len=%d)",
			w.Code, w.Body.Len())
	}
}

func testNLMAllFailed(t *testing.T) {
	hs := make([]http.Handler, 3)
	for i := range hs {
		hs[i] = http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			panic("upstream blew up")
		})
	}

	p, _, _ := albpool.NewHealthy(hs)
	defer p.Stop()
	p.SetHealthy(hs)

	h := &handler{}
	h.SetPool(p)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, albpool.NewParentGET(t))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when all members fail, got %d (body len=%d)",
			w.Code, w.Body.Len())
	}
}

// NLM fanout clones the parent request N ways. For POST/PUT/PATCH the body
// must be primed before fanout, else N goroutines race on r.Body and
// rsc.RequestBody inside GetBody. Run under -race to catch any regression.
func testNLMPostBodyRaceFree(t *testing.T) {
	const body = `{"query":"sum(rate(metric[5m]))","start":"2024-01-01T00:00:00Z","end":"2024-01-01T01:00:00Z","step":"15s"}`
	const lm = "Sun, 06 Nov 1994 08:49:37 GMT"
	albpool.RunPostBodyFanoutRace(t, func(p pool.Pool) http.Handler {
		h := &handler{}
		h.SetPool(p)
		return h
	}, body, 4, 16, func(w http.ResponseWriter) {
		w.Header().Set(headers.NameLastModified, lm)
	})
}
