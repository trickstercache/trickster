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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
)

func TestIPACL(t *testing.T) {
	// the listener allows 192.0.2.0/24 and 203.0.113.5/32, the one address wall allows, so that client can reach
	// the backend list
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	openOrigin, openHits := countingOrigin(t)
	apiOrigin, apiHits := countingOrigin(t)
	memberOrigin, memberHits := countingOrigin(t)
	h := startIPACL(t, openOrigin.URL, apiOrigin.URL, memberOrigin.URL)

	t.Run("listener", func(t *testing.T) {
		before := openHits.Load()
		require.Equal(t, http.StatusOK, ipACLStatus(t, h, "/open/", "192.0.2.9"))
		require.Equal(t, before+1, openHits.Load())

		before = openHits.Load()
		denied := aclDenies(t, h.MetricsAddr, `ip_acl="edge",scope="listener",verdict="deny"`)
		require.Equal(t, http.StatusForbidden, ipACLStatus(t, h, "/open/", "198.51.100.8"))
		require.Equal(t, before, openHits.Load())
		require.GreaterOrEqual(t, aclDenies(t, h.MetricsAddr, `ip_acl="edge",scope="listener",verdict="deny"`), denied+1)
	})

	t.Run("path", func(t *testing.T) {
		denied := aclDenies(t, h.MetricsAddr, `ip_acl="wall",scope="backend",verdict="deny"`)
		before := apiHits.Load()
		require.Equal(t, http.StatusTooManyRequests, ipACLStatus(t, h, "/api/inherited", "192.0.2.9"))
		require.Equal(t, before, apiHits.Load())
		require.GreaterOrEqual(t, aclDenies(t, h.MetricsAddr, `ip_acl="wall",scope="backend",verdict="deny"`), denied+1)

		denied = aclDenies(t, h.MetricsAddr, `ip_acl="wall",scope="backend",verdict="deny"`)
		before = apiHits.Load()
		require.Equal(t, http.StatusOK, ipACLStatus(t, h, "/api/public", "192.0.2.9"))
		require.Equal(t, before+1, apiHits.Load())
		require.Equal(t, denied, aclDenies(t, h.MetricsAddr, `ip_acl="wall",scope="backend",verdict="deny"`))
	})

	t.Run("alb", func(t *testing.T) {
		before := memberHits.Load()
		denied := aclDenies(t, h.MetricsAddr, `ip_acl="wall",scope="backend",verdict="deny"`)
		require.Equal(t, http.StatusTooManyRequests, ipACLStatus(t, h, "/pool/", "192.0.2.9"))
		require.Equal(t, before, memberHits.Load())
		require.GreaterOrEqual(t, aclDenies(t, h.MetricsAddr, `ip_acl="wall",scope="backend",verdict="deny"`), denied+1)

		require.Equal(t, http.StatusOK, ipACLStatus(t, h, "/pool/", "203.0.113.5"))
		require.Equal(t, before+1, memberHits.Load())
	})
}

func countingOrigin(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func startIPACL(t *testing.T, openURL, apiURL, memberURL string) tricksterHarness {
	t.Helper()
	ports, release := portutil.Reserve(t, 3)
	front, metrics, mgmt := ports[0], ports[1], ports[2]
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	cfg := fmt.Sprintf(`ip_acls:
  edge:
    source: client_ip
    action: reject
    default: deny
    allow:
      - 192.0.2.0/24
      - 203.0.113.5/32
  wall:
    source: client_ip
    action: reject
    status: 429
    default: deny
    allow:
      - 203.0.113.5/32
listeners:
  default:
    address: 127.0.0.1
    port: %d
    ip_acl_name: edge
    trusted_proxies:
      - 127.0.0.1
  metrics:
    address: 127.0.0.1
    port: %d
  mgmt:
    address: 127.0.0.1
    port: %d
logging:
  log_level: error
backends:
  open:
    provider: rp
    origin_url: %s
    listener_names: [default]
  api:
    provider: rp
    origin_url: %s
    listener_names: [default]
    ip_acl_name: wall
    path_defaults_disabled: true
    paths:
      - path: /inherited
        match_type: prefix
        handler: proxy
        methods: [GET]
      - path: /public
        match_type: prefix
        handler: proxy
        methods: [GET]
        ip_acl_name: none
  member:
    provider: rp
    origin_url: %s
    ip_acl_name: wall
  pool:
    provider: alb
    listener_names: [default]
    alb:
      mechanism: rr
      pool:
        - member
`, front, metrics, mgmt, openURL, apiURL, memberURL)
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0o600))
	release()
	runTrickster(t, context.Background(), "-config", path)
	h := tricksterHarness{
		BaseAddr:    fmt.Sprintf("127.0.0.1:%d", front),
		MetricsAddr: fmt.Sprintf("127.0.0.1:%d", metrics),
	}
	waitForTrickster(t, h.MetricsAddr)
	return h
}

func ipACLStatus(t *testing.T, h tricksterHarness, path, client string) int {
	t.Helper()
	resp, _ := h.do(t, path, withHeader(headers.NameXForwardedFor, client))
	return resp.StatusCode
}

func aclDenies(t *testing.T, metricsAddr, fragment string) float64 {
	t.Helper()
	value, ok := metricValue(t, metricsAddr, "trickster_ip_acl_decisions_total", fragment)
	if !ok {
		return 0
	}
	return value
}
