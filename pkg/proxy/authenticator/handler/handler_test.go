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

package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

type testAuthenticator struct {
	result        *types.AuthResult
	err           error
	authCalls     int
	sanitizeCalls int
}

func (a *testAuthenticator) Authenticate(*http.Request) (*types.AuthResult, error) {
	a.authCalls++
	return a.result, a.err
}
func (*testAuthenticator) ExtractCredentials(*http.Request) (string, string, error) {
	return "", "", nil
}
func (*testAuthenticator) SetExtractCredentialsFunc(types.ExtractCredsFunc) {}
func (*testAuthenticator) SetCredentials(*http.Request, string, string) error {
	return nil
}
func (*testAuthenticator) SetSetCredentialsFunc(types.SetCredentialsFunc)            {}
func (*testAuthenticator) SetObserveOnly(bool)                                       {}
func (*testAuthenticator) IsObserveOnly() bool                                       { return false }
func (*testAuthenticator) LoadUsers(string, types.CredentialsFileFormat, bool) error { return nil }
func (*testAuthenticator) AddUser(string, string) error                              { return nil }
func (*testAuthenticator) RemoveUser(string)                                         {}
func (a *testAuthenticator) Clone() types.Authenticator                              { return a }
func (*testAuthenticator) ProxyPreserve() bool                                       { return false }
func (a *testAuthenticator) Sanitize(*http.Request)                                  { a.sanitizeCalls++ }

func TestMiddleware(t *testing.T) {
	tests := []struct {
		name             string
		authenticator    *testAuthenticator
		cached           *types.AuthResult
		wantStatus       int
		wantNext         bool
		wantAuthCalls    int
		wantSanitize     int
		wantAuthResult   bool
		wantResponseHead string
	}{
		{name: "nil authenticator", wantStatus: http.StatusInternalServerError},
		{name: "cached failure", authenticator: &testAuthenticator{},
			cached: &types.AuthResult{Status: types.AuthFailed}, wantStatus: http.StatusUnauthorized},
		{name: "cached success", authenticator: &testAuthenticator{},
			cached: &types.AuthResult{Status: types.AuthSuccess}, wantStatus: http.StatusNoContent,
			wantNext: true},
		{name: "authentication error", authenticator: &testAuthenticator{err: errors.New("denied")},
			wantStatus: http.StatusUnauthorized, wantAuthCalls: 1},
		{name: "nil result", authenticator: &testAuthenticator{},
			wantStatus: http.StatusUnauthorized, wantAuthCalls: 1},
		{name: "failed result copies challenge", authenticator: &testAuthenticator{result: &types.AuthResult{
			Status: types.AuthFailed, ResponseHeaders: map[string]string{"WWW-Authenticate": "Basic"},
		}}, wantStatus: http.StatusUnauthorized, wantAuthCalls: 1, wantResponseHead: "Basic"},
		{name: "observed result", authenticator: &testAuthenticator{result: &types.AuthResult{
			Status: types.AuthObserved,
		}}, wantStatus: http.StatusNoContent, wantNext: true, wantAuthCalls: 1, wantAuthResult: true},
		{name: "successful result", authenticator: &testAuthenticator{result: &types.AuthResult{
			Status: types.AuthSuccess,
		}}, wantStatus: http.StatusNoContent, wantNext: true, wantAuthCalls: 1,
			wantSanitize: 1, wantAuthResult: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusNoContent)
			})
			rsc := &request.Resources{AuthResult: tc.cached}
			req := request.SetResources(httptest.NewRequest(http.MethodGet, "/", nil), rsc)
			recorder := httptest.NewRecorder()
			var authenticator types.Authenticator
			if tc.authenticator != nil {
				authenticator = tc.authenticator
			}
			Middleware(authenticator, next).ServeHTTP(recorder, req)

			if recorder.Code != tc.wantStatus || nextCalled != tc.wantNext {
				t.Fatalf("response = %d, next = %t; want %d/%t",
					recorder.Code, nextCalled, tc.wantStatus, tc.wantNext)
			}
			if tc.authenticator != nil && (tc.authenticator.authCalls != tc.wantAuthCalls ||
				tc.authenticator.sanitizeCalls != tc.wantSanitize) {
				t.Errorf("calls = authenticate:%d sanitize:%d; want %d/%d",
					tc.authenticator.authCalls, tc.authenticator.sanitizeCalls,
					tc.wantAuthCalls, tc.wantSanitize)
			}
			if (rsc.AuthResult != nil) != tc.wantAuthResult && tc.cached == nil {
				t.Errorf("cached auth result presence = %t, want %t", rsc.AuthResult != nil, tc.wantAuthResult)
			}
			if got := recorder.Header().Get("WWW-Authenticate"); got != tc.wantResponseHead {
				t.Errorf("WWW-Authenticate = %q, want %q", got, tc.wantResponseHead)
			}
		})
	}
}

func TestNamedMiddlewareScopesCachedResult(t *testing.T) {
	a := &testAuthenticator{result: &types.AuthResult{Status: types.AuthSuccess}}
	rsc := &request.Resources{AuthResult: &types.AuthResult{
		AuthenticatorName: "outer", Status: types.AuthSuccess,
	}}
	req := request.SetResources(httptest.NewRequest(http.MethodGet, "/", nil), rsc)
	recorder := httptest.NewRecorder()
	NamedMiddleware("inner", a, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, req)
	if a.authCalls != 1 {
		t.Fatalf("inner authenticator calls = %d, want 1", a.authCalls)
	}
	if rsc.AuthResult.AuthenticatorName != "inner" {
		t.Errorf("cached authenticator = %q, want inner", rsc.AuthResult.AuthenticatorName)
	}
}
