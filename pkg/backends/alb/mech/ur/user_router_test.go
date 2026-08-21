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

package ur

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	at "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func testRoute(handler http.Handler) UserRoute {
	opts := bo.New()
	opts.Name = "test-route"
	opts.Provider = providers.ReverseProxyShort
	opts.OriginURL = "http://example.com"
	backend, err := backends.New(opts.Name, opts, nil, handler, nil)
	if err != nil {
		panic(err)
	}
	return UserRoute{Backend: backend}
}

// mockAuth implements at.Authenticator for testing
type mockAuth struct {
	username      string
	cred          string
	err           error
	setErr        error
	setCalls      []setCred
	sanitizeCalls atomic.Int64
	sanitizeFn    func(*http.Request)
}

type setCred struct{ user, cred string }

func (m *mockAuth) Authenticate(*http.Request) (*at.AuthResult, error) { return nil, nil }
func (m *mockAuth) ExtractCredentials(*http.Request) (string, string, error) {
	return m.username, m.cred, m.err
}
func (m *mockAuth) SetExtractCredentialsFunc(at.ExtractCredsFunc) {}
func (m *mockAuth) SetCredentials(r *http.Request, u, c string) error {
	m.setCalls = append(m.setCalls, setCred{u, c})
	return m.setErr
}
func (m *mockAuth) SetSetCredentialsFunc(at.SetCredentialsFunc)            {}
func (m *mockAuth) SetObserveOnly(bool)                                    {}
func (m *mockAuth) IsObserveOnly() bool                                    { return false }
func (m *mockAuth) LoadUsers(string, at.CredentialsFileFormat, bool) error { return nil }
func (m *mockAuth) AddUser(string, string) error                           { return nil }
func (m *mockAuth) RemoveUser(string)                                      {}
func (m *mockAuth) Clone() at.Authenticator                                { return m }
func (m *mockAuth) ProxyPreserve() bool                                    { return false }
func (m *mockAuth) Sanitize(r *http.Request) {
	m.sanitizeCalls.Add(1)
	if m.sanitizeFn != nil {
		m.sanitizeFn(r)
	}
}

