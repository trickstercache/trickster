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
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/ready"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
)

func mustList(t *testing.T, o ipacl.Options) *ipacl.List {
	t.Helper()
	list, _, err := ipacl.Compile(o)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func routeBody(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
}

func serve(h http.Handler, method, path, remote, forwarded string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	if forwarded != "" {
		req.Header.Set(headers.NameXForwardedFor, forwarded)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestListenerIPACL(t *testing.T) {
	deny := mustList(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Status: http.StatusTooManyRequests})
	allow := mustList(t, ipacl.Options{Allow: []string{"192.0.2.9"}})

	proxyWith := func(t *testing.T, list *ipacl.List, readyPath string) http.Handler {
		t.Helper()
		c := config.NewConfig()
		c.Listeners[listenerconfig.DefaultFrontendName].IPACL = list
		c.MgmtConfig.ReadyHandlerPath = readyPath
		raw := lm.NewRouter()
		if err := raw.RegisterRoute("/api", nil, nil, matching.PathMatchTypeExact, routeBody("route")); err != nil {
			t.Fatal(err)
		}
		if err := raw.RegisterRoute(readyPath, nil, nil, matching.PathMatchTypeExact, routeBody("backend")); err != nil {
			t.Fatal(err)
		}
		if err := raw.RegisterRoute(mgmt.DefaultPingHandlerPath, nil,
			[]string{http.MethodGet}, matching.PathMatchTypeExact, routeBody("pong")); err != nil {
			t.Fatal(err)
		}
		reserved := []mgmtRoute{{path: readyPath, handler: ready.HandlerFunc(&ready.State{}, nil)}}
		routers := map[string]router.Router{listenerconfig.DefaultFrontendName: raw}
		got := desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter(), nil, reserved)
		proxy := got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false)]
		return proxy.router
	}

	t.Run("deny", func(t *testing.T) {
		h := proxyWith(t, deny, mgmt.DefaultReadyHandlerPath)
		w := serve(h, http.MethodGet, "/api", "192.0.2.9:1", "")
		if w.Code != http.StatusTooManyRequests || w.Body.String() == "route" {
			t.Fatalf("deny = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("allow", func(t *testing.T) {
		h := proxyWith(t, allow, mgmt.DefaultReadyHandlerPath)
		w := serve(h, http.MethodGet, "/api", "192.0.2.9:1", "")
		if w.Code != http.StatusOK || w.Body.String() != "route" {
			t.Fatalf("allow = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("readiness", func(t *testing.T) {
		h := proxyWith(t, deny, mgmt.DefaultReadyHandlerPath)
		req := httptest.NewRequest(http.MethodGet, mgmt.DefaultReadyHandlerPath, nil)
		req.RemoteAddr = "192.0.2.9:1"
		req.Host = "api.example.com"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable || w.Body.String() != ready.BodyNotReady {
			t.Fatalf("ready = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("ping", func(t *testing.T) {
		h := proxyWith(t, deny, mgmt.DefaultReadyHandlerPath)
		w := serve(h, http.MethodGet, mgmt.DefaultPingHandlerPath, "192.0.2.9:1", "")
		if w.Code != http.StatusTooManyRequests || w.Body.String() == "pong" {
			t.Fatalf("ping = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("custom ready path", func(t *testing.T) {
		h := proxyWith(t, deny, "/custom-ready")
		w := serve(h, http.MethodGet, "/custom-ready", "192.0.2.9:1", "")
		if w.Code != http.StatusServiceUnavailable || w.Body.String() != ready.BodyNotReady {
			t.Fatalf("custom ready = %d %q", w.Code, w.Body.String())
		}
		w = serve(h, http.MethodGet, mgmt.DefaultReadyHandlerPath, "192.0.2.9:1", "")
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("default ready path = %d; want the listener list", w.Code)
		}
	})

	t.Run("client ip", func(t *testing.T) {
		c := config.NewConfig()
		front := c.Listeners[listenerconfig.DefaultFrontendName]
		front.IPACL = allow
		front.TrustedProxies = []string{"10.1.1.1"}
		raw := lm.NewRouter()
		if err := raw.RegisterRoute("/api", nil, nil, matching.PathMatchTypeExact, routeBody("route")); err != nil {
			t.Fatal(err)
		}
		routers := map[string]router.Router{listenerconfig.DefaultFrontendName: raw}
		got := desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter(), nil, nil)
		h := got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false)].router
		w := serve(h, http.MethodGet, "/api", "10.1.1.1:9", "192.0.2.9")
		if w.Code != http.StatusOK || w.Body.String() != "route" {
			t.Fatalf("forwarded client = %d %q", w.Code, w.Body.String())
		}
		w = serve(h, http.MethodGet, "/api", "10.1.1.1:9", "198.51.100.8")
		if w.Code != http.StatusForbidden {
			t.Fatalf("forwarded client = %d; want the resolved address denied", w.Code)
		}
	})

	t.Run("peer", func(t *testing.T) {
		c := config.NewConfig()
		front := c.Listeners[listenerconfig.DefaultFrontendName]
		front.IPACL = mustList(t, ipacl.Options{Allow: []string{"10.1.1.1"}, Source: "peer"})
		front.TrustedProxies = []string{"10.1.1.1"}
		raw := lm.NewRouter()
		if err := raw.RegisterRoute("/api", nil, nil, matching.PathMatchTypeExact, routeBody("route")); err != nil {
			t.Fatal(err)
		}
		routers := map[string]router.Router{listenerconfig.DefaultFrontendName: raw}
		got := desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter(), nil, nil)
		h := got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false)].router
		w := serve(h, http.MethodGet, "/api", "10.1.1.1:9", "192.0.2.9")
		if w.Code != http.StatusOK || w.Body.String() != "route" {
			t.Fatalf("peer allow = %d %q", w.Code, w.Body.String())
		}
		front.IPACL = mustList(t, ipacl.Options{Allow: []string{"192.0.2.9"}, Source: "peer"})
		got = desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter(), nil, nil)
		h = got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false)].router
		w = serve(h, http.MethodGet, "/api", "10.1.1.1:9", "192.0.2.9")
		if w.Code != http.StatusForbidden {
			t.Fatalf("peer deny = %d; forwarded client must not be used", w.Code)
		}
	})
}

