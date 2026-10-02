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
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/metricsutil"
	"github.com/trickstercache/trickster/v2/integration/internal/portutil"

	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	acmeIssuerName      = "pebble"
	acmeOriginBody      = "acme-origin"
	acmeReadyPath       = "/trickster/ready"
	acmeHandlerPath     = "/trickster/acme"
	acmeCertsPending    = "certificates pending"
	acmeWaitOnStartup   = "60s"
	acmeIssueTimeout    = 60 * time.Second
	acmeOrdersMetric    = `trickster_acme_orders_total{issuer="pebble",result="success"}`
	acmeChallengeMetric = `trickster_acme_challenge_requests_total{result="served",type="%s"}`
	acmeRedisEndpoint   = "127.0.0.1:6379"
)

type acmeStack struct {
	pebble      *testPebble
	dns         *testDNS
	configPath  string
	plainAddr   string
	tlsAddr     string
	metricsAddr string
	mgmtAddr    string
	release     func()
}

type acmeStackOptions struct {
	challenge   string
	domains     []string
	hosts       []string
	dnsIssuer   bool
	onDemand    string
	redisPrefix string
}

func newACMEStack(t *testing.T, o acmeStackOptions) *acmeStack {
	t.Helper()
	ports, release := portutil.Reserve(t, 4)
	d := startTestDNS(t)
	p := startPebble(t, ports[0], ports[1], d.Addr)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, acmeOriginBody)
	}))
	t.Cleanup(origin.Close)
	issuer := fmt.Sprintf("challenges: [%s]", o.challenge)
	if o.dnsIssuer {
		issuer = fmt.Sprintf(`dns_provider:
        provider: rfc2136
        propagation_timeout: -1s
        resolvers: [%s]
        rfc2136:
          server: %s
          key_name: %s
          key_alg: hmac-sha256
          key: %s`, d.Addr, d.Addr, acmeTestTSIGName, acmeTestTSIGSecret)
	}
	backendACME := fmt.Sprintf("\n    tls:\n      acme:\n        issuer: %s", acmeIssuerName)
	if len(o.domains) > 0 {
		backendACME += "\n        domains: " + yamlList(o.domains)
	}
	storage := "path: " + t.TempDir()
	if o.redisPrefix != "" {
		storage = fmt.Sprintf("provider: redis\n    redis:\n      key_prefix: %q\n"+
			"      connection:\n        endpoint: %s", o.redisPrefix, acmeRedisEndpoint)
	}
	onDemand := ""
	if o.onDemand != "" {
		// on-demand issuance covers the backend's hosts, so the backend does not opt in itself
		backendACME = ""
		onDemand = fmt.Sprintf("\n  on_demand:\n    issuer: %s\n    listeners: [default]\n    %s",
			acmeIssuerName, o.onDemand)
	}
	cfg := fmt.Sprintf(`listeners:
  default:
    address: 127.0.0.1
    port: %d
    tls_address: 127.0.0.1
    tls_port: %d
  metrics:
    address: 127.0.0.1
    port: %d
  mgmt:
    address: 127.0.0.1
    port: %d
logging:
  log_level: info
acme:%s
  wait_on_startup: %s
  storage:
    %s
  issuers:
    %s:
      directory_url: %s
      email: acme-integration@trickstercache.org
      agree_to_terms: true
      trusted_ca_paths: [%s]
      %s
backends:
  site:
    provider: reverseproxy
    origin_url: %s
    hosts: %s%s
`, ports[0], ports[1], ports[2], ports[3], onDemand, acmeWaitOnStartup, storage, acmeIssuerName,
		p.DirectoryURL, p.TrustPath, issuer, origin.URL, yamlList(o.hosts), backendACME)
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0o600))
	return &acmeStack{
		pebble: p, dns: d, configPath: path, release: release,
		plainAddr:   fmt.Sprintf("127.0.0.1:%d", ports[0]),
		tlsAddr:     fmt.Sprintf("127.0.0.1:%d", ports[1]),
		metricsAddr: fmt.Sprintf("127.0.0.1:%d", ports[2]),
		mgmtAddr:    fmt.Sprintf("127.0.0.1:%d", ports[3]),
	}
}

