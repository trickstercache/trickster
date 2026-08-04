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
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// TestFRDisqualifiesTruncatedWinner asserts that FR (FGR variant) does not
// claim a member whose response exceeded MaxCaptureBytes. Both members emit
// FGR-qualifying status codes but with bodies past the cap; without the fix,
// the first one to finish wins the CAS and serves a truncated 200 body.
func TestFRDisqualifiesTruncatedWinner(t *testing.T) {
	const maxBytes = 16
	const bodySize = 1024

	hs := []http.Handler{
		albpool.SizedBodyHandler(http.StatusOK, bodySize),
		albpool.SizedBodyHandler(http.StatusOK, bodySize),
	}
	p, _, _ := albpool.New(-1, hs)
	defer p.Stop()
	p.SetHealthy(hs)

	h := &handler{
		fgr:             true,
		fgrCodes:        sets.New([]int{http.StatusOK}),
		maxCaptureBytes: maxBytes,
	}
	h.SetPool(p)

	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
	h.ServeHTTP(w, r)

	if w.Code == http.StatusOK {
		got := w.Body.Len()
		t.Fatalf("FR served truncated upstream as 200: body %d bytes vs Content-Length %s (cap %d)",
			got, w.Header().Get("Content-Length"), maxBytes)
	}
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 (no qualifying member), got %d", w.Code)
	}
}

// TestFRTruncatedAllMembersFallback covers the FR (non-FGR) flow with multiple
// members all producing truncated 200s. Before the fix, FR's CAS path serves
// the first truncated body. After, every member is disqualified and the
// fallback emits 502 rather than a truncated payload.
func TestFRTruncatedAllMembersFallback(t *testing.T) {
	const maxBytes = 16
	const bodySize = 1024

	hs := []http.Handler{
		albpool.SizedBodyHandler(http.StatusOK, bodySize),
		albpool.SizedBodyHandler(http.StatusOK, bodySize),
	}
	p, _, _ := albpool.New(-1, hs)
	defer p.Stop()
	p.SetHealthy(hs)

	h := &handler{maxCaptureBytes: maxBytes}
	h.SetPool(p)

	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
	h.ServeHTTP(w, r)

	if w.Code == http.StatusOK && w.Body.Len() < bodySize {
		t.Fatalf("FR served truncated 200: body %d bytes vs Content-Length %s (cap %d)",
			w.Body.Len(), w.Header().Get("Content-Length"), maxBytes)
	}
}

// TestFRPrefersIntactOverTruncated verifies that when one member's body fits
// under the cap and another exceeds it, FR picks the intact one (or 502s),
// never the truncated one.
func TestFRPrefersIntactOverTruncated(t *testing.T) {
	const maxBytes = 64

	hs := []http.Handler{
		albpool.SizedBodyHandler(http.StatusOK, 4096),
		albpool.StatusHandler(http.StatusOK, "ok"),
	}
	p, _, _ := albpool.New(-1, hs)
	defer p.Stop()
	p.SetHealthy(hs)

	h := &handler{
		fgr:             true,
		fgrCodes:        sets.New([]int{http.StatusOK}),
		maxCaptureBytes: maxBytes,
	}
	h.SetPool(p)

	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
	h.ServeHTTP(w, r)

	if w.Code == http.StatusOK && w.Body.String() != "ok" {
		t.Fatalf("FR served the truncated member instead of the intact one: body=%q len=%d",
			w.Body.String(), w.Body.Len())
	}
}