func TestServeHTTP(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("no username falls through to default", func(t *testing.T) {
		h := &Handler{
			defaultHandler: okHandler,
			options:        &uropt.Options{},
		}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://example.com/", nil)
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("expected %d got %d", http.StatusOK, w.Code)
		}
	})

	t.Run("username from AuthResult routes to user handler", func(t *testing.T) {
		userCalled := false
		userHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			userCalled = true
			w.WriteHeader(http.StatusAccepted)
		})
		h := &Handler{
			defaultHandler: okHandler,
			options: &uropt.Options{
				Users: uropt.UserMappingOptionsByUser{
					"alice": {},
				},
			},
			userRoutes: UserRoutes{"alice": testRoute(userHandler)},
		}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://example.com/", nil)
		rsc := &request.Resources{
			AuthResult: &at.AuthResult{Username: "alice", Status: at.AuthSuccess},
		}
		r = request.SetResources(r, rsc)
		h.ServeHTTP(w, r)
		if !userCalled {
			t.Error("expected user-specific handler to be called")
		}
		if w.Code != http.StatusAccepted {
			t.Errorf("expected %d got %d", http.StatusAccepted, w.Code)
		}
	})

	t.Run("unknown user falls through to default", func(t *testing.T) {
		h := &Handler{
			defaultHandler: okHandler,
			options: &uropt.Options{
				Users: uropt.UserMappingOptionsByUser{
					"alice": {},
				},
			},
			userRoutes: UserRoutes{"alice": testRoute(okHandler)},
		}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://example.com/", nil)
		rsc := &request.Resources{
			AuthResult: &at.AuthResult{Username: "bob", Status: at.AuthSuccess},
		}
		r = request.SetResources(r, rsc)
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("expected %d got %d", http.StatusOK, w.Code)
		}
	})

	t.Run("authenticator extracts credentials", func(t *testing.T) {
		userCalled := false
		userHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			userCalled = true
			w.WriteHeader(http.StatusAccepted)
		})
		h := &Handler{
			authenticator:  &mockAuth{username: "carol", cred: "pass"},
			defaultHandler: okHandler,
			options: &uropt.Options{
				Users: uropt.UserMappingOptionsByUser{
					"carol": {},
				},
			},
			userRoutes: UserRoutes{"carol": testRoute(userHandler)},
		}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://example.com/", nil)
		h.ServeHTTP(w, r)
		if !userCalled {
			t.Error("expected user handler for carol")
		}
	})

	t.Run("credential remapping", func(t *testing.T) {
		auth := &mockAuth{}
		h := &Handler{
			authenticator:      auth,
			defaultHandler:     okHandler,
			enableReplaceCreds: true,
			options: &uropt.Options{
				Users: uropt.UserMappingOptionsByUser{
					"alice": {
						ToUser:       "admin",
						ToCredential: "secret",
					},
				},
			},
			userRoutes: UserRoutes{"alice": testRoute(okHandler)},
		}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://example.com/", nil)
		rsc := &request.Resources{
			AuthResult: &at.AuthResult{Username: "alice", Status: at.AuthSuccess},
		}
		r = request.SetResources(r, rsc)
		h.ServeHTTP(w, r)
		if len(auth.setCalls) != 1 {
			t.Fatalf("expected 1 SetCredentials call, got %d", len(auth.setCalls))
		}
		if auth.setCalls[0].user != "admin" || auth.setCalls[0].cred != "secret" {
			t.Errorf("expected admin/secret, got %s/%s",
				auth.setCalls[0].user, auth.setCalls[0].cred)
		}
	})

	t.Run("credential remapping keeps inbound username for routing", func(t *testing.T) {
		auth := &mockAuth{}
		aliceHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		})
		adminHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
		h := &Handler{
			authenticator:      auth,
			defaultHandler:     okHandler,
			enableReplaceCreds: true,
			options: &uropt.Options{
				Users: uropt.UserMappingOptionsByUser{
					"alice": {
						ToUser:       "admin",
						ToCredential: "secret",
					},
				},
			},
			userRoutes: UserRoutes{
				"alice": testRoute(aliceHandler),
				"admin": testRoute(adminHandler),
			},
		}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://example.com/", nil)
		r = request.SetResources(r, &request.Resources{
			AuthResult: &at.AuthResult{Username: "alice", Status: at.AuthSuccess},
		})

		h.ServeHTTP(w, r)

		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d from alice route", w.Code, http.StatusAccepted)
		}
		if len(auth.setCalls) != 1 {
			t.Fatalf("expected 1 SetCredentials call, got %d", len(auth.setCalls))
		}
		if auth.setCalls[0].user != "admin" || auth.setCalls[0].cred != "secret" {
			t.Errorf("expected outbound admin/secret, got %s/%s",
				auth.setCalls[0].user, auth.setCalls[0].cred)
		}
	})

	t.Run("user in map without runtime route falls to default", func(t *testing.T) {
		defaultCalled := false
		defaultH := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			defaultCalled = true
			w.WriteHeader(http.StatusOK)
		})
		h := &Handler{
			defaultHandler: defaultH,
			options: &uropt.Options{
				Users: uropt.UserMappingOptionsByUser{
					"alice": {},
				},
			},
		}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://example.com/", nil)
		rsc := &request.Resources{
			AuthResult: &at.AuthResult{Username: "alice", Status: at.AuthSuccess},
		}
		r = request.SetResources(r, rsc)
		h.ServeHTTP(w, r)
		if !defaultCalled {
			t.Error("expected default handler when user has no runtime route")
		}
	})

	t.Run("default fallback retains inbound credentials", func(t *testing.T) {
		const inboundAuthorization = "Basic YWxpY2U6aW5ib3VuZA=="
		var observedAuthorization string
		defaultTarget := testRoute(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			observedAuthorization = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		}))
		unavailableTarget := testRoute(http.NotFoundHandler())
		unavailableTarget.Status = fixedRouteStatus(-1)
		auth := &mockAuth{}
		h := &Handler{
			authenticator:      auth,
			enableReplaceCreds: true,
			defaultTarget:      defaultTarget,
			options: &uropt.Options{Users: uropt.UserMappingOptionsByUser{
				"alice": {
					ToBackend: "unavailable", ToUser: "origin-user", ToCredential: "origin-password",
				},
			}},
			userRoutes: UserRoutes{"alice": unavailableTarget},
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		r.Header.Set("Authorization", inboundAuthorization)
		r = request.SetResources(r, &request.Resources{
			AuthResult: &at.AuthResult{Username: "alice", Status: at.AuthSuccess},
		})

		h.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if observedAuthorization != inboundAuthorization {
			t.Fatalf("default backend authorization = %q, want inbound credentials",
				observedAuthorization)
		}
		if len(auth.setCalls) != 0 || auth.sanitizeCalls.Load() != 0 {
			t.Fatalf("fallback rewrote credentials: set calls = %d, sanitize calls = %d",
				len(auth.setCalls), auth.sanitizeCalls.Load())
		}
	})

	// SetCredentials returning an error must not be silently ignored. Dispatch
	// to the mapped target with stale or partial credentials risks leaking the
	// inbound user's credentials to the downstream backend.
	t.Run("SetCredentials error must not dispatch", func(t *testing.T) {
		var targetCalls atomic.Int64
		target := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			targetCalls.Add(1)
			w.WriteHeader(http.StatusAccepted)
		})
		auth := &mockAuth{setErr: errors.New("boom")}
		h := &Handler{
			authenticator:      auth,
			defaultHandler:     okHandler,
			enableReplaceCreds: true,
			options: &uropt.Options{
				Users: uropt.UserMappingOptionsByUser{
					"alice": {
						ToUser:       "admin",
						ToCredential: "secret",
					},
				},
			},
			userRoutes: UserRoutes{"alice": testRoute(target)},
		}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://example.com/", nil)
		r = request.SetResources(r, &request.Resources{
			AuthResult: &at.AuthResult{Username: "alice", Status: at.AuthSuccess},
		})
		h.ServeHTTP(w, r)

		if targetCalls.Load() != 0 {
			t.Errorf("target handler dispatched despite SetCredentials error; got %d calls",
				targetCalls.Load())
		}
	})

	// NoRouteStatusCode must be honored even when no DefaultBackend is configured
	// and no DefaultHandler has been wired up by the client startup path.
	t.Run("NoRouteStatusCode honored without DefaultHandler", func(t *testing.T) {
		h := &Handler{
			noRouteStatusCode: http.StatusNotFound,
		}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://example.com/", nil)
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected %d (NoRouteStatusCode) got %d",
				http.StatusNotFound, w.Code)
		}
	})

	// After credential remap, the downstream backend must not see the original
	// inbound Authorization header alongside the new credential. The router
	// should call the authenticator's Sanitize on the downstream request.
	t.Run("inbound Authorization sanitized after remap", func(t *testing.T) {
		var observedAuthz string
		target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			observedAuthz = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		})
		auth := &mockAuth{
			sanitizeFn: func(r *http.Request) {
				r.Header.Del("Authorization")
			},
		}
		h := &Handler{
			authenticator:      auth,
			defaultHandler:     okHandler,
			enableReplaceCreds: true,
			options: &uropt.Options{
				Users: uropt.UserMappingOptionsByUser{
					"alice": {
						ToUser:       "admin",
						ToCredential: "secret",
					},
				},
			},
			userRoutes: UserRoutes{"alice": testRoute(target)},
		}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://example.com/", nil)
		// inbound: alice's original creds
		r.Header.Set("Authorization", "Basic YWxpY2U6b2xkcHc=") // alice:oldpw
		r = request.SetResources(r, &request.Resources{
			AuthResult: &at.AuthResult{Username: "alice", Status: at.AuthSuccess},
		})
		h.ServeHTTP(w, r)

		if auth.sanitizeCalls.Load() == 0 {
			t.Error("expected Sanitize to be called on downstream request after remap")
		}
		if observedAuthz == "Basic YWxpY2U6b2xkcHc=" {
			t.Errorf("downstream received original inbound Authorization: %q", observedAuthz)
		}
	})
}

