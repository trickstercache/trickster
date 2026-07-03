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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// CVE-2021-33197 attack shape: an empty token in the Connection header
// historically bypassed Go's reverse-proxy hop-by-hop stripper, letting clients
// smuggle hop-by-hop headers (e.g. Authorization) to the upstream. The stdlib
// fix landed in Go 1.16.6 / 1.17. This test is regression coverage: it drives
// the malformed shape through Trickster end-to-end and asserts the named
// hop-by-hop headers do not reach the upstream.

type headerCaptureUpstream struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []http.Header
}

func newHeaderCaptureUpstream(t *testing.T) *headerCaptureUpstream {
	t.Helper()
	u := &headerCaptureUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.got = append(u.got, r.Header.Clone())
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *headerCaptureUpstream) lastHeader(t *testing.T) http.Header {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	require.NotEmpty(t, u.got, "upstream did not receive any requests")
	return u.got[len(u.got)-1]
}

func TestProxyStripsHopByHopWithEmptyConnectionToken(t *testing.T) {
	const (
		frontPort   = 19400
		metricsPort = 19401
		mgmtPort    = 19402
	)
	upstream := newHeaderCaptureUpstream(t)

	var sb strings.Builder
	fmt.Fprintf(&sb, "frontend:\n  listen_port: %d\n", frontPort)
	fmt.Fprintf(&sb, "metrics:\n  listen_port: %d\n", metricsPort)
	fmt.Fprintf(&sb, "mgmt:\n  listen_port: %d\n", mgmtPort)
	sb.WriteString("logging:\n  log_level: error\n")
	sb.WriteString("caches:\n  mem1:\n    provider: memory\n")
	sb.WriteString("backends:\n")
	sb.WriteString("  rpc1:\n")
	sb.WriteString("    provider: reverseproxycache\n")
	fmt.Fprintf(&sb, "    origin_url: %s\n", upstream.srv.URL)
	sb.WriteString("    cache_name: mem1\n")
	sb.WriteString("    paths:\n")
	sb.WriteString("      - path: /\n")
	sb.WriteString("        match_type: prefix\n")
	sb.WriteString("        handler: proxycache\n")

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(sb.String()), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

	type attack struct {
		name             string
		connectionHeader string
		extraHeaders     map[string]string
		mustStrip        []string
	}

	cases := []attack{
		{
			name:             "empty token then Authorization",
			connectionHeader: ", Authorization",
			extraHeaders:     map[string]string{"Authorization": "Bearer leaked-token"},
			mustStrip:        []string{"Authorization"},
		},
		{
			name:             "empty token then X-Forwarded-For",
			connectionHeader: ", X-Forwarded-For",
			extraHeaders:     map[string]string{"X-Forwarded-For": "10.0.0.1"},
			mustStrip:        []string{"X-Forwarded-For"},
		},
		{
			name:             "well-formed connection lists custom header",
			connectionHeader: "close, X-Trickster-User",
			extraHeaders:     map[string]string{"X-Trickster-User": "alice"},
			mustStrip:        []string{"X-Trickster-User"},
		},
	}

	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := fmt.Sprintf("http://127.0.0.1:%d/rpc1/hop-%d", frontPort, i)
			req, err := http.NewRequest(http.MethodGet, u, nil)
			require.NoError(t, err)
			req.Header.Set("Connection", c.connectionHeader)
			for k, v := range c.extraHeaders {
				req.Header.Set(k, v)
			}
			resp, err := client.Do(req)
			require.NoError(t, err)
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)

			got := upstream.lastHeader(t)
			for _, h := range c.mustStrip {
				require.Empty(t, got.Values(h),
					"upstream received hop-by-hop header %q that should have been stripped (Connection=%q, got=%v)",
					h, c.connectionHeader, got.Values(h))
			}
			require.Empty(t, got.Values("Connection"),
				"upstream received Connection header; proxy should never forward it (got=%v)",
				got.Values("Connection"))
		})
	}
}
