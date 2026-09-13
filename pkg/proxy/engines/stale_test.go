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
	"net/http"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/cachecontrol"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func parseReqDirectives(v string) *cachecontrol.RequestDirectives {
	h := http.Header{}
	h.Set(headers.NameCacheControl, v)
	return cachecontrol.ParseRequest(h)
}

// staleNow anchors the aged policies below so second truncation in CurrentAge
// cannot make a boundary case flap
var staleNow = time.Now()

// agedPolicy returns a policy that became stale staleFor seconds before staleNow
func agedPolicy(lifetime, staleFor, staleWhileRev, staleIfErr int) *CachingPolicy {
	return &CachingPolicy{
		FreshnessLifetime:    lifetime,
		StaleWhileRevalidate: staleWhileRev,
		StaleIfError:         staleIfErr,
		LocalDate:            staleNow.Add(-time.Duration(lifetime+staleFor) * time.Second),
	}
}

func TestCanServeStaleWhileRevalidate(t *testing.T) {
	now := staleNow
	tests := []struct {
		name     string
		cp       *CachingPolicy
		expected bool
	}{
		{"nil policy", nil, false},
		{"no window", agedPolicy(10, 5, 0, 0), false},
		{"still fresh", agedPolicy(10, -5, 60, 0), false},
		{"inside the window", agedPolicy(10, 5, 60, 0), true},
		{"just stale", agedPolicy(10, 0, 60, 0), true},
		{"past the window", agedPolicy(10, 90, 60, 0), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.cp.CanServeStaleWhileRevalidate(now); got != test.expected {
				t.Errorf("got %t expected %t", got, test.expected)
			}
		})
	}
}

func TestCanServeStaleOnError(t *testing.T) {
	now := staleNow
	if (&CachingPolicy{}).CanServeStaleOnError(now) {
		t.Error("expected no stale serving without a window")
	}
	if !agedPolicy(10, 5, 0, 60).CanServeStaleOnError(now) {
		t.Error("expected stale serving inside the window")
	}
	if agedPolicy(10, 90, 0, 60).CanServeStaleOnError(now) {
		t.Error("expected no stale serving past the window")
	}
}

func TestServeStaleOnError(t *testing.T) {
	doc := func(staleIfErr int) *HTTPDocument {
		return &HTTPDocument{CachingPolicy: agedPolicy(10, 5, 0, staleIfErr)}
	}
	tests := []struct {
		name     string
		doc      *HTTPDocument
		status   int
		expected bool
	}{
		{"no stored document", nil, 503, false},
		{"server error inside window", doc(60), 503, true},
		{"gateway error inside window", doc(60), 502, true},
		// RFC 5861 4 covers an origin that could not answer, not one that did
		{"not found is an answer", doc(60), 404, false},
		{"success is an answer", doc(60), 200, false},
		{"no window configured", doc(0), 503, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pr := &proxyRequest{
				cacheDocument:    test.doc,
				upstreamResponse: &http.Response{StatusCode: test.status},
			}
			if got := serveStaleOnError(pr); got != test.expected {
				t.Errorf("got %t expected %t", got, test.expected)
			}
		})
	}
	// no upstream response at all
	if serveStaleOnError(&proxyRequest{cacheDocument: doc(60)}) {
		t.Error("expected false with no upstream response")
	}
}

// retention has to outlive freshness or the validator is gone by the time it
// would save a body transfer
func TestTTLOutlivesFreshness(t *testing.T) {
	maxTTL := 24 * time.Hour
	revalidatable := &CachingPolicy{FreshnessLifetime: 1, CanRevalidate: true}
	if got := revalidatable.TTL(2, maxTTL); got <= time.Second {
		t.Errorf("a revalidatable object needs a window past freshness, got %s", got)
	}
	// the stale windows extend retention when they reach further
	staleWhileRev := &CachingPolicy{FreshnessLifetime: 1, StaleWhileRevalidate: 600}
	if got := staleWhileRev.TTL(2, maxTTL); got < 601*time.Second {
		t.Errorf("got %s expected retention through the stale window", got)
	}
	staleIfErr := &CachingPolicy{FreshnessLifetime: 1, StaleIfError: 600}
	if got := staleIfErr.TTL(2, maxTTL); got < 601*time.Second {
		t.Errorf("got %s expected retention through the stale window", got)
	}
	// a plain object is kept for its lifetime and no longer
	plain := &CachingPolicy{FreshnessLifetime: 60}
	if got := plain.TTL(2, maxTTL); got != 60*time.Second {
		t.Errorf("got %s expected 60s", got)
	}
	// MaxTTL still caps everything
	if got := (&CachingPolicy{FreshnessLifetime: 100000}).TTL(2, time.Minute); got != time.Minute {
		t.Errorf("got %s expected the max to cap retention", got)
	}
}

func TestClientAcceptsStored(t *testing.T) {
	now := time.Now()
	withAge := func(age int) *CachingPolicy {
		return &CachingPolicy{FreshnessLifetime: 3600, LocalDate: now.Add(-time.Duration(age) * time.Second)}
	}
	// no directives means the cache's own judgement stands
	if !withAge(10).clientAcceptsStored(now) {
		t.Error("expected acceptance with no client directives")
	}
	// max-age=0 rules out anything that has aged at all
	cp := withAge(10)
	cp.ClientDirectives = parseReqDirectives("max-age=0")
	if cp.clientAcceptsStored(now) {
		t.Error("expected max-age=0 to rule out an aged response")
	}
	cp = withAge(10)
	cp.ClientDirectives = parseReqDirectives("max-age=60")
	if !cp.clientAcceptsStored(now) {
		t.Error("expected a response inside the client's max-age to be accepted")
	}
	// min-fresh demands more lifetime than remains
	cp = withAge(10)
	cp.ClientDirectives = parseReqDirectives("min-fresh=7200")
	if cp.clientAcceptsStored(now) {
		t.Error("expected min-fresh to rule out too little remaining freshness")
	}
	cp = withAge(10)
	cp.ClientDirectives = parseReqDirectives("min-fresh=60")
	if !cp.clientAcceptsStored(now) {
		t.Error("expected enough remaining freshness to be accepted")
	}
}