func yamlList(items []string) string {
	// each item is quoted, since a leading * would otherwise be a YAML alias
	quoted := make([]string, len(items))
	for i, v := range items {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func (s *acmeStack) start(t *testing.T) func() {
	t.Helper()
	s.release()
	return runTrickster(t, context.Background(), "-config", s.configPath)
}

func (s *acmeStack) readiness() (int, string, error) {
	resp, err := http.Get("http://" + s.mgmtAddr + acmeReadyPath)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

func (s *acmeStack) requireReady(t *testing.T, within time.Duration) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, body, err := s.readiness()
		if assert.NoError(c, err) {
			assert.Equal(c, http.StatusOK, status, body)
		}
	}, within, 100*time.Millisecond, "readiness never reported ready")
}

func (s *acmeStack) requireServed(t *testing.T, serverName string) string {
	t.Helper()
	var serial string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		leaf, err := s.pebble.servedLeaf(s.tlsAddr, serverName)
		if assert.NoError(c, err) {
			serial = leaf.SerialNumber.String()
		}
	}, acmeIssueTimeout, 250*time.Millisecond, "no verifiable certificate served for "+serverName)
	return serial
}

func (s *acmeStack) get(t *testing.T, host string) string {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: s.pebble.Roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, s.tlsAddr)
		},
	}}
	resp, err := client.Get("https://" + host + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	return string(b)
}

func (s *acmeStack) metric(t *testing.T, name string) float64 {
	t.Helper()
	return metricsutil.ScrapeURL(t, "http://"+s.metricsAddr+"/metrics", nil)[name]
}

func TestACME_HTTP01(t *testing.T) {
	const domain = "http01.acme.test"
	s := newACMEStack(t, acmeStackOptions{challenge: "http-01", hosts: []string{domain}})
	s.pebble.Hold(t)
	stop := s.start(t)

	// readiness holds while the CA has not answered, though every listener is serving
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, body, err := s.readiness()
		if assert.NoError(c, err) {
			assert.Equal(c, http.StatusServiceUnavailable, status)
			assert.Equal(c, acmeCertsPending, body)
		}
	}, 10*time.Second, 50*time.Millisecond)
	ordersBefore := s.metric(t, acmeOrdersMetric)
	challengesBefore := s.metric(t, fmt.Sprintf(acmeChallengeMetric, "http-01"))
	s.pebble.Release()

	s.requireReady(t, acmeIssueTimeout)
	serial := s.requireServed(t, domain)
	require.Equal(t, acmeOriginBody, s.get(t, domain))
	require.Greater(t, s.metric(t, acmeOrdersMetric), ordersBefore)
	require.Greater(t, s.metric(t, fmt.Sprintf(acmeChallengeMetric, "http-01")), challengesBefore)

	// a request under the challenge path that matches no pending challenge never reaches the origin
	resp, err := http.Get("http://" + s.plainAddr + "/.well-known/acme-challenge/unknown")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// a restart loads the stored certificate rather than ordering another; counters are process-wide
	ordersBefore = s.metric(t, acmeOrdersMetric)
	stop()
	runTrickster(t, context.Background(), "-config", s.configPath)
	s.requireReady(t, 10*time.Second)
	require.Equal(t, serial, s.requireServed(t, domain))
	require.Equal(t, ordersBefore, s.metric(t, acmeOrdersMetric))

	// a forced renewal through the mgmt handler replaces the served certificate
	resp, err = http.Post("http://"+s.mgmtAddr+acmeHandlerPath+"?domain="+domain, "", nil)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		leaf, err := s.pebble.servedLeaf(s.tlsAddr, domain)
		if assert.NoError(c, err) {
			assert.NotEqual(c, serial, leaf.SerialNumber.String())
		}
	}, acmeIssueTimeout, 250*time.Millisecond, "a forced renewal never replaced the certificate")
}

