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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	testtls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/quic-go/quic-go/http3"
	"github.com/stretchr/testify/require"
)

// h3Harness boots Trickster with a TLS listener plus an HTTP/3 endpoint in
// front of origin, returning the harness, the HTTPS address, the UDP port, and
// a cert pool that trusts the generated self-signed certificate.
func h3Harness(t *testing.T, originURL string) (tricksterHarness, string, int, *x509.CertPool) {
	t.Helper()
	keyPEM, certPEM, err := testtls.GetTestKeyAndCertWithNames("localhost")
	require.NoError(t, err)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(certPEM), "test certificate must parse")

	tlsPorts, releaseTLS := portutil.Reserve(t, 1)
	udpPorts, releaseUDP := portutil.ReserveUDP(t, 1)
	tlsPort, udpPort := tlsPorts[0], udpPorts[0]

	h := configHarness(t, func(c *tkconfig.Config) {
		if c.Backends == nil {
			c.Backends = make(bo.Lookup)
		}
		b := bo.New()
		b.Name = "h3proxy"
		b.Provider = providers.ReverseProxy
		b.OriginURL = originURL
		b.CacheName = "default"
		b.ListenerNames = []string{listenerconfig.DefaultFrontendName}
		b.TLS = &to.Options{
			ServeTLS:          true,
			FullChainCertPath: certPath,
			PrivateKeyPath:    keyPath,
		}
		c.Backends["h3proxy"] = b

		l := c.Listeners[listenerconfig.DefaultFrontendName]
		require.NotNil(t, l, "default listener must exist")
		l.TLSListenAddress = "127.0.0.1"
		l.TLSListenPort = tlsPort
		l.HTTP3 = &listenerconfig.HTTP3Options{
			Enabled:       true,
			ListenAddress: "127.0.0.1",
			ListenPort:    udpPort,
		}
	})
	// the reservations must be released before Trickster binds them
	releaseTLS()
	releaseUDP()
	h.start(t)
	return h, fmt.Sprintf("127.0.0.1:%d", tlsPort), udpPort, pool
}

func h3Client(pool *x509.CertPool) *http.Client {
	return &http.Client{Transport: &http3.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13},
	}}
}

func TestHTTP3ServesRequests(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "served %s", r.URL.Path)
	}))
	defer origin.Close()

	_, httpsAddr, udpPort, pool := h3Harness(t, origin.URL)

	client := h3Client(pool)
	defer client.Transport.(*http3.Transport).Close()

	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/h3proxy/hello", udpPort))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "HTTP/3.0", resp.Proto, "request must have been served over HTTP/3")
	require.Equal(t, "served /hello", string(body))
	require.NotEmpty(t, httpsAddr)
}

func TestHTTP3AltSvcAdvertised(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer origin.Close()

	_, httpsAddr, udpPort, pool := h3Harness(t, origin.URL)

	// the TLS/TCP endpoint is what advertises the alternative service
	tcpClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
	resp, err := tcpClient.Get("https://" + httpsAddr + "/h3proxy/hello")
	require.NoError(t, err)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	altSvc := resp.Header.Get("Alt-Svc")
	require.Contains(t, altSvc, fmt.Sprintf(`h3=":%d"`, udpPort),
		"TLS responses must advertise the HTTP/3 port so clients can upgrade")
}

// TestHTTP3ProtocolParity is the assertion that actually protects the proxy
// engines: the same request over HTTP/1.1, HTTP/2 and HTTP/3 must produce the
// same body and the same Trickster result status.
func TestHTTP3ProtocolParity(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(strings.Repeat("p", 4096)))
	}))
	defer origin.Close()

	_, httpsAddr, udpPort, pool := h3Harness(t, origin.URL)

	h3c := h3Client(pool)
	defer h3c.Transport.(*http3.Transport).Close()

	h1c := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: false,
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
	}}
	h2c := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}}

	type result struct {
		proto, body, trkResult string
	}
	fetch := func(c *http.Client, url string) result {
		resp, err := c.Get(url)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return result{resp.Proto, string(b), resp.Header.Get("X-Trickster-Result")}
	}

	over1 := fetch(h1c, "https://"+httpsAddr+"/h3proxy/parity")
	over2 := fetch(h2c, "https://"+httpsAddr+"/h3proxy/parity")
	over3 := fetch(h3c, fmt.Sprintf("https://127.0.0.1:%d/h3proxy/parity", udpPort))

	require.Equal(t, "HTTP/1.1", over1.proto)
	require.Equal(t, "HTTP/2.0", over2.proto)
	require.Equal(t, "HTTP/3.0", over3.proto)

	require.Equal(t, over1.body, over3.body, "HTTP/3 body must match HTTP/1.1")
	require.Equal(t, over2.body, over3.body, "HTTP/3 body must match HTTP/2")
	require.Equal(t, over1.trkResult, over3.trkResult,
		"HTTP/3 must produce the same Trickster result as HTTP/1.1")
}

func TestHTTP3ByteRangeParity(t *testing.T) {
	full := strings.Repeat("abcdefghij", 512)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "obj.txt", time.Time{}, strings.NewReader(full))
	}))
	defer origin.Close()

	_, httpsAddr, udpPort, pool := h3Harness(t, origin.URL)
	h3c := h3Client(pool)
	defer h3c.Transport.(*http3.Transport).Close()
	tcpClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}

	rangeGet := func(c *http.Client, url string) (int, string) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		require.NoError(t, err)
		req.Header.Set("Range", "bytes=10-19")
		resp, err := c.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(b)
	}

	tcpCode, tcpBody := rangeGet(tcpClient, "https://"+httpsAddr+"/h3proxy/obj.txt")
	h3Code, h3Body := rangeGet(h3c, fmt.Sprintf("https://127.0.0.1:%d/h3proxy/obj.txt", udpPort))

	require.Equal(t, tcpCode, h3Code, "range status must match across protocols")
	require.Equal(t, tcpBody, h3Body, "range body must match across protocols")
	require.Equal(t, full[10:20], h3Body)
}

func TestHTTP3RefusesUpgrade(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer origin.Close()

	_, _, udpPort, pool := h3Harness(t, origin.URL)
	h3c := h3Client(pool)
	defer h3c.Transport.(*http3.Transport).Close()

	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("https://127.0.0.1:%d/h3proxy/socket", udpPort), nil)
	require.NoError(t, err)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	// RFC 9114 4.2 makes connection-specific headers malformed on HTTP/3, so
	// this either fails at the client or is rejected server-side; either way it
	// must not hang or produce a half-open tunnel
	resp, err := h3c.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	require.NotEqual(t, http.StatusSwitchingProtocols, resp.StatusCode,
		"HTTP/3 cannot carry a 101 upgrade")
}