func TestResolveRouteProtocolNeutral(t *testing.T) {
	target := testRoute(http.NotFoundHandler())
	h := &Handler{
		enableReplaceCreds: true,
		options: &uropt.Options{Users: uropt.UserMappingOptionsByUser{
			"alice": {ToBackend: "mysql-a", ToUser: "origin-a", ToCredential: "secret"},
		}},
		userRoutes: UserRoutes{"alice": target},
	}

	decision, ok := h.ResolveRoute(backends.RouteInput{
		Username: "alice", Credential: "inbound", Authenticated: true,
	})
	if !ok {
		t.Fatal("ResolveRoute() did not select configured target")
	}
	if decision.Target.Backend != target.Backend {
		t.Fatal("ResolveRoute() selected a different backend")
	}
	if decision.Outcome != backends.RouteOutcomeSelected {
		t.Fatalf("ResolveRoute() outcome = %q", decision.Outcome)
	}
	if !decision.ReplaceCredentials || decision.OutboundUsername != "origin-a" ||
		decision.OutboundCredential != "secret" {
		t.Fatalf("ResolveRoute() decision = %+v", decision)
	}

	h.defaultTarget = target
	decision, ok = h.ResolveRoute(backends.RouteInput{Username: "unknown", Authenticated: true})
	if !ok || decision.Outcome != backends.RouteOutcomeDefault {
		t.Fatalf("default ResolveRoute() = %+v, %t", decision, ok)
	}

	unavailableTarget := target
	unavailableTarget.Status = fixedRouteStatus(-1)
	h.userRoutes["alice"] = unavailableTarget
	decision, ok = h.ResolveRoute(backends.RouteInput{
		Username: "alice", Credential: "inbound", Authenticated: true,
	})
	if ok || decision.Outcome != backends.RouteOutcomeUnavailable || decision.ReplaceCredentials {
		t.Fatalf("isolated ResolveRoute() = %+v, %t", decision, ok)
	}
	decision, ok = h.ResolveRoute(backends.RouteInput{
		Username: "alice", Credential: "inbound", Authenticated: true,
		FallbackOnMappedUnavailable: true,
	})
	if !ok || decision.Outcome != backends.RouteOutcomeDefault || decision.ReplaceCredentials {
		t.Fatalf("HTTP fallback ResolveRoute() = %+v, %t", decision, ok)
	}

	h.defaultTarget.Status = fixedRouteStatus(-1)
	decision, ok = h.ResolveRoute(backends.RouteInput{Username: "unknown", Authenticated: true})
	if ok || decision.Outcome != backends.RouteOutcomeUnavailable {
		t.Fatalf("unavailable ResolveRoute() = %+v, %t", decision, ok)
	}
}

