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

package integration

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
)

func rateLimitDecisions(t *testing.T, h tricksterHarness, limiter, result string) float64 {
	t.Helper()
	v, _ := metricValue(t, h.MetricsAddr, "trickster_ratelimit_decisions_total",
		fmt.Sprintf(`limiter=%q,plane="http",result=%q`, limiter, result))
	return v
}

func TestRateLimitHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	h := staticConfigHarness(t, "testdata/configs/ratelimit.yaml")
	h.start(t)

	limited := rateLimitDecisions(t, h, "rl-it-route", "limited")
	resp, body := h.do(t, "/rl-it-prom/api/v1/query")
	if resp.StatusCode == http.StatusTooManyRequests {
		t.Fatalf("first query limited: %s", body)
	}
	if resp.Header.Get(headers.NameRateLimitPolicy) == "" {
		t.Fatalf("policy %v", resp.Header)
	}
	resp, body = h.do(t, "/rl-it-prom/api/v1/query")
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode, string(body))
	require.Equal(t, "no-store", resp.Header.Get(headers.NameCacheControl))
	wait, err := strconv.Atoi(resp.Header.Get(headers.NameRetryAfter))
	require.NoError(t, err)
	require.GreaterOrEqual(t, wait, 1)
	time.Sleep(time.Duration(wait) * time.Second)
	resp, body = h.do(t, "/rl-it-prom/api/v1/query")
	if resp.StatusCode == http.StatusTooManyRequests {
		t.Fatalf("request after Retry-After still limited: %s", body)
	}
	require.Greater(t, rateLimitDecisions(t, h, "rl-it-route", "limited"), limited)

	counted := rateLimitDecisions(t, h, "rl-it-count", "counted")
	for range 2 {
		resp, body = h.do(t, "/rl-it-prom/api/v1/query_range")
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusNotFound {
			t.Fatalf("count mode status %d: %s", resp.StatusCode, body)
		}
	}
	require.Greater(t, rateLimitDecisions(t, h, "rl-it-count", "counted"), counted)

	for range 2 {
		resp, body = h.do(t, "/rl-it-prom/api/v1/labels")
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusNotFound {
			t.Fatalf("path none status %d: %s", resp.StatusCode, body)
		}
	}
}

func TestRateLimitListenerReadiness(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	h := staticConfigHarness(t, "testdata/configs/ratelimit_listener.yaml")
	h.start(t)
	resp, body := h.do(t, "/no-such-rate-limit-path")
	require.Equal(t, http.StatusNotFound, resp.StatusCode, string(body))
	for range 3 {
		resp, body = h.do(t, "/trickster/ready")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	}
	resp, body = h.do(t, "/no-such-rate-limit-path")
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode, string(body))
}

func TestRateLimitPathNoneKeepsListener(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	h := staticConfigHarness(t, "testdata/configs/ratelimit_none.yaml")
	h.start(t)
	routeAllowed := rateLimitDecisions(t, h, "rl-it-none-route", "allowed")
	routeLimited := rateLimitDecisions(t, h, "rl-it-none-route", "limited")
	resp, body := h.do(t, "/rl-it-prom/api/v1/labels")
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusNotFound {
		t.Fatalf("path none status %d: %s", resp.StatusCode, body)
	}
	require.Equal(t, routeAllowed, rateLimitDecisions(t, h, "rl-it-none-route", "allowed"))
	resp, body = h.do(t, "/rl-it-prom/api/v1/labels")
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode, string(body))
	require.Equal(t, routeLimited, rateLimitDecisions(t, h, "rl-it-none-route", "limited"))
}