func TestManagementListenerIPACLExemptsReadiness(t *testing.T) {
	c := config.NewConfig()
	c.Listeners[mgmt.ListenerNameMgmt].IPACL = mustList(t, ipacl.Options{
		Allow: []string{"10.0.0.0/8"}, Status: http.StatusTooManyRequests,
	})
	mgmtRouter := lm.NewRouter()
	readyPath := c.MgmtConfig.ReadyHandlerPath
	if err := mgmtRouter.RegisterRoute(readyPath, nil, nil, matching.PathMatchTypeExact,
		ready.HandlerFunc(&ready.State{}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := mgmtRouter.RegisterRoute("/api", nil, nil, matching.PathMatchTypeExact, routeBody("route")); err != nil {
		t.Fatal(err)
	}
	got := desiredListeners(c, nil, mgmtRouter, lm.NewRouter(), nil,
		[]mgmtRoute{{path: readyPath, handler: ready.HandlerFunc(&ready.State{}, nil)}})
	h := got[listenerKey(mgmt.ListenerNameMgmt, listenerconfig.ProtocolHTTP, false)].router
	w := serve(h, http.MethodGet, readyPath, "192.0.2.9:1", "")
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != ready.BodyNotReady {
		t.Fatalf("mgmt ready = %d %q", w.Code, w.Body.String())
	}
	w = serve(h, http.MethodGet, "/api", "192.0.2.9:1", "")
	if w.Code != http.StatusTooManyRequests || w.Body.String() == "route" {
		t.Fatalf("mgmt route = %d %q", w.Code, w.Body.String())
	}
}
