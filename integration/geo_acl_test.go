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
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
)

const (
	geoClientUS      = "192.0.2.10"
	geoClientFrance  = "198.51.100.10"
	geoClientGermany = "203.0.113.10"
	geoDecisions     = "trickster_geo_acl_decisions_total"
	geoDefaultBody   = "This resource is not available in your geographical area.\n"
	geoCountryHeader = "CF-IPCountry"
)

func geoDecisionCount(t *testing.T, h tricksterHarness, acl, result string) float64 {
	t.Helper()
	v, _ := metricValue(t, h.MetricsAddr, geoDecisions,
		fmt.Sprintf(`geo_acl=%q,plane="http",result=%q`, acl, result))
	return v
}

func TestGeoACLHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	h := staticConfigHarness(t, "testdata/configs/geo_acl.yaml")
	h.start(t)
	get := func(path, client string) (*http.Response, string) {
		t.Helper()
		opts := []requestOption{withParams(url.Values{"query": {fmt.Sprintf("up + 0*%d", time.Now().UnixNano())}})}
		if client != "" {
			opts = append(opts, withHeader(headers.NameXForwardedFor, client))
		}
		resp, body := h.do(t, path, opts...)
		return resp, string(body)
	}
	const query = "/geo-it-prom/api/v1/query"

	t.Run("allowed and refused", func(t *testing.T) {
		denied := geoDecisionCount(t, h, "geo-it-north-america", "denied")
		resp, body := get(query, geoClientUS)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		resp, body = get(query, geoClientFrance)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.Equal(t, geoDefaultBody, body)
		require.Equal(t, headers.ValueNoStore, resp.Header.Get(headers.NameCacheControl))
		// the test client itself is the trusted proxy, and no geofeed places it
		resp, _ = get(query, "")
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.Eventually(t, func() bool {
			return geoDecisionCount(t, h, "geo-it-north-america", "denied") == denied+2
		}, 10*time.Second, 50*time.Millisecond)
	})

	t.Run("paths", func(t *testing.T) {
		// none clears the backend's geo ACL for the path
		resp, body := get("/geo-it-prom/api/v1/status/buildinfo", geoClientFrance)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		// a path's own geo ACL replaces its backend's
		resp, body = get("/geo-it-prom/api/v1/labels", geoClientGermany)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		resp, body = get("/geo-it-prom/api/v1/labels", geoClientUS)
		require.Equal(t, http.StatusUnavailableForLegalReasons, resp.StatusCode)
		require.Equal(t, "Europe only.\n", body)
		require.True(t, strings.Contains(resp.Header.Get(headers.NameLink), `rel="blocked-by"`))
	})

	t.Run("header", func(t *testing.T) {
		// the listener trusts the test client, so the location header it sends is believed
		resp, body := h.do(t, "/geo-it-edge/api/v1/status/buildinfo", withHeader(geoCountryHeader, "FR"))
		require.Equal(t, http.StatusForbidden, resp.StatusCode, body)
		resp, body = h.do(t, "/geo-it-edge/api/v1/status/buildinfo", withHeader(geoCountryHeader, "US"))
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
	})

	t.Run("count", func(t *testing.T) {
		counted := geoDecisionCount(t, h, "geo-it-count", "counted")
		resp, body := get("/geo-it-count/api/v1/query", geoClientFrance)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Eventually(t, func() bool {
			return geoDecisionCount(t, h, "geo-it-count", "counted") == counted+1
		}, 10*time.Second, 50*time.Millisecond)
	})
}
