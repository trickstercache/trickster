/*
 * Copyright 2026 The Trickster Authors
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

package handler

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func mustOptions(t *testing.T, name string, mutate func(*options.Options)) *options.Options {
	t.Helper()
	o := &options.Options{Name: name, Limit: 1, Window: timeconv.Duration(time.Minute)}
	if mutate != nil {
		mutate(o)
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	return o
}

func resultCount(name, result string) float64 {
	return testutil.ToFloat64(metrics.RateLimitDecisions.WithLabelValues(name, planeHTTP, result))
}

func serve(h http.Handler, method, path string, hdr http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, vs := range hdr {
		req.Header[k] = append([]string(nil), vs...)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

type sameHandler struct{ called bool }

func (s *sameHandler) ServeHTTP(http.ResponseWriter, *http.Request) { s.called = true }

func TestNilLimiterIsNext(t *testing.T) {
	next := &sameHandler{}
	if HTTP(nil, nil, next) != next {
		t.Fatal("nil route limiter")
	}
	if Listener(nil, "", false, next) != next {
		t.Fatal("nil listener limiter")
	}
	if HTTP(mustOptions(t, "nil-next", nil), nil, nil) != nil || Listener(mustOptions(t, "nil-next-l", nil), "", false, nil) != nil {
		t.Fatal("nil next")
	}
	ratelimit.Walk(func(name string, _ int) {
		if name == "nil-next" || name == "nil-next-l" {
			t.Fatalf("lookup %s", name)
		}
	})
}

func TestRejectAndHead(t *testing.T) {
	o := mustOptions(t, "reject-one", nil)
	var calls int
	h := HTTP(o, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	before := resultCount("reject-one", "limited")
	if w := serve(h, http.MethodGet, "/", nil); w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("first = %d calls=%d", w.Code, calls)
	}
	w := serve(h, http.MethodHead, "/", nil)
	if w.Code != http.StatusTooManyRequests || w.Body.Len() != 0 || calls != 1 {
		t.Fatalf("head = %d body=%q calls=%d", w.Code, w.Body.String(), calls)
	}
	if w.Header().Get(headers.NameCacheControl) != headers.ValueNoStore || w.Header().Get(headers.NameRetryAfter) == "" {
		t.Fatalf("headers %v", w.Header())
	}
	if got := resultCount("reject-one", "limited"); got != before+1 {
		t.Fatalf("limited = %v", got)
	}
	get := serve(h, http.MethodGet, "/", nil)
	if get.Body.String() != "Too Many Requests\n" {
		t.Fatalf("body %q", get.Body.String())
	}
}

func TestCountAppendsNames(t *testing.T) {
	outer := mustOptions(t, "count-outer", func(o *options.Options) { o.Action = options.ActionCount })
	inner := mustOptions(t, "count-inner", func(o *options.Options) { o.Action = options.ActionCount })
	var got []string
	h := HTTP(outer, nil, HTTP(inner, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append([]string(nil), r.Header.Values(headers.NameXTricksterRateLimited)...)
		w.WriteHeader(http.StatusOK)
	})))
	serve(h, http.MethodGet, "/", nil)
	if len(got) != 0 {
		t.Fatalf("allowed marker %v", got)
	}
	w := serve(h, http.MethodGet, "/", nil)
	joined := strings.Join(got, ",")
	if w.Code != http.StatusOK || !strings.Contains(joined, "count-outer") || !strings.Contains(joined, "count-inner") {
		t.Fatalf("count = %d marker %v", w.Code, got)
	}
}

func TestCountAppendsAndStrips(t *testing.T) {
	o := mustOptions(t, "count-one", func(o *options.Options) { o.Action = options.ActionCount })
	var got string
	inner := HTTP(o, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(headers.NameXTricksterRateLimited)
		w.WriteHeader(http.StatusOK)
	}))
	h := Listener(nil, "", true, inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(headers.NameXTricksterRateLimited, "spoofed")
	innerOnly := httptest.NewRecorder()
	// first event is allowed, so the marker is not written yet
	h.ServeHTTP(httptest.NewRecorder(), req)
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(headers.NameXTricksterRateLimited, "spoofed")
	h.ServeHTTP(innerOnly, req)
	if innerOnly.Code != http.StatusOK || got != "count-one" {
		t.Fatalf("count = %d marker %q", innerOnly.Code, got)
	}
	if resultCount("count-one", "counted") < 1 {
		t.Fatal("counted")
	}
}

func TestNestedIETFAndLegacy(t *testing.T) {
	outer := mustOptions(t, `out"er`, func(o *options.Options) {
		o.Limit = 10
		o.PolicyHeaders = options.PolicyIETF
	})
	innerIETF := mustOptions(t, "inner-ietf", func(o *options.Options) {
		o.Limit = 4
		o.PolicyHeaders = options.PolicyIETF
	})
	both := HTTP(outer, nil, HTTP(innerIETF, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})))
	w := serve(both, http.MethodGet, "/", nil)
	policies := w.Header().Values(headers.NameRateLimitPolicy)
	limits := w.Header().Values(headers.NameRateLimit)
	if len(policies) != 2 || len(limits) != 2 {
		t.Fatalf("nested IETF policy %v rate %v", policies, limits)
	}
	joined := strings.Join(append(policies, limits...), " ")
	if !strings.Contains(joined, `"out\"er"`) || !strings.Contains(joined, `"inner-ietf"`) {
		t.Fatalf("nested IETF values %s", joined)
	}

	inner := mustOptions(t, "inner", func(o *options.Options) {
		o.Limit = 3
		o.PolicyHeaders = options.PolicyLegacy
	})
	h := HTTP(outer, nil, HTTP(inner, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})))
	w = serve(h, http.MethodGet, "/", nil)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code)
	}
	if got := w.Header().Values(headers.NameRateLimit); len(got) != 1 || !strings.Contains(got[0], `"out\"er"`) {
		t.Fatalf("RateLimit %v", got)
	}
	if w.Header().Get(headers.NameXRateLimitLimit) != "3" {
		t.Fatalf("legacy %v", w.Header())
	}
	reset, err := strconv.ParseInt(w.Header().Get(headers.NameXRateLimitReset), 10, 64)
	if err != nil || reset < time.Now().Unix() {
		t.Fatalf("reset %q", w.Header().Get(headers.NameXRateLimitReset))
	}
	again := HTTP(mustOptions(t, "outer-legacy", func(o *options.Options) {
		o.Limit = 10
		o.PolicyHeaders = options.PolicyLegacy
	}), nil, HTTP(inner, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	w = serve(again, http.MethodGet, "/", nil)
	if w.Header().Get(headers.NameXRateLimitLimit) != "3" {
		t.Fatalf("inner legacy wins: %v", w.Header())
	}
}

func TestMissingKeyAndFull(t *testing.T) {
	exempt := mustOptions(t, "miss-exempt", func(o *options.Options) {
		o.Keys = []string{"header:X-Tenant"}
	})
	var stored int
	h := HTTP(exempt, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if w := serve(h, http.MethodGet, "/", nil); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	ratelimit.Walk(func(name string, n int) {
		if name == "miss-exempt" {
			stored = n
		}
	})
	if stored != 0 || resultCount("miss-exempt", "exempt") < 1 {
		t.Fatalf("stored %d", stored)
	}

	shared := mustOptions(t, "miss-shared", func(o *options.Options) {
		o.Keys = []string{"header:X-Tenant"}
		o.MissingKey = options.MissingShared
	})
	h = HTTP(shared, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	serve(h, http.MethodGet, "/", nil)
	if w := serve(h, http.MethodGet, "/", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("shared = %d", w.Code)
	}

	full := mustOptions(t, "full-reject", func(o *options.Options) {
		o.Keys = []string{"header:X-Tenant"}
		o.MaxKeys = 1
		o.MaxKeysAction = options.OnFullReject
		o.Limit = 10
	})
	h = HTTP(full, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	serve(h, http.MethodGet, "/", http.Header{"X-Tenant": {"a"}})
	if w := serve(h, http.MethodGet, "/", http.Header{"X-Tenant": {"b"}}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("full reject = %d", w.Code)
	}
	if resultCount("full-reject", "full") < 1 {
		t.Fatal("full metric")
	}

	var marked string
	counted := mustOptions(t, "full-count", func(o *options.Options) {
		o.Keys = []string{"header:X-Tenant"}
		o.MaxKeys = 1
		o.MaxKeysAction = options.OnFullReject
		o.Action = options.ActionCount
		o.Limit = 10
	})
	h = HTTP(counted, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marked = r.Header.Get(headers.NameXTricksterRateLimited)
		w.WriteHeader(http.StatusOK)
	}))
	serve(h, http.MethodGet, "/", http.Header{"X-Tenant": {"a"}})
	if w := serve(h, http.MethodGet, "/", http.Header{"X-Tenant": {"b"}}); w.Code != http.StatusOK || marked != "full-count" {
		t.Fatalf("full count = %d marker %q", w.Code, marked)
	}
}

func TestTrustedProxySeparateBuckets(t *testing.T) {
	o := mustOptions(t, "by-ip", func(o *options.Options) { o.Keys = []string{"client_ip"} })
	h := HTTP(o, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	trusted, err := clientip.ParseTrusted([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	h = clientip.Middleware(trusted, h)
	one := func(xff string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:9"
		req.Header.Set(headers.NameXForwardedFor, xff)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if one("192.0.2.1") != http.StatusNoContent || one("192.0.2.2") != http.StatusNoContent {
		t.Fatal("distinct clients")
	}
	if one("192.0.2.1") != http.StatusTooManyRequests {
		t.Fatal("same client")
	}
}

func TestReadinessExemptAndHealthCounted(t *testing.T) {
	o := mustOptions(t, "ready-edge", nil)
	var calls int
	h := Listener(o, "/trickster/ready", false, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	for range 3 {
		if w := serve(h, http.MethodGet, "/trickster/ready", nil); w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
	}
	if calls != 3 || resultCount("ready-edge", "exempt") < 3 {
		t.Fatalf("calls %d", calls)
	}
	if w := serve(h, http.MethodGet, "/trickster/health", nil); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	if w := serve(h, http.MethodGet, "/trickster/health", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("health = %d", w.Code)
	}
}

func TestRouteACLDoesNotConsume(t *testing.T) {
	o := mustOptions(t, "after-acl", nil)
	denied := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	// The route ACL sits outside the route limiter, so a denial never reaches it.
	acl := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		denied.ServeHTTP(w, r)
	})
	_ = HTTP(o, nil, denied)
	acl.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	ratelimit.Walk(func(name string, n int) {
		if name == "after-acl" && n != 0 {
			t.Fatalf("route quota %d", n)
		}
	})
	// A listener limiter has already judged before a route ACL denies.
	h := Listener(mustOptions(t, "before-acl", nil), "", false, denied)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	var n int
	ratelimit.Walk(func(name string, keys int) {
		if name == "before-acl" {
			n = keys
		}
	})
	if n != 1 {
		t.Fatalf("listener quota %d", n)
	}
}

func TestCachedResponsesCount(t *testing.T) {
	o := mustOptions(t, "cache-hit", func(o *options.Options) { o.Limit = 2 })
	h := HTTP(o, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	before := resultCount("cache-hit", "allowed")
	serve(h, http.MethodGet, "/", nil)
	serve(h, http.MethodGet, "/", nil)
	if got := resultCount("cache-hit", "allowed"); got != before+2 {
		t.Fatalf("allowed %v", got)
	}
	if w := serve(h, http.MethodGet, "/", nil); w.Code != http.StatusTooManyRequests {
		t.Fatal(w.Code)
	}
}

type customResponder struct{}

func (customResponder) WriteRateLimited(w http.ResponseWriter, _ *http.Request, _ ratelimit.Decision) {
	w.Header().Set("X-Custom", "1")
	w.WriteHeader(http.StatusTeapot)
}

func TestCustomResponder(t *testing.T) {
	h := HTTP(mustOptions(t, "custom-rl", nil), customResponder{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if w := serve(h, http.MethodGet, "/", nil); w.Code != http.StatusOK {
		t.Fatalf("first = %d", w.Code)
	}
	w := serve(h, http.MethodGet, "/", nil)
	if w.Code != http.StatusTeapot || w.Header().Get("X-Custom") != "1" || w.Header().Get(headers.NameRetryAfter) == "" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
}

func TestPolicyOnExempt(t *testing.T) {
	o := mustOptions(t, "exempt-ietf", func(o *options.Options) {
		o.Keys = []string{"header:X-Tenant"}
		o.PolicyHeaders = options.PolicyIETF
	})
	h := HTTP(o, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	w := serve(h, http.MethodGet, "/", nil)
	if w.Header().Get(headers.NameRateLimitPolicy) == "" || w.Header().Get(headers.NameRetryAfter) != "" {
		t.Fatalf("%v", w.Header())
	}
}

func TestClientIPFromContext(t *testing.T) {
	o := mustOptions(t, "ctx-ip", func(o *options.Options) { o.Keys = []string{"client_ip"} })
	h := HTTP(o, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(tctx.WithClientIP(req.Context(), "203.0.113.7"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
}
