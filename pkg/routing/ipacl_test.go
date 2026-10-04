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

package routing

import (
	"net/http"
	"net/http/httptest"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	aclhandler "github.com/trickstercache/trickster/v2/pkg/proxy/ipacl/handler"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func mustList(t *testing.T, o ipacl.Options) *ipacl.List {
	t.Helper()
	list, _, err := ipacl.Compile(o)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

type calledHandler struct {
	called bool
}

func (h *calledHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.called = true
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("cached"))
}

type countingAuthenticator struct {
	calls int
}

func (a *countingAuthenticator) Authenticate(*http.Request) (*types.AuthResult, error) {
	a.calls++
	return &types.AuthResult{Status: types.AuthSuccess}, nil
}

func (*countingAuthenticator) ExtractCredentials(*http.Request) (string, string, error) {
	return "", "", nil
}
func (*countingAuthenticator) SetExtractCredentialsFunc(types.ExtractCredsFunc) {}
func (*countingAuthenticator) SetCredentials(*http.Request, string, string) error {
	return nil
}
func (*countingAuthenticator) SetSetCredentialsFunc(types.SetCredentialsFunc) {}
func (*countingAuthenticator) SetObserveOnly(bool)                            {}
func (*countingAuthenticator) IsObserveOnly() bool                            { return false }

func (*countingAuthenticator) LoadUsers(string, types.CredentialsFileFormat, bool) error { return nil }

func (*countingAuthenticator) AddUser(string, string) error { return nil }
func (*countingAuthenticator) RemoveUser(string)            {}
func (a *countingAuthenticator) Clone() types.Authenticator { return a }

func (*countingAuthenticator) ProxyPreserve() bool    { return false }
func (*countingAuthenticator) Sanitize(*http.Request) {}

func TestRouteIPACL(t *testing.T) {
	office := mustList(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Status: http.StatusTooManyRequests})
	partners := mustList(t, ipacl.Options{Allow: []string{"192.0.2.9"}, Status: http.StatusForbidden})

	serve := func(t *testing.T, backend *bo.Options, path *po.Options, remote string) (int, string, bool) {
		t.Helper()
		cache := &calledHandler{}
		path.Handler = cache
		path.NoMetrics = true
		h := applyMiddleware(backend, path, nil, nil, nil, routeLogging{}, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "http://example"+path.Path, nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String(), cache.called
	}

	backend := &bo.Options{Name: "api", Provider: "prometheus", IPACLName: "office", IPACL: office}
	path := &po.Options{Path: "/api/v1/query"}

	t.Run("backend deny", func(t *testing.T) {
		code, body, called := serve(t, backend, path, "192.0.2.9:1")
		if code != http.StatusTooManyRequests || body == "cached" || called {
			t.Fatalf("backend deny = %d %q called=%v", code, body, called)
		}
	})

	t.Run("backend allow", func(t *testing.T) {
		code, body, called := serve(t, backend, path, "10.1.2.3:1")
		if code != http.StatusOK || body != "cached" || !called {
			t.Fatalf("backend allow = %d %q called=%v", code, body, called)
		}
	})

	t.Run("inherit", func(t *testing.T) {
		inherited := &po.Options{Path: "/api/v1/query", IPACLName: ""}
		code, _, called := serve(t, backend, inherited, "192.0.2.9:1")
		if code != http.StatusTooManyRequests || called {
			t.Fatalf("inherit = %d called=%v", code, called)
		}
	})

	t.Run("path override", func(t *testing.T) {
		over := &po.Options{Path: "/admin/", IPACLName: "partners", IPACL: partners}
		code, _, called := serve(t, backend, over, "10.1.2.3:1")
		if code != http.StatusForbidden || called {
			t.Fatalf("path deny over backend allow = %d called=%v", code, called)
		}
		code, body, called := serve(t, backend, over, "192.0.2.9:1")
		if code != http.StatusOK || body != "cached" || !called {
			t.Fatalf("path allow over backend deny = %d %q called=%v", code, body, called)
		}
	})

	t.Run("none", func(t *testing.T) {
		cleared := &po.Options{Path: "/public/", IPACLName: reserved.ReferenceNone}
		code, body, called := serve(t, backend, cleared, "192.0.2.9:1")
		if code != http.StatusOK || body != "cached" || !called {
			t.Fatalf("none = %d %q called=%v", code, body, called)
		}
	})
}

func TestRouteIPACLBeforeAuthenticator(t *testing.T) {
	list := mustList(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Status: http.StatusTooManyRequests})
	auth := &countingAuthenticator{}
	backend := &bo.Options{
		Name: "api", Provider: "prometheus", IPACL: list,
		AuthOptions: &autho.Options{Name: "basic", Authenticator: auth},
	}
	cache := &calledHandler{}
	path := &po.Options{Path: "/api", Handler: cache, NoMetrics: true}
	h := applyMiddleware(backend, path, nil, nil, nil, routeLogging{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.RemoteAddr = "192.0.2.9:1"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests || auth.calls != 0 || cache.called {
		t.Fatalf("denied before auth: status=%d auth=%d cache=%v", w.Code, auth.calls, cache.called)
	}

	req.RemoteAddr = "10.1.2.3:1"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || auth.calls != 1 || !cache.called {
		t.Fatalf("allowed reaches auth: status=%d auth=%d cache=%v", w.Code, auth.calls, cache.called)
	}
}

func TestRouteACLScopes(t *testing.T) {
	office := mustList(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}})
	partners := mustList(t, ipacl.Options{Allow: []string{"192.0.2.9"}})
	backend := &bo.Options{IPACLName: "office", IPACL: office}
	for _, test := range []struct {
		name          string
		path          *po.Options
		backend       *bo.Options
		list          *ipacl.List
		aclName, want string
	}{
		{"inherited", &po.Options{}, backend, office, "office", aclhandler.ScopeBackend},
		{"replaced", &po.Options{IPACLName: "partners", IPACL: partners}, backend, partners, "partners",
			aclhandler.ScopePath},
		{"cleared", &po.Options{IPACLName: reserved.ReferenceNone}, backend, nil, "", ""},
		{"no backend list", &po.Options{}, &bo.Options{}, nil, "", ""},
		{"no backend", &po.Options{IPACLName: "partners", IPACL: partners}, nil, partners, "partners",
			aclhandler.ScopePath},
		{"no path", nil, backend, nil, "", ""},
	} {
		list, name, scope := routeACL(test.path, test.backend)
		if list != test.list || name != test.aclName || scope != test.want {
			t.Errorf("%s: got %p %q %q, want %p %q %q", test.name, list, name, scope, test.list, test.aclName, test.want)
		}
	}
}
