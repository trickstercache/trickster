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
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	at "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// Concurrent SetDefaultHandler / SetAuthenticator vs ServeHTTP. Run under
// -race to catch any regression in the locking discipline. Without the mutex,
// this races on h.options.DefaultHandler / h.authenticator fields.
func TestURConcurrentReloadIsRaceFree(t *testing.T) {
	okH := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := &Handler{
		options: &uropt.Options{
			DefaultHandler:    okH,
			NoRouteStatusCode: http.StatusUnauthorized,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	wg.Go(func() {
		for ctx.Err() == nil {
			h.SetDefaultHandler(okH)
			h.SetAuthenticator(&mockAuth{}, false)
		}
	})

	const callers = 8
	for range callers {
		wg.Go(func() {
			for ctx.Err() == nil {
				w := httptest.NewRecorder()
				r, _ := http.NewRequest("GET", "http://example.com/", nil)
				h.ServeHTTP(w, r)
			}
		})
	}

	time.Sleep(300 * time.Millisecond)
	cancel()
	wg.Wait()
}

// When the AuthResult path is in use (cred is unextracted, "") and the router
// entry leaves ToCredential empty, SetCredentials must NOT fire: writing
// (ToUser, "") would emit Basic auth with an empty password and collapse
// every SSO-mapped user to the same cache key.
func TestURRetainsInboundCredWhenToCredentialEmpty(t *testing.T) {
	auth := &mockAuth{}
	okH := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := &Handler{
		authenticator:      auth,
		enableReplaceCreds: true,
		options: &uropt.Options{
			DefaultHandler: okH,
			Users: uropt.UserMappingOptionsByUser{
				"alice": {
					ToUser:    "bob",
					ToHandler: okH,
				},
			},
		},
	}

	r, _ := http.NewRequest("GET", "http://example.com/", nil)
	rsc := &request.Resources{
		AuthResult: &at.AuthResult{Username: "alice", Status: at.AuthSuccess},
	}
	r = request.SetResources(r, rsc)
	h.ServeHTTP(httptest.NewRecorder(), r)

	if len(auth.setCalls) != 0 {
		t.Fatalf("expected zero SetCredentials calls (cred and ToCredential both empty), got %d", len(auth.setCalls))
	}
	if n := auth.sanitizeCalls.Load(); n != 0 {
		t.Errorf("expected Sanitize not to fire when there is no replacement cred to write, got %d", n)
	}
}
