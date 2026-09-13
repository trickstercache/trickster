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

package engines

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func newBareRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "http://example.com/a", nil)
}

// a replacement response must establish its own permission for shared storage;
// inheriting it would put an authenticated body under the anonymous key
func TestMergeDoesNotInheritShareability(t *testing.T) {
	cp := &CachingPolicy{IsShareable: true}
	cp.Merge(&CachingPolicy{IsShareable: false})
	if cp.IsShareable {
		t.Error("expected a replacement response to establish its own shareability")
	}
	cp = &CachingPolicy{IsShareable: false}
	cp.Merge(&CachingPolicy{IsShareable: true})
	if !cp.IsShareable {
		t.Error("expected the replacement's own shareability to apply")
	}
}

// RFC 9111 4.2.4 bars serving stale under must-revalidate, proxy-revalidate or
// an applicable s-maxage, whatever the RFC 5861 windows say
func TestNoStaleServingBlocksBothStalePaths(t *testing.T) {
	now := staleNow
	for _, name := range []string{"while-revalidate", "if-error"} {
		t.Run(name, func(t *testing.T) {
			cp := agedPolicy(10, 5, 300, 300)
			if name == "while-revalidate" && !cp.CanServeStaleWhileRevalidate(now) {
				t.Fatal("expected the window to allow stale serving first")
			}
			cp.NoStaleServing = true
			if cp.CanServeStaleWhileRevalidate(now) || cp.CanServeStaleOnError(now) {
				t.Error("expected the prohibition to override the window")
			}
			// the lifetime itself must survive; zeroing it is not the mechanism
			if cp.FreshnessLifetime != 10 {
				t.Errorf("freshness lifetime changed to %d", cp.FreshnessLifetime)
			}
		})
	}
}

func TestResponseDirectivesSetNoStaleServing(t *testing.T) {
	tests := []struct {
		cc       string
		expected bool
	}{
		{"max-age=60, stale-while-revalidate=300", false},
		{"max-age=60, must-revalidate", true},
		{"max-age=60, proxy-revalidate", true},
		{"s-maxage=60", true},
		{"s-maxage=1, stale-while-revalidate=300", true},
	}
	for _, test := range tests {
		t.Run(test.cc, func(t *testing.T) {
			h := http.Header{}
			h.Set(headers.NameCacheControl, test.cc)
			cp := GetResponseCachingPolicy(200, nil, h)
			if cp.NoStaleServing != test.expected {
				t.Errorf("got %t expected %t", cp.NoStaleServing, test.expected)
			}
		})
	}
}

// the background refresh mutates the policy on its own goroutine, so the clone
// must not share it with the request still serving the stale body
func TestProxyRequestCloneCopiesCachingPolicy(t *testing.T) {
	pr := &proxyRequest{
		Request:       newBareRequest(),
		cachingPolicy: &CachingPolicy{ETag: "original", FreshnessLifetime: 10},
	}
	c := pr.Clone()
	if c.cachingPolicy == pr.cachingPolicy {
		t.Fatal("expected the clone to have its own policy")
	}
	c.cachingPolicy.ETag = "replacement"
	if pr.cachingPolicy.ETag != "original" {
		t.Error("mutating the clone's policy changed the original")
	}
	// a nil policy must still clone cleanly
	if (&proxyRequest{Request: newBareRequest()}).Clone().cachingPolicy != nil {
		t.Error("expected a nil policy to stay nil")
	}
}

// a leader's response answers only requests selecting the same variant
func TestOPCResultSuitableFor(t *testing.T) {
	names := []string{"X-Mecone-Select"}
	waiter := func(v string) *proxyRequest {
		h := http.Header{}
		if v != "" {
			h.Set("X-Mecone-Select", v)
		}
		return &proxyRequest{Request: &http.Request{Header: h}, primaryKey: "primary"}
	}
	leader := waiter("alpha")
	shared := &opcResult{
		varyNames:      names,
		varyGeneration: "gen1",
		varyKey:        varySecondaryKey("primary", "gen1", names, leader.Header),
	}
	if !shared.suitableFor(leader) {
		t.Error("expected the leader's own selection to be suitable")
	}
	if shared.suitableFor(waiter("beta")) {
		t.Error("expected a different selection to be unsuitable")
	}
	// absent is its own selection, not a match for a present value
	if shared.suitableFor(waiter("")) {
		t.Error("expected an absent field to be unsuitable")
	}
	// a response that nominated nothing answers everyone
	if !(&opcResult{}).suitableFor(waiter("beta")) {
		t.Error("expected a non-varying result to be suitable")
	}
	var nilResult *opcResult
	if !nilResult.suitableFor(waiter("beta")) {
		t.Error("expected a nil result to be treated as suitable")
	}
}

