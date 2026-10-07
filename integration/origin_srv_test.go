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

package integration

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The SRV scenario fronts origins reachable only on the ports their SRV records publish in the
// CoreDNS zone with one rule, rewriter, router and rp backend

const (
	srvOriginsBackend = "origins"
	srvSlowPath       = "/slow"
	srvSlowDelay      = time.Second
	srvLeafHostHeader = "X-Seen-Host"
	srvAlphaHost      = "alpha.trickster.test"
	srvBravoHost      = "bravo.trickster.test"
	srvBravoAlias     = "bravo-legacy.trickster.test"
	srvCharlieHost    = "charlie.trickster.test"
	srvAlphaBody      = "alpha origin content"
	srvBravoBody      = "bravo origin content"
	srvCharlieBody    = "charlie origin content"
	srvRedirectURL    = "https://www.example.com/"
)

// srvLeaf is an origin whose body names it; it closes each connection so every request dials again.
// slow, if set, hears of each slow request.
func srvLeaf(t *testing.T, body string, slow chan<- struct{}) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == srvSlowPath && slow != nil {
			slow <- struct{}{}
			time.Sleep(srvSlowDelay)
		}
		w.Header().Set("Connection", "close")
		w.Header().Set(srvLeafHostHeader, r.Host)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	_, port, err := net.SplitHostPort(s.Listener.Addr().String())
	require.NoError(t, err)
	return port
}

func srvOriginConfig(frontPort, metricsPort, mgmtPort int, minTTL string) string {
	return fmt.Sprintf(`
listeners:
  default:
    port: %d
  metrics:
    port: %d
  mgmt:
    port: %d
mgmt:
  reload_drain_timeout: 5s
logging:
  log_level: info

request_rewriters:
  to-origin:
    instructions:
      - [ 'hostname', 'set', '${site}.origins.trickster.test' ]

rules:
  site-router:
    input_source: hostname
    input_type: string
    operation: rmatch
    operation_arg: '^(?P<site>[a-z0-9-]{1,63})\.trickster\.test$'
    cases:
      - matches: [ 'true' ]
        req_rewriter_name: to-origin
        next_route: %s
    redirect_url: '%s'

backends:
  router:
    provider: rule
    rule_name: site-router
    is_default: true

  %s:
    provider: rp
    origin_url: 'http://origins.trickster.test'
    preserve_host: true
    path_routing_disabled: true
    origin_resolution:
      mode: srv
      resolver: '%s'
      min_ttl: %s
      max_ttl: 5s
      negative_ttl: 1s
`, frontPort, metricsPort, mgmtPort, srvOriginsBackend, srvRedirectURL,
		srvOriginsBackend, coreDNSAddr, minTTL)
}

// srvGet sends a request for host to the front listener without following redirects
func srvGet(t *testing.T, frontAddr, host, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+frontAddr+path, nil)
	require.NoError(t, err)
	req.Host = host
	c := &http.Client{
		Timeout: discoveryHTTPClient.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(b)
}

func requireSRVOrigin(t *testing.T, frontAddr, host, body string) {
	t.Helper()
	resp, got := srvGet(t, frontAddr, host, "/")
	require.Equal(t, http.StatusOK, resp.StatusCode, "host %s: %s", host, got)
	require.Equal(t, body, got, "host %s reached the wrong origin", host)
	require.Equal(t, host, resp.Header.Get(srvLeafHostHeader), "preserve_host forwards the public host")
}