func TestACME_TLSALPN01(t *testing.T) {
	const domain = "alpn.acme.test"
	s := newACMEStack(t, acmeStackOptions{challenge: "tls-alpn-01", hosts: []string{domain}})
	s.pebble.Hold(t)
	s.start(t)
	waitForTrickster(t, s.metricsAddr)
	before := s.metric(t, fmt.Sprintf(acmeChallengeMetric, "tls-alpn-01"))
	s.pebble.Release()
	s.requireReady(t, acmeIssueTimeout)
	s.requireServed(t, domain)
	require.Equal(t, acmeOriginBody, s.get(t, domain))
	require.Greater(t, s.metric(t, fmt.Sprintf(acmeChallengeMetric, "tls-alpn-01")), before)
}

func TestACME_DNS01Wildcard(t *testing.T) {
	const wildcard = "*.wild.acme.test"
	s := newACMEStack(t, acmeStackOptions{dnsIssuer: true, hosts: []string{wildcard}})
	s.start(t)
	s.requireReady(t, acmeIssueTimeout)
	s.requireServed(t, "one.wild.acme.test")
	require.Equal(t, acmeOriginBody, s.get(t, "two.wild.acme.test"))
}

func TestACME_OnDemand(t *testing.T) {
	const (
		allowed = "one.od.acme.test"
		asked   = "yes.ask.acme.test"
		refused = "no.ask.acme.test"
		outside = "elsewhere.acme.test"
	)
	ask := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d := r.URL.Query().Get("domain"); d == asked || strings.HasSuffix(d, ".od.acme.test") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(ask.Close)
	s := newACMEStack(t, acmeStackOptions{
		challenge: "http-01",
		hosts:     []string{"**.od.acme.test", "**.ask.acme.test", outside},
		onDemand:  fmt.Sprintf("ask: %s\n    allowed_domains: [\"*.od.acme.test\", \"*.ask.acme.test\"]", ask.URL),
	})
	s.start(t)
	s.requireReady(t, 10*time.Second)

	// the first handshake for an approved name blocks while its certificate is issued
	s.requireServed(t, allowed)
	s.requireServed(t, asked)
	require.Equal(t, acmeOriginBody, s.get(t, allowed))

	// a name the ask endpoint refuses, or outside allowed_domains, is never issued
	refusedBefore := s.metric(t, `trickster_acme_on_demand_decisions_total{result="refused"}`)
	for _, name := range []string{refused, outside} {
		_, err := s.pebble.servedLeaf(s.tlsAddr, name)
		require.Error(t, err, name)
	}
	require.Equal(t, refusedBefore+2, s.metric(t, `trickster_acme_on_demand_decisions_total{result="refused"}`))

	// later handshakes are answered from the listener's store without another order
	ordersBefore := s.metric(t, acmeOrdersMetric)
	for range 3 {
		s.requireServed(t, allowed)
	}
	require.Equal(t, ordersBefore, s.metric(t, acmeOrdersMetric))
}

func TestACME_RedisStorage(t *testing.T) {
	const domain = "redis.acme.test"
	// a prefix no earlier run used, since Redis outlives the test; its keys are removed afterward
	prefix := fmt.Sprintf("trickster:acme:it:%d:", time.Now().UnixNano())
	rc := redis.NewClient(&redis.Options{Addr: acmeRedisEndpoint})
	t.Cleanup(func() {
		ctx := context.Background()
		keys, _ := rc.Keys(ctx, "{"+prefix+"}*").Result()
		if len(keys) > 0 {
			rc.Del(ctx, keys...)
		}
		rc.Close()
	})
	require.NoError(t, rc.Ping(context.Background()).Err(), "the developer environment's Redis is required")
	s := newACMEStack(t, acmeStackOptions{challenge: "http-01", hosts: []string{domain}, redisPrefix: prefix})
	stop := s.start(t)
	s.requireReady(t, acmeIssueTimeout)
	serial := s.requireServed(t, domain)

	// a restart, which could as well be another instance sharing the keyspace, reuses the certificate
	ordersBefore := s.metric(t, acmeOrdersMetric)
	stop()
	runTrickster(t, context.Background(), "-config", s.configPath)
	s.requireReady(t, 10*time.Second)
	require.Equal(t, serial, s.requireServed(t, domain))
	require.Equal(t, ordersBefore, s.metric(t, acmeOrdersMetric))
}