// If-Range is compared against the validator of what is actually stored
func TestIfRangeMatchesDocument(t *testing.T) {
	doc := &HTTPDocument{CachingPolicy: &CachingPolicy{ETag: `"current"`}}
	pr := &proxyRequest{
		cacheDocument: doc,
		cachingPolicy: &CachingPolicy{IfRangeValue: `"current"`},
	}
	if !pr.ifRangeMatchesDocument() {
		t.Error("expected a matching validator")
	}
	pr.cachingPolicy = &CachingPolicy{IfRangeValue: `"obsolete"`}
	if pr.ifRangeMatchesDocument() {
		t.Error("expected a stale validator not to match")
	}
	// nothing stored means nothing to match against
	if (&proxyRequest{cachingPolicy: &CachingPolicy{IfRangeValue: `"x"`}}).ifRangeMatchesDocument() {
		t.Error("expected false with no stored document")
	}
	if (&proxyRequest{cacheDocument: doc}).ifRangeMatchesDocument() {
		t.Error("expected false with no request policy")
	}
	// a date validator has to match the stored Last-Modified exactly
	lm := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	pr = &proxyRequest{
		cacheDocument: &HTTPDocument{CachingPolicy: &CachingPolicy{LastModified: lm}},
		cachingPolicy: &CachingPolicy{IfRangeValue: lm.Format(time.RFC1123)},
	}
	if !pr.ifRangeMatchesDocument() {
		t.Error("expected a matching date validator")
	}
}

const testCredential = "Basic bWVjb25lOnRlc3Q="

// an authenticated miss must stay keyed to its credential; probing the shared
// key must not leave the request pointed at the anonymous one, or a negative
// cache entry lands where anyone could read it
func TestAuthenticatedNegativeResponseIsNotShared(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("private absence"))
	}))
	defer ts.Close()

	rsc, url, done := originResources(t, ts, "/authneg")
	defer done()
	// a configured negative cache is what makes the 404 storable at all
	rsc.BackendOptions.NegativeCache = map[int]time.Duration{
		http.StatusNotFound: time.Hour,
	}

	if code, _ := runOPCWith(rsc, http.MethodGet, url,
		map[string]string{headers.NameAuthorization: testCredential}); code != http.StatusNotFound {
		t.Fatalf("got %d expected 404", code)
	}
	before := hits.Load()
	// an anonymous request must not be answered from the authenticated miss
	if code, _ := runOPCWith(rsc, http.MethodGet, url, nil); code != http.StatusNotFound {
		t.Fatalf("got %d expected 404", code)
	}
	if hits.Load() == before {
		t.Error("the anonymous request was served the authenticated negative response")
	}
}

// a successful authenticated write supersedes the shared copy too
func TestAuthenticatedWriteInvalidatesSharedResponse(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		n := hits.Add(1)
		w.Header().Set(headers.NameCacheControl, "public, max-age=3600")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "body-%d", n)
	}))
	defer ts.Close()

	rsc, url, done := originResources(t, ts, "/authwrite")
	defer done()
	auth := map[string]string{headers.NameAuthorization: testCredential}

	if _, body := runOPCWith(rsc, http.MethodGet, url, auth); body != "body-1" {
		t.Fatalf("got %s", body)
	}
	// stored under the credential-free key, so an anonymous read reuses it
	if _, body := runOPCWith(rsc, http.MethodGet, url, nil); body != "body-1" {
		t.Fatalf("got %s expected the shared copy", body)
	}
	if code, _ := runOPCWith(rsc, http.MethodPut, url, auth); code != http.StatusNoContent {
		t.Fatalf("got %d", code)
	}
	if _, body := runOPCWith(rsc, http.MethodGet, url, nil); body == "body-1" {
		t.Error("the authenticated write left the shared representation cached")
	}
}

