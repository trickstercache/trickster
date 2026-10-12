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

package setup

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit"
	rlopts "github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/challenge"
	"github.com/trickstercache/trickster/v2/pkg/routing"
)

type rateLimitChallengeSolver struct{}

func (rateLimitChallengeSolver) ServeHTTPChallenge(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != acmeTestChallenge {
		return false
	}
	_, _ = w.Write([]byte("token.thumbprint"))
	return true
}

func (rateLimitChallengeSolver) TLSALPNCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return nil, nil
}

func TestHTTP01BypassesExhaustedListenerLimiter(t *testing.T) {
	challenge.SetSolver(rateLimitChallengeSolver{})
	t.Cleanup(func() { challenge.SetSolver(nil) })
	c := acmeListenerConfig()
	o := &rlopts.Options{Name: t.Name(), Limit: 1}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	ln := c.Listeners[listenerconfig.DefaultFrontendName]
	ln.RateLimiterName, ln.RateLimiter = o.Name, o
	routers := map[string]router.Router{listenerconfig.DefaultFrontendName: markerRouter(acmeTestMarker)}
	desired := desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter(), nil, nil)
	plain := desired[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false)].router
	checkChallenge := func() {
		t.Helper()
		if code, body := serveBody(plain, acmeTestChallenge); code != http.StatusOK || body != "token.thumbprint" {
			t.Fatalf("challenge = %d %q", code, body)
		}
		if code, _ := serveBody(plain, challenge.HTTPPathPrefix+"unknown"); code != http.StatusNotFound {
			t.Fatalf("unknown challenge = %d", code)
		}
	}
	checkChallenge()
	if code, body := serveBody(plain, "/index.html"); code != http.StatusOK || body != acmeTestMarker {
		t.Fatalf("challenge consumed quota: %d %q", code, body)
	}
	if code, _ := serveBody(plain, "/index.html"); code != http.StatusTooManyRequests {
		t.Fatalf("ordinary request = %d", code)
	}
	checkChallenge()
	secure := desired[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, true)].router
	if code, _ := serveBody(secure, acmeTestChallenge); code != http.StatusTooManyRequests {
		t.Fatalf("TLS challenge path bypassed the limiter: %d", code)
	}
}

func TestApplyConfigFailedShapeChangeKeepsRateLimitCounts(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(origin.Close)
	load := func(limit int) (*config.Config, backends.Backends) {
		t.Helper()
		data := fmt.Sprintf(`
rate_limiters:
  reload-security:
    limit: %d
    window: 1h
backends:
  api:
    provider: rp
    origin_url: %s
    rate_limiter_name: reload-security
  z-last:
    provider: rp
    origin_url: %s
`, limit, origin.URL, origin.URL)
		c, clients, err := BootstrapConfig("-config", writeConfig(t, data))
		if err != nil {
			t.Fatal(err)
		}
		quietListeners(c)
		return c, clients
	}
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	si := &instance.ServerInstance{Listeners: group}
	t.Cleanup(func() { Shutdown(si) })
	c, clients := load(1)
	if err := ApplyConfig(si, c, clients, nil, nil, group); err != nil {
		t.Fatal(err)
	}
	if code, _ := serveBody(clients["api"].Router(), "/"); code != http.StatusNoContent {
		t.Fatalf("initial request = %d", code)
	}
	candidate, candidateClients := load(2)
	delete(candidateClients, "z-last")
	if err := ApplyConfig(si, candidate, candidateClients, nil, nil, group); err == nil ||
		!strings.Contains(err.Error(), "z-last") {
		t.Fatalf("expected failure after constructing api routes, got %v", err)
	}
	original := c.RateLimiters["reload-security"]
	kept := ratelimit.Lookup(original.Name, ratelimit.Shape{
		IPv6Prefix: original.IPv6Prefix, Window: time.Duration(original.Window), Limit: uint32(original.Limit), MaxKeys: original.MaxKeys,
	}, ratelimit.Policy{})
	if kept.Len() != 1 {
		t.Fatal("failed apply lost the committed bucket")
	}
	restored, restoredClients := load(1)
	if err := ApplyConfig(si, restored, restoredClients, nil, nil, group); err != nil {
		t.Fatal(err)
	}
	if code, _ := serveBody(restoredClients["api"].Router(), "/"); code != http.StatusTooManyRequests {
		t.Fatalf("restoring original shape lost quota: %d", code)
	}
}

func TestIndirectCountLimiterStripsSpoofedMarker(t *testing.T) {
	for _, plane := range []string{"rule", "alb"} {
		t.Run(plane, func(t *testing.T) {
			var got []string
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = slices.Clone(r.Header.Values(headers.NameXTricksterRateLimited))
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(origin.Close)
			front := `    provider: rule
    rule_name: dispatch
`
			if plane == "alb" {
				front = `    provider: alb
    alb:
      mechanism: round_robin
      pool:
        - name: target
`
			}
			data := fmt.Sprintf(`
listeners:
  edge:
    port: 19001
rate_limiters:
  %s:
    limit: 1
    window: 1h
    action: count
rules:
  dispatch:
    input_source: path
    operation: eq
    next_route: target
backends:
  front:
%s    listener_names: [edge]
  target:
    provider: rp
    origin_url: %s
    listener_names: [default]
    rate_limiter_name: %s
`, t.Name(), front, origin.URL, t.Name())
			c, clients, err := BootstrapConfig("-config", writeConfig(t, data))
			if err != nil {
				t.Fatal(err)
			}
			routers := map[string]router.Router{"edge": lm.NewRouter(), listenerconfig.DefaultFrontendName: lm.NewRouter()}
			if err := routing.RegisterProxyRoutesForListeners(c, clients, routers, lm.NewRouter(), nil, nil, false); err != nil {
				t.Fatal(err)
			}
			if err := alb.StartALBPools(clients, nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = alb.StopPools(clients) })
			h := wrapListener(c, "edge", c.Listeners["edge"], nil, routers["edge"])
			for _, want := range [][]string{nil, {t.Name()}} {
				req := httptest.NewRequest(http.MethodGet, "/front/", nil)
				req.Header.Add(headers.NameXTricksterRateLimited, "spoofed")
				req.Header.Add(headers.NameXTricksterRateLimited, "another-spoof")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != http.StatusNoContent || !slices.Equal(got, want) {
					t.Fatalf("response = %d, marker = %v; want %v", w.Code, got, want)
				}
			}
		})
	}
}
