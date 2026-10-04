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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	configtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
	"github.com/stretchr/testify/require"
)

const (
	moqWebBackend = "moqweb"
	moqWebUser    = "viewer"
	moqWebSecret  = "viewer-secret"
)

type moqWebHarness struct {
	tricksterHarness
	h3Addr string
	pool   *x509.CertPool
}

func moqWebTransportHarness(t *testing.T, origin *moqOrigin, site string, authenticated bool) moqWebHarness {
	t.Helper()
	// a moq backend for origin on the default listener's HTTP/3 endpoint, beside a reverse proxy backend for site
	// so plain HTTP/3 requests share the endpoint
	serve, pool := moqServingTLS(t)
	serve.InsecureSkipVerify = true
	tlsPorts, releaseTLS := portutil.Reserve(t, 1)
	udpPorts, releaseUDP := portutil.ReserveUDP(t, 1)
	h := configHarness(t, func(c *tkconfig.Config) {
		l := c.Listeners[listenerconfig.DefaultFrontendName]
		l.TLSListenAddress, l.TLSListenPort = "127.0.0.1", tlsPorts[0]
		l.HTTP3 = &listenerconfig.HTTP3Options{Enabled: true, ListenAddress: "127.0.0.1", ListenPort: udpPorts[0]}
		b := bo.New()
		b.Name, b.Provider, b.CacheName = moqWebBackend, providers.MoQ, "default"
		b.OriginURL = fmt.Sprintf("moqt://%s/live", origin.addr)
		b.ListenerNames, b.TLS, b.MoQ = []string{listenerconfig.DefaultFrontendName}, serve, mo.New()
		if authenticated {
			b.AuthenticatorName = moqWebBackend
			c.Authenticators[moqWebBackend] = &autho.Options{
				Provider: "basic", Users: configtypes.EnvStringMap{moqWebUser: moqWebSecret},
			}
		}
		c.Backends[moqWebBackend] = b
		s := bo.New()
		s.Name, s.Provider, s.OriginURL, s.CacheName = "h3site", providers.ReverseProxy, site, "default"
		s.ListenerNames, s.TLS = []string{listenerconfig.DefaultFrontendName}, serve
		c.Backends["h3site"] = s
	})
	releaseTLS()
	releaseUDP()
	h.start(t)
	return moqWebHarness{tricksterHarness: h, h3Addr: fmt.Sprintf("127.0.0.1:%d", udpPorts[0]), pool: pool}
}

func (h moqWebHarness) tlsConfig() *tls.Config {
	return &tls.Config{RootCAs: h.pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}
}

func (h moqWebHarness) dial(t *testing.T, header http.Header) (*http.Response, *session.Session, error) {
	t.Helper()
	// a MoQ session by WebTransport through the backend's path-routed URL
	tr := &webtransport.Transport{TLSClientConfig: h.tlsConfig(), ApplicationProtocols: codec.ALPNs(codec.ClientOffer)}
	ctx, cancel := context.WithTimeout(context.Background(), moqWait)
	defer cancel()
	rsp, ws, err := tr.Dial(ctx, "https://"+h.h3Addr+"/"+moqWebBackend+"/live", header)
	if err != nil {
		return rsp, nil, err
	}
	c, ok := codec.ForALPN(ws.SessionState().ApplicationProtocol)
	require.True(t, ok, "a draft was negotiated")
	s, err := session.New(session.FromWebTransport(ws), session.Options{
		Role: session.RoleClient, Codec: c, WebTransport: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	return rsp, s, s.Start(ctx)
}

func TestMoQWebTransport(t *testing.T) {
	origin := startMoQOrigin(t)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "plain over h3")
	}))
	t.Cleanup(site.Close)
	h := moqWebTransportHarness(t, origin, site.URL, false)
	const sessionSeries = `backend_name="moqweb",draft="21",listener_name="default",result="ok",role="downstream",` +
		`transport="webtransport"`
	// the counter outlives the daemon in this process, so only its change is this test's
	before, _ := metricValue(t, h.MetricsAddr, "trickster_moq_sessions_total", sessionSeries)

	_, first, err := h.dial(t, nil)
	require.NoError(t, err)
	_, second, err := h.dial(t, nil)
	require.NoError(t, err)
	a, b := moqSubscribe(t, first), moqSubscribe(t, second)
	require.Equal(t, int32(1), origin.subscribes.Load(), "browser sessions share one upstream subscription")
	origin.publish(t, "/live", 0, "wt-key", "wt-delta")
	for _, r := range []*moqReceived{a, b} {
		require.Equal(t, "wt-key", moqNext(t, r.objects, "a key object"))
		require.Equal(t, "wt-delta", moqNext(t, r.objects, "a delta object"))
	}
	sessions, found := metricValue(t, h.MetricsAddr, "trickster_moq_sessions_total", sessionSeries)
	require.True(t, found, "WebTransport sessions are counted")
	require.Equal(t, before+2, sessions)

	// plain HTTP/3 requests are served on the endpoint that now carries WebTransport
	client := &http.Client{Transport: &http3.Transport{TLSClientConfig: h.tlsConfig()}, Timeout: moqWait}
	resp, err := client.Get("https://" + h.h3Addr + "/h3site/")
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "plain over h3", string(body))
}

func TestMoQWebTransportAuthenticated(t *testing.T) {
	origin := startMoQOrigin(t)
	site := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(site.Close)
	h := moqWebTransportHarness(t, origin, site.URL, true)

	rsp, _, err := h.dial(t, nil)
	require.Error(t, err, "the backend's authenticator gates the CONNECT")
	require.NotNil(t, rsp)
	require.Equal(t, http.StatusUnauthorized, rsp.StatusCode)

	req, _ := http.NewRequest(http.MethodConnect, "https://"+h.h3Addr, nil)
	req.SetBasicAuth(moqWebUser, moqWebSecret)
	_, s, err := h.dial(t, req.Header)
	require.NoError(t, err, "valid credentials open the session")
	r := moqSubscribe(t, s)
	origin.publish(t, "/live", 0, "authorized")
	require.Equal(t, "authorized", moqNext(t, r.objects, "an object"))
}