func TestOriginSRVResolution(t *testing.T) {
	requireCoreDNS(t)
	guardSIGHUP(t)
	ports, release := portutil.Reserve(t, 4)
	frontPort, metricsPort, mgmtPort, dead := ports[0], ports[1], ports[2], ports[3]
	slow := make(chan struct{}, 1)
	alpha := srvLeaf(t, srvAlphaBody, slow)
	bravo := srvLeaf(t, srvBravoBody, nil)
	charlie := srvLeaf(t, srvCharlieBody, nil)

	// alpha's rewritten name owns its SRV record; bravo's names alias the service record.
	// bravo's weight-9 target has nothing listening, so most dials fail over to the live one.
	records := []string{
		"node-07.svc\tIN\tA\t127.0.0.1",
		"node-02.svc\tIN\tA\t127.0.0.1",
		"node-04.svc\tIN\tA\t127.0.0.1",
		"alpha.origins\tIN\tSRV\t1 1 " + alpha + " node-07.svc.trickster.test.",
		"_bravo._tcp.svc\tIN\tSRV\t1 1 " + bravo + " node-02.svc.trickster.test.",
		fmt.Sprintf("_bravo._tcp.svc\tIN\tSRV\t1 9 %d node-04.svc.trickster.test.", dead),
		"bravo.origins\tIN\tCNAME\t_bravo._tcp.svc.trickster.test.",
		"bravo-legacy.origins\tIN\tCNAME\t_bravo._tcp.svc.trickster.test.",
	}
	writeZone(t, records...)

	cfgPath := t.TempDir() + "/trickster.yaml"
	require.NoError(t, os.WriteFile(cfgPath,
		[]byte(srvOriginConfig(frontPort, metricsPort, mgmtPort, "1s")), 0o644))
	release()
	runTrickster(t, t.Context(), "-config", cfgPath)
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	frontAddr := fmt.Sprintf("127.0.0.1:%d", frontPort)
	waitForTrickster(t, metricsAddr)

	// CoreDNS reloads the zone file on a 1s cadence
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, got := srvGet(t, frontAddr, srvAlphaHost, "/")
		assert.Equal(collect, http.StatusOK, resp.StatusCode)
		assert.Equal(collect, srvAlphaBody, got)
	}, 15*time.Second, 250*time.Millisecond)

	t.Run("each hostname reaches its own origin", func(t *testing.T) {
		requireSRVOrigin(t, frontAddr, srvAlphaHost, srvAlphaBody)
		requireSRVOrigin(t, frontAddr, srvBravoHost, srvBravoBody)
		requireSRVOrigin(t, frontAddr, srvBravoAlias, srvBravoBody)
		requireSRVOrigin(t, frontAddr, srvAlphaHost, srvAlphaBody)
	})

	t.Run("a failing target falls through within one request", func(t *testing.T) {
		for range 20 {
			requireSRVOrigin(t, frontAddr, srvBravoHost, srvBravoBody)
		}
		failures, ok := metricValue(t, metricsAddr, "trickster_proxy_origin_srv_dial_attempts_total",
			`backend_name="`+srvOriginsBackend+`",result="failure"`)
		require.True(t, ok)
		require.Positive(t, failures, "the weight-9 dead target is dialed first nearly every time")
	})

	t.Run("a hostname outside the pattern redirects", func(t *testing.T) {
		resp, _ := srvGet(t, frontAddr, "www.example.org", "/")
		require.GreaterOrEqual(t, resp.StatusCode, http.StatusMultipleChoices)
		require.Less(t, resp.StatusCode, http.StatusBadRequest)
		require.Equal(t, srvRedirectURL, resp.Header.Get("Location"))
	})

	t.Run("a new origin needs only DNS records", func(t *testing.T) {
		resp, _ := srvGet(t, frontAddr, srvCharlieHost, "/")
		require.Equal(t, http.StatusBadGateway, resp.StatusCode, "charlie has no SRV records yet")

		records = append(records,
			"charlie.origins\tIN\tSRV\t1 1 "+charlie+" node-07.svc.trickster.test.")
		writeZone(t, records...)
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			resp, got := srvGet(t, frontAddr, srvCharlieHost, "/")
			assert.Equal(collect, http.StatusOK, resp.StatusCode)
			assert.Equal(collect, srvCharlieBody, got)
		}, 15*time.Second, 250*time.Millisecond)
		requireSRVOrigin(t, frontAddr, srvCharlieHost, srvCharlieBody)
		requireSRVOrigin(t, frontAddr, srvAlphaHost, srvAlphaBody)
	})

	t.Run("a reload keeps in-flight requests", func(t *testing.T) {
		type result struct {
			status int
			body   string
			err    error
		}
		inflight := make(chan result, 1)
		go func() {
			req, err := http.NewRequest(http.MethodGet, "http://"+frontAddr+srvSlowPath, nil)
			if err != nil {
				inflight <- result{err: err}
				return
			}
			req.Host = srvAlphaHost
			resp, err := discoveryHTTPClient.Do(req)
			if err != nil {
				inflight <- result{err: err}
				return
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			inflight <- result{status: resp.StatusCode, body: string(b), err: err}
		}()
		// the request is in flight once the origin holds it
		select {
		case <-slow:
		case <-time.After(10 * time.Second):
			t.Fatal("the slow request never reached its origin")
		}
		require.NoError(t, os.WriteFile(cfgPath,
			[]byte(srvOriginConfig(frontPort, metricsPort, mgmtPort, "2s")), 0o644))
		sighupUntilReloaded(t, metricsAddr)
		r := <-inflight
		require.NoError(t, r.err)
		require.Equal(t, http.StatusOK, r.status)
		require.Equal(t, srvAlphaBody, r.body)
		requireSRVOrigin(t, frontAddr, srvAlphaHost, srvAlphaBody)
		requireSRVOrigin(t, frontAddr, srvBravoAlias, srvBravoBody)
	})
}
