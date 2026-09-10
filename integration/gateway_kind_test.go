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
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"
)

func TestGatewayKind(t *testing.T) {
	skipUnlessKind(t)
	waitForTrickster(t, gatewayMetricsAddr)
	waitRoute(t, gatewayHTTPAddr, "shop.example.com", "/", http.StatusOK, 2*time.Minute)

	t.Run("host and path", func(t *testing.T) {
		resp, body := hostGet(t, gatewayHTTPAddr, "shop.example.com", "/hello")
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Contains(t, body, "Hostname: webecho-")
		require.Contains(t, body, "GET /hello HTTP/1.1")
		require.Equal(t, "shop", resp.Header.Get("X-Trickster-Gateway"),
			"the plain rule's response header filter applies")
		resp, _ = hostGet(t, gatewayHTTPAddr, "unclaimed.example.com", "/hello")
		require.Equal(t, http.StatusNotFound, resp.StatusCode, "a host no route claims is not served")
	})

	t.Run("header match", func(t *testing.T) {
		canary := http.Header{"X-Canary": {"always"}}
		for range 5 {
			resp, body := hostGet(t, gatewayHTTPAddr, "shop.example.com", "/hello", canary)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			require.Contains(t, body, "Name: canary",
				"the header-matched rule outranks the plain prefix on the same path")
		}
		resp, body := hostGet(t, gatewayHTTPAddr, "shop.example.com", "/hello",
			http.Header{"X-Canary": {"never"}})
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.NotContains(t, body, "Name: canary", "a header value the match does not name falls through")
	})

	t.Run("prefix rewrite", func(t *testing.T) {
		resp, body := hostGet(t, gatewayHTTPAddr, "shop.example.com", "/api/orders/1?x=y")
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Contains(t, body, "GET /orders/1?x=y HTTP/1.1", "ReplacePrefixMatch strips the prefix")
		resp, body = hostGet(t, gatewayHTTPAddr, "shop.example.com", "/api")
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Contains(t, body, "GET / HTTP/1.1", "the bare prefix rewrites to the root")
	})

	t.Run("weighted canary", func(t *testing.T) {
		const total = 200
		var canary int
		for range total {
			resp, body := hostGet(t, gatewayHTTPAddr, "shop.example.com", "/canary")
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			if strings.Contains(body, "Name: canary") {
				canary++
			}
		}
		t.Logf("weighted canary: %d of %d requests reached the canary", canary, total)
		require.InDelta(t, total/4, canary, 10, "weights 3:1 apportion one request in four to the canary")
	})

	t.Run("redirect", func(t *testing.T) {
		resp, _ := hostGet(t, gatewayHTTPAddr, "old.example.com", "/some/path?q=1")
		require.Equal(t, http.StatusMovedPermanently, resp.StatusCode)
		require.Equal(t, "https://old.example.com/some/path?q=1", resp.Header.Get("Location"))
	})

	t.Run("websocket echo", func(t *testing.T) {
		waitRoute(t, gatewayHTTPAddr, "bin.example.com", "/get", http.StatusOK, time.Minute)
		config, err := websocket.NewConfig("ws://bin.example.com/websocket/echo", "http://bin.example.com")
		require.NoError(t, err)
		conn, err := net.DialTimeout("tcp", gatewayHTTPAddr, 5*time.Second)
		require.NoError(t, err)
		defer conn.Close()
		ws, err := websocket.NewClient(config, conn)
		require.NoError(t, err, "the upgrade is tunneled through the Gateway")
		defer ws.Close()
		for i := range 3 {
			sent := "hello " + strconv.Itoa(i)
			require.NoError(t, websocket.Message.Send(ws, sent))
			var got string
			require.NoError(t, websocket.Message.Receive(ws, &got))
			require.Equal(t, sent, got)
		}
	})

	t.Run("large body", func(t *testing.T) {
		const size = 10 * 1024 * 1024
		digest := func() string {
			req, err := hostRequest(gatewayHTTPAddr, "bin.example.com",
				"/bytes/"+strconv.Itoa(size)+"?seed=7", nil)
			require.NoError(t, err)
			resp, err := hostClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			h := sha256.New()
			n, err := io.Copy(h, resp.Body)
			require.NoError(t, err)
			require.Equal(t, int64(size), n, "the whole body is relayed")
			return hex.EncodeToString(h.Sum(nil))
		}
		require.Equal(t, digest(), digest(), "a seeded body is relayed intact both times")
	})

	t.Run("cache hit", func(t *testing.T) {
		const result = "X-Trickster-Result"
		resp, body := hostGet(t, gatewayHTTPAddr, "bin.example.com", "/cache/60?scenario=hit")
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Contains(t, resp.Header.Get(result), "engine=ObjectProxyCache",
			"the policy's proxycache handler serves the route: %s", resp.Header.Get(result))
		require.NotContains(t, resp.Header.Get(result), "status=hit")
		resp, body = hostGet(t, gatewayHTTPAddr, "bin.example.com", "/cache/60?scenario=hit")
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Contains(t, resp.Header.Get(result), "status=hit",
			"a response the origin marked cacheable is a hit the second time: %s", resp.Header.Get(result))
	})

	t.Run("prometheus acceleration", func(t *testing.T) {
		const result = "X-Trickster-Result"
		end := time.Now().Truncate(time.Minute)
		query := url.Values{
			"query": {"up"},
			"start": {strconv.FormatInt(end.Add(-5*time.Minute).Unix(), 10)},
			"end":   {strconv.FormatInt(end.Unix(), 10)},
			"step":  {"15"},
		}
		path := "/api/v1/query_range?" + query.Encode()
		waitRoute(t, gatewayHTTPAddr, "prom-gw.example.com", path, http.StatusOK, 3*time.Minute)
		resp, body := hostGet(t, gatewayHTTPAddr, "prom-gw.example.com", path)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Contains(t, resp.Header.Get(result), "engine=DeltaProxyCache",
			"the Service's policy selects the prometheus provider: %s", resp.Header.Get(result))
		resp, body = hostGet(t, gatewayHTTPAddr, "prom-gw.example.com", path)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Contains(t, resp.Header.Get(result), "status=hit", "%s", resp.Header.Get(result))
	})

	t.Run("tls from a secret, rotated under load", func(t *testing.T) {
		const host = "secure.example.com"
		first, serial := tlsSecretManifest(t, "edge-tls", host)
		applyKind(t, first)
		waitServedSerial(t, gatewayHTTPSAddr, host, serial, 2*time.Minute)

		transport := &http.Transport{
			TLSClientConfig:     &tls.Config{ServerName: host, InsecureSkipVerify: true}, //nolint:gosec // self-signed
			MaxIdleConnsPerHost: 4,
		}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
		get := func() (*http.Response, error) {
			req, err := http.NewRequest(http.MethodGet, "https://"+gatewayHTTPSAddr+"/", nil)
			if err != nil {
				return nil, err
			}
			req.Host = host
			return client.Do(req)
		}
		resp, err := get()
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		require.Contains(t, string(body), "Hostname: webecho-", "the HTTPS listener serves the secure route")

		// a rotation costs no reload: the certificate is pushed into the listener's store
		const reloads = "trickster_config_reload_attempts_total"
		before := stableMetric(t, gatewayMetricsAddr, reloads, 2*time.Second)
		load := startLoad(4, get)
		second, rotated := tlsSecretManifest(t, "edge-tls", host)
		applyKind(t, second)
		waitServedSerial(t, gatewayHTTPSAddr, host, rotated, time.Minute)
		time.Sleep(2 * time.Second)
		requests, errors, failures := load.end()
		t.Logf("certificate rotation under load: requests=%d errors=%d; failures: %s",
			requests, errors, failures)
		require.Positive(t, requests)
		require.Zero(t, errors, "rotating the Secret must reset no connection; failures: %s", failures)
		after, ok := metricValue(t, gatewayMetricsAddr, reloads, "")
		require.True(t, ok)
		require.Equal(t, before, after, "a certificate rotation must not reload the configuration")
	})
}