func BenchmarkResolveRouteProtocolNeutral(b *testing.B) {
	target := testRoute(http.NotFoundHandler())
	for _, userCount := range []int{10, 1000, 10000} {
		users := make(uropt.UserMappingOptionsByUser, userCount)
		routes := make(UserRoutes, userCount)
		for i := range userCount {
			username := fmt.Sprintf("user-%05d", i)
			users[username] = &uropt.UserMappingOptions{ToBackend: "mysql-a"}
			routes[username] = target
		}
		h := &Handler{options: &uropt.Options{Users: users}, userRoutes: routes}
		username := fmt.Sprintf("user-%05d", userCount/2)
		b.Run(fmt.Sprintf("users_%d/mapped", userCount), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				decision, ok := h.ResolveRoute(backends.RouteInput{
					Username: username, Authenticated: true,
				})
				if !ok || decision.Outcome != backends.RouteOutcomeSelected {
					b.Fatal("mapped route was not selected")
				}
			}
		})
		h.defaultTarget = target
		b.Run(fmt.Sprintf("users_%d/default", userCount), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				decision, ok := h.ResolveRoute(backends.RouteInput{
					Username: "unknown", Authenticated: true,
				})
				if !ok || decision.Outcome != backends.RouteOutcomeDefault {
					b.Fatal("default route was not selected")
				}
			}
		})
	}
}

type fixedRouteStatus int32

func (s fixedRouteStatus) Get() int32 { return int32(s) }