// invalidation has to reach every variant, not just the index that points at them
func TestInvalidationRemovesEveryVaryVariant(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		n := hits.Add(1)
		w.Header().Set(headers.NameCacheControl, "max-age=3600")
		w.Header().Set(headers.NameVary, "X-Mecone-Select")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "%s-%d", r.Header.Get("X-Mecone-Select"), n)
	}))
	defer ts.Close()

	rsc, url, done := originResources(t, ts, "/variants")
	defer done()
	alpha := map[string]string{"X-Mecone-Select": "alpha"}
	beta := map[string]string{"X-Mecone-Select": "beta"}

	_, alphaOld := runOPCWith(rsc, http.MethodGet, url, alpha)
	_, betaOld := runOPCWith(rsc, http.MethodGet, url, beta)
	if alphaOld == betaOld {
		t.Fatal("expected the two selections to get their own representations")
	}
	if code, _ := runOPCWith(rsc, http.MethodPut, url, nil); code != http.StatusNoContent {
		t.Fatal("expected the write to reach the origin")
	}
	// refreshing alpha recreates the index; beta must not come back with it
	if _, body := runOPCWith(rsc, http.MethodGet, url, alpha); body == alphaOld {
		t.Error("alpha survived the invalidation")
	}
	if _, body := runOPCWith(rsc, http.MethodGet, url, beta); body == betaOld {
		t.Error("beta became reachable again after the index was recreated")
	}
}

// a serialized cache provider must carry the prohibition across a round trip,
// or a response governed by must-revalidate can be served stale after retrieval
func TestCachingPolicySerializationPreservesStaleRules(t *testing.T) {
	cp := &CachingPolicy{
		FreshnessLifetime:    60,
		StaleWhileRevalidate: 300,
		StaleIfError:         600,
		InitialAge:           12,
		NoStaleServing:       true,
		ETag:                 `"v1"`,
	}
	b, err := cp.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	out := &CachingPolicy{}
	if _, err = out.UnmarshalMsg(b); err != nil {
		t.Fatal(err)
	}
	if !out.NoStaleServing {
		t.Error("NoStaleServing did not survive serialization")
	}
	if out.StaleWhileRevalidate != 300 || out.StaleIfError != 600 || out.InitialAge != 12 {
		t.Errorf("stale windows or age did not survive: %+v", out)
	}
	// the prohibition has to still bite after retrieval
	out.LocalDate = time.Now().Add(-90 * time.Second)
	if out.CanServeStaleWhileRevalidate(time.Now()) || out.CanServeStaleOnError(time.Now()) {
		t.Error("a deserialized policy served stale despite the prohibition")
	}
}

// a serialized variant index must carry its generation, or the key derived
// after retrieval cannot find the variant that was stored
func TestDocumentSerializationPreservesVaryIndex(t *testing.T) {
	names := []string{"X-Mecone-Select"}
	gen := newVaryGeneration()
	d := &HTTPDocument{VaryNames: names, VaryGeneration: gen}
	b, err := d.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	out := &HTTPDocument{}
	if _, err = out.UnmarshalMsg(b); err != nil {
		t.Fatal(err)
	}
	if out.VaryGeneration != gen {
		t.Errorf("VaryGeneration got %q expected %q", out.VaryGeneration, gen)
	}
	if len(out.VaryNames) != 1 || out.VaryNames[0] != names[0] {
		t.Errorf("VaryNames got %v", out.VaryNames)
	}
	// the whole point of persisting it: the same selection resolves to the
	// same secondary key on the far side of the round trip
	h := http.Header{"X-Mecone-Select": []string{"alpha"}}
	if varySecondaryKey("primary", out.VaryGeneration, out.VaryNames, h) !=
		varySecondaryKey("primary", gen, names, h) {
		t.Error("a retrieved index derived a key that cannot find its variant")
	}
}

// Vary: * matches no later request, which an empty name list cannot express
// because a response with no Vary at all also has none
func TestOPCResultUnmatchableIsNeverSuitable(t *testing.T) {
	pr := &proxyRequest{Request: newBareRequest(), primaryKey: "primary"}
	if (&opcResult{varyUnmatchable: true}).suitableFor(pr) {
		t.Error("expected an unmatchable result to be refused")
	}
	if !(&opcResult{}).suitableFor(pr) {
		t.Error("expected a result with no Vary to be suitable")
	}
}
