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
	"net/http"
	"net/http/httptest"
	"testing"

	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	at "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// mockAuth implements at.Authenticator for testing
type mockAuth struct {
	username string
	cred     string
	err      error
	setCalls []setCred
}

type setCred struct{ user, cred string }

func (m *mockAuth) Authenticate(*http.Request) (*at.AuthResult, error) { return nil, nil }
func (m *mockAuth) ExtractCredentials(*http.Request) (string, string, error) {
	return m.username, m.cred, m.err
}
func (m *mockAuth) SetExtractCredentialsFunc(at.ExtractCredsFunc) {}
func (m *mockAuth) SetCredentials(r *http.Request, u, c string) error {
	m.setCalls = append(m.setCalls, setCred{u, c})
	return nil
}
func (m *mockAuth) SetSetCredentialsFunc(at.SetCredentialsFunc)            {}
func (m *mockAuth) SetObserveOnly(bool)                                    {}
func (m *mockAuth) IsObserveOnly() bool                                    { return false }
func (m *mockAuth) LoadUsers(string, at.CredentialsFileFormat, bool) error { return nil }
func (m *mockAuth) AddUser(string, string) error                           { return nil }
func (m *mockAuth) RemoveUser(string)                                      {}
func (m *mockAuth) Clone() at.Authenticator                                { return m }
func (m *mockAuth) ProxyPreserve() bool                                    { return false }
func (m *mockAuth) Sanitize(*http.Request)                                 {}

func TestHandleDefaultNilHandler(t *testing.T) {
	// Before the fix, this would panic with a nil pointer dereference
	// because handleDefault did not return after calling HandleBadGateway.
	h := &Handler{
		options: &uropt.Options{
			DefaultHandler: nil,
		},
	}
	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "http://example.com/", nil)
	h.handleDefault(w, r)
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected %d got %d", http.StatusBadGateway, w.Code)
	}
}

func TestHandleDefaultWithHandler(t *testing.T) {
	called := false
	h := &Handler{
		options: &uropt.Options{
			DefaultHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}),
		},
	}
	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "http://example.com/", nil)
	h.handleDefault(w, r)
	if !called {
		t.Error("expected DefaultHandler to be called")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, w.Code)
	}
}

func TestServeHTTP(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("no username falls through to default", func(t *testing.T) {
		h := &Handler{
			options: &uropt.Options{
				DefaultHandler: okHandler,
			},
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
			options: &uropt.Options{
				DefaultHandler: okHandler,
				Users: uropt.UserMappingOptionsByUser{
					"alice": {ToHandler: userHandler},
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
		if !userCalled {
			t.Error("expected user-specific handler to be called")
		}
		if w.Code != http.StatusAccepted {
			t.Errorf("expected %d got %d", http.StatusAccepted, w.Code)
		}
	})

	t.Run("unknown user falls through to default", func(t *testing.T) {
		h := &Handler{
			options: &uropt.Options{
				DefaultHandler: okHandler,
				Users: uropt.UserMappingOptionsByUser{
					"alice": {ToHandler: okHandler},
				},
			},
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
			authenticator: &mockAuth{username: "carol", cred: "pass"},
			options: &uropt.Options{
				DefaultHandler: okHandler,
				Users: uropt.UserMappingOptionsByUser{
					"carol": {ToHandler: userHandler},
				},
			},
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
			enableReplaceCreds: true,
			options: &uropt.Options{
				DefaultHandler: okHandler,
				Users: uropt.UserMappingOptionsByUser{
					"alice": {
						ToUser:       "admin",
						ToCredential: "secret",
						ToHandler:    okHandler,
					},
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
		if len(auth.setCalls) != 1 {
			t.Fatalf("expected 1 SetCredentials call, got %d", len(auth.setCalls))
		}
		if auth.setCalls[0].user != "admin" || auth.setCalls[0].cred != "secret" {
			t.Errorf("expected admin/secret, got %s/%s",
				auth.setCalls[0].user, auth.setCalls[0].cred)
		}
	})

	t.Run("user in map without ToHandler falls to default", func(t *testing.T) {
		defaultCalled := false
		defaultH := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			defaultCalled = true
			w.WriteHeader(http.StatusOK)
		})
		h := &Handler{
			options: &uropt.Options{
				DefaultHandler: defaultH,
				Users: uropt.UserMappingOptionsByUser{
					"alice": {}, // no ToHandler
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
			t.Error("expected default handler when user has no ToHandler")
		}
	})
}
