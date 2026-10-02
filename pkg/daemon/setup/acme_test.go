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

package setup

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme"
	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/challenge"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
)

const (
	acmeTestMarker    = "backend"
	acmeTestChallenge = challenge.HTTPPathPrefix + "token"
	acmeTestIssuer    = "le"
)

func acmeListenerConfig() *config.Config {
	c := config.NewConfig()
	lo := c.Listeners[listenerconfig.DefaultFrontendName]
	lo.Active, lo.ListenPort, lo.TLSListenPort, lo.ServeTLS = true, 1, 2, true
	b := c.Backends["default"]
	b.ListenerNames = []string{listenerconfig.DefaultFrontendName}
	b.TLS = &to.Options{ACME: &acmeopts.BackendOptions{Issuer: acmeTestIssuer}}
	c.ACME = &acmeopts.Options{Issuers: map[string]*acmeopts.IssuerOptions{acmeTestIssuer: {AgreeToTerms: true}}}
	c.ACME.Initialize()
	return c
}

func serveBody(h http.Handler, path string) (int, string) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code, w.Body.String()
}

func TestDesiredListenersAnswerHTTP01OnPlaintext(t *testing.T) {
	challenge.SetSolver(nil)
	c := acmeListenerConfig()
	routers := map[string]router.Router{listenerconfig.DefaultFrontendName: markerRouter(acmeTestMarker)}
	got := desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter(), nil, nil)
	plain := got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false)]
	secure := got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, true)]

	// a challenge path matching no pending challenge is answered here and never reaches a backend
	if code, body := serveBody(plain.router, acmeTestChallenge); code != http.StatusNotFound || body != "" {
		t.Errorf("plaintext challenge = %d %q; want an empty 404", code, body)
	}
	if _, body := serveBody(plain.router, "/index.html"); body != acmeTestMarker {
		t.Errorf("plaintext route = %q; want the backend", body)
	}
	if _, body := serveBody(secure.router, acmeTestChallenge); body != acmeTestMarker {
		t.Errorf("TLS challenge path = %q; want the backend, since http-01 arrives over plaintext", body)
	}

	// a listener without ACME passes the challenge path through to its backends
	c.ACME = nil
	got = desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter(), nil, nil)
	plain = got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false)]
	if _, body := serveBody(plain.router, acmeTestChallenge); body != acmeTestMarker {
		t.Errorf("challenge path without ACME = %q; want the backend", body)
	}
}

func TestManagementOnlyRoute(t *testing.T) {
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	conf := config.NewConfig()
	deactivateBuiltinListeners(conf)
	frontPort, mgmtPort := availablePort(t), availablePort(t)
	front := conf.Listeners[listenerconfig.DefaultFrontendName]
	front.Active, front.ListenPort, front.ListenAddress = true, frontPort, "127.0.0.1"
	mgmtListener := conf.Listeners[mgmt.ListenerNameMgmt]
	mgmtListener.Active, mgmtListener.ListenPort = true, mgmtPort
	route := mgmtRoute{
		path: conf.MgmtConfig.ACMEHandlerPath, mgmtOnly: true,
		methods: []string{http.MethodGet}, handler: markerRouter(acmeTestMarker),
	}
	routers := map[string]router.Router{listenerconfig.DefaultFrontendName: lm.NewRouter()}
	applyListenerConfigs(conf, nil, routers, http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil,
		group, route)
	waitForListener(t, group, listenerKey(mgmt.ListenerNameMgmt, listenerconfig.ProtocolHTTP, false))
	waitForListener(t, group, listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false))

	get := func(port int) (int, string) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, route.path))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if _, body := get(mgmtPort); body != acmeTestMarker {
		t.Errorf("mgmt listener = %q; want the management-only route", body)
	}
	if _, body := get(frontPort); body == acmeTestMarker {
		t.Error("a management-only route must not be served on proxy listeners")
	}
}

func TestACMERoute(t *testing.T) {
	conf := config.NewConfig()
	if r := acmeRoute(&instance.ServerInstance{}, conf); r.path != "" || r.handler != nil {
		t.Errorf("route without a manager = %+v; want none", r)
	}
	r := acmeRoute(&instance.ServerInstance{ACME: acme.New(nil, nil)}, conf)
	if r.path != mgmt.DefaultACMEHandlerPath || !r.mgmtOnly || r.handler == nil ||
		!slices.Equal(r.methods, []string{http.MethodGet, http.MethodPost}) {
		t.Errorf("route = %+v; want the management-only ACME handler", r)
	}
}
