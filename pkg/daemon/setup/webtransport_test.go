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
	"net/http"
	"path/filepath"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	listenerhttp3 "github.com/trickstercache/trickster/v2/pkg/proxy/listener/http3"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
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
