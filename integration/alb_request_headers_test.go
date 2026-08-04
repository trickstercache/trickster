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
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// HX1: hop-by-hop headers from the inbound client must not be relayed to
// pool members during ALB fanout. RFC 7230 6.1 lists Connection, Keep-Alive,
// TE, Trailer, Transfer-Encoding, Upgrade, Proxy-Authorization,
// Proxy-Authenticate, plus any header named in Connection.
func TestALBRequestHeadersNoHopByHopLeak(t *testing.T) {
	healthyResp := albTestdata(t, "alb_request_headers/healthy.json")

	type capture struct {
		mu   sync.Mutex
		seen []http.Header
	}
	mkOrigin := func(c *capture) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/v1/status/buildinfo" {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
				return
			}
			c.mu.Lock()
			c.seen = append(c.seen, r.Header.Clone())
			c.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, healthyResp)
		}))
	}

	var capA, capB capture
	upA := mkOrigin(&capA)
	upB := mkOrigin(&capB)
	t.Cleanup(upA.Close)
	t.Cleanup(upB.Close)

	frontPort := 19100
	metricsPort := 19101
	mgmtPort := 19102

	yaml := fmt.Sprintf(albTestdata(t, "alb_request_headers/hop.yaml.tmpl"),
		frontPort, metricsPort, mgmtPort, upA.URL, upB.URL)

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

	u := fmt.Sprintf("http://127.0.0.1:%d/alb-fr-hop/api/v1/query?query=%s",
		frontPort, url.QueryEscape("up"))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	require.NoError(t, err)
	req.Header.Set("Proxy-Authorization", "Bearer should-not-leak")
	req.Header.Set("X-Hop-Specific", "1")
	req.Header.Set("Connection", "X-Hop-Specific")

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		capA.mu.Lock()
		capB.mu.Lock()
		gotA := len(capA.seen)
		gotB := len(capB.seen)
		capA.mu.Unlock()
		capB.mu.Unlock()
		if gotA > 0 || gotB > 0 {
			return
		}
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Do(req.Clone(context.Background()))
		if !assert.NoError(c, err) {
			return
		}
		resp.Body.Close()
		assert.Equal(c, http.StatusOK, resp.StatusCode)
		capA.mu.Lock()
		capB.mu.Lock()
		gotA = len(capA.seen)
		gotB = len(capB.seen)
		capA.mu.Unlock()
		capB.mu.Unlock()
		assert.Greater(c, gotA+gotB, 0, "no upstream received the fanout request")
	}, 10*time.Second, 200*time.Millisecond)

	checkOne := func(t *testing.T, name string, h http.Header) {
		t.Helper()
		assert.Emptyf(t, h.Values("Proxy-Authorization"),
			"%s received Proxy-Authorization=%v; should be stripped before fanout",
			name, h.Values("Proxy-Authorization"))
		assert.Emptyf(t, h.Values("X-Hop-Specific"),
			"%s received X-Hop-Specific=%v; header was named in inbound Connection: and per RFC 7230 6.1 must not be forwarded",
			name, h.Values("X-Hop-Specific"))
	}

	capA.mu.Lock()
	for i, h := range capA.seen {
		checkOne(t, fmt.Sprintf("prom-a req[%d]", i), h)
	}
	capA.mu.Unlock()
	capB.mu.Lock()
	for i, h := range capB.seen {
		checkOne(t, fmt.Sprintf("prom-b req[%d]", i), h)
	}
	capB.mu.Unlock()
}
