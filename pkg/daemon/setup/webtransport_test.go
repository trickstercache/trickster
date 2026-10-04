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
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/validate"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	listenerhttp3 "github.com/trickstercache/trickster/v2/pkg/proxy/listener/http3"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/monitor"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/quic-go/quic-go"
)

func TestApplyHTTP3WebTransportFollowsMoQBackends(t *testing.T) {
	dir := t.TempDir()
	keyPath, certPath := filepath.Join(dir, "test.key.pem"), filepath.Join(dir, "test.cert.pem")
	if err := tlstest.WriteTestKeyAndCert(false, keyPath, certPath); err != nil {
		t.Fatal(err)
	}
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	conf := tlsTestConfig(t, keyPath, certPath, availablePort(t))
	conf.Backends["default"].NormalizeListenerNames()
	frontend := conf.Listeners[listenerconfig.DefaultFrontendName]
	frontend.Protocol = listenerconfig.ProtocolHTTP
	frontend.HTTP3 = &listenerconfig.HTTP3Options{
		Enabled: true, ListenAddress: "127.0.0.1", ListenPort: availableUDPPort(t),
	}
	routers := map[string]router.Router{listenerconfig.DefaultFrontendName: markerRouter("h3")}
	applyListenerConfigs(conf, nil, routers, http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	h3Key := listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP3, false)
	waitForListener(t, group, h3Key)
	h, ok := listener.ProtocolServerAs[*listenerhttp3.Handler](group, h3Key)
	if !ok || h.WebTransportEnabled() {
		t.Fatal("an HTTP/3 endpoint routing to no moq backend serves no WebTransport")
	}

	withMoQ := conf.Clone()
	live := bo.New()
	live.Name, live.Provider, live.OriginURL = "live", providers.MoQ, "moqt://127.0.0.1:1/live"
	live.ListenerNames = []string{listenerconfig.DefaultFrontendName}
	live.NormalizeListenerNames()
	withMoQ.Backends["live"] = live
	applyListenerConfigs(withMoQ, conf, routers, http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	if same, ok := listener.ProtocolServerAs[*listenerhttp3.Handler](group, h3Key); !ok || same != h ||
		!h.WebTransportEnabled() {
		t.Error("a reload binding a moq backend turns WebTransport on without restarting the endpoint")
	}

	applyListenerConfigs(conf, withMoQ, routers, http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	if h.WebTransportEnabled() {
		t.Error("a reload unbinding the last moq backend turns WebTransport off for new connections")
	}
}

func TestMoQListenerRuntimeCerts(t *testing.T) {
	// a moq listener the Kubernetes controller serves takes its certificates at runtime, as an HTTPS one does
	conf := config.NewConfig()
	port := availableUDPPort(t)
	conf.Listeners["moq1"] = &listenerconfig.Options{
		Protocol: listenerconfig.ProtocolMoQ, ListenAddress: "127.0.0.1", ListenPort: port, TLSRuntimeCerts: true,
	}
	live := bo.New()
	live.Name, live.Provider, live.OriginURL = "live", providers.MoQ, "moqt://127.0.0.1:1/live"
	live.ListenerNames = []string{"moq1"}
	conf.Backends = bo.Lookup{"live": live}
	if err := validate.Listeners(conf); err != nil {
		t.Fatal(err)
	}
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	applyListenerConfigs(conf, nil, map[string]router.Router{}, http.NotFoundHandler(), lm.NewRouter(), nil, nil,
		nil, group)
	waitForListener(t, group, listenerKey("moq1", listenerconfig.ProtocolMoQ, false))
	m := monitor.New()
	t.Cleanup(m.Close)
	m.Apply(conf, group)
	if !slices.Contains(m.TLSListeners(), "moq1") {
		t.Fatalf("the moq listener takes runtime certificates: %v", m.TLSListeners())
	}
	const serverName = "moq.example.com"
	handshake := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := quic.DialAddr(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &tls.Config{
			InsecureSkipVerify: true, ServerName: serverName, // #nosec G402 -- test certificate
			NextProtos: []string{codec.ALPNFor(21)}, MinVersion: tls.VersionTLS13,
		}, nil)
		if err != nil {
			return err
		}
		return conn.CloseWithError(0, "")
	}
	if handshake() == nil {
		t.Fatal("a handshake succeeded before any certificate was installed")
	}
	key, cert, err := tlstest.GetTestKeyAndCertWithNames(serverName)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetMemoryCert("moq1", "default/moq-tls", cert, key); err != nil {
		t.Fatal(err)
	}
	if err := handshake(); err != nil {
		t.Errorf("the installed certificate serves the handshake: %v", err)
	}
}

func TestHTTP3ListenerRuntimeCerts(t *testing.T) {
	// the HTTP/3 endpoint of an HTTPS listener the Kubernetes controller serves takes the runtime certificates too
	conf := config.NewConfig()
	udpPort := availableUDPPort(t)
	conf.Listeners["web"] = &listenerconfig.Options{
		ListenAddress: "127.0.0.1", ListenPort: availablePort(t), TLSListenAddress: "127.0.0.1",
		TLSListenPort: availablePort(t), TLSRuntimeCerts: true,
		HTTP3: &listenerconfig.HTTP3Options{Enabled: true, ListenAddress: "127.0.0.1", ListenPort: udpPort},
	}
	live := bo.New()
	live.Name, live.Provider, live.OriginURL = "live", providers.MoQ, "https://127.0.0.1:1/live"
	live.ListenerNames = []string{"web"}
	conf.Backends = bo.Lookup{"live": live}
	if err := validate.Listeners(conf); err != nil {
		t.Fatal(err)
	}
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	routers := map[string]router.Router{"web": lm.NewRouter()}
	applyListenerConfigs(conf, nil, routers, http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	waitForListener(t, group, listenerKey("web", listenerconfig.ProtocolHTTP3, false))
	m := monitor.New()
	t.Cleanup(m.Close)
	m.Apply(conf, group)
	const serverName = "live.example.com"
	handshake := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := quic.DialAddr(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(udpPort)), &tls.Config{
			InsecureSkipVerify: true, ServerName: serverName, // #nosec G402 -- test certificate
			NextProtos: []string{listenerhttp3.ALPN}, MinVersion: tls.VersionTLS13,
		}, nil)
		if err != nil {
			return err
		}
		return conn.CloseWithError(0, "")
	}
	if handshake() == nil {
		t.Fatal("a handshake succeeded before any certificate was installed")
	}
	key, cert, err := tlstest.GetTestKeyAndCertWithNames(serverName)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetMemoryCert("web", "default/web-tls", cert, key); err != nil {
		t.Fatal(err)
	}
	if err := handshake(); err != nil {
		t.Errorf("the installed certificate serves the HTTP/3 handshake: %v", err)
	}
}
