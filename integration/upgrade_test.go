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
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"

	"github.com/stretchr/testify/require"
)

// echoUpgradeOrigin answers a protocol upgrade and echoes one line back over
// the switched connection, which is enough to prove a tunnel carries bytes in
// both directions without pulling in a WebSocket library.
func echoUpgradeOrigin(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		brw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		brw.Flush()
		line, err := brw.ReadString('\n')
		if err != nil {
			return
		}
		brw.WriteString("echo:" + line)
		brw.Flush()
	}))
	t.Cleanup(s.Close)
	return s
}

// addPassthroughBackend registers a reverseproxy backend named name pointing at
// originURL, which routes through the ReverseProxy-backed passthrough lane.
func addPassthroughBackend(name, originURL string) func(*tkconfig.Config) {
	return func(c *tkconfig.Config) {
		if c.Backends == nil {
			c.Backends = make(bo.Lookup)
		}
		o := bo.New()
		o.Name = name
		o.Provider = providers.ReverseProxy
		o.OriginURL = originURL
		o.CacheName = "default"
		c.Backends[name] = o
	}
}

func TestUpgradeTunnel(t *testing.T) {
	origin := echoUpgradeOrigin(t)
	h := configHarness(t, addPassthroughBackend("wsproxy", origin.URL))
	h.start(t)

	conn, err := net.DialTimeout("tcp", h.BaseAddr, 5*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(20*time.Second)))

	_, err = conn.Write([]byte("GET /wsproxy/socket HTTP/1.1\r\nHost: " + h.BaseAddr +
		"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"))
	require.NoError(t, err)

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode,
		"trickster must relay the origin's 101 rather than downgrading it")
	require.Equal(t, "websocket", resp.Header.Get("Upgrade"))

	_, err = conn.Write([]byte("ping\n"))
	require.NoError(t, err)
	got, err := br.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "echo:ping\n", got, "tunnel must carry bytes in both directions")
}

func TestUpgradeRejectedByOrigin(t *testing.T) {
	// an origin that declines the upgrade must surface as an ordinary response,
	// not a hung or hijacked connection
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("no"))
	}))
	defer origin.Close()

	h := configHarness(t, addPassthroughBackend("wsdeny", origin.URL))
	h.start(t)

	req, err := http.NewRequest(http.MethodGet, "http://"+h.BaseAddr+"/wsdeny/socket", nil)
	require.NoError(t, err)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}
