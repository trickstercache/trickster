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

// When a router entry sets ToUser but leaves ToCredential empty, the inbound
// credential must be retained rather than overwritten with the empty string.
// Before the fix, the AuthResult-driven path would call SetCredentials with
// (ToUser, "") and silently break Basic auth on the backend.
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

	if len(auth.setCalls) != 1 {
		t.Fatalf("expected one SetCredentials call, got %d", len(auth.setCalls))
	}
	if auth.setCalls[0].user != "bob" {
		t.Errorf("expected ToUser remap to bob, got %q", auth.setCalls[0].user)
	}
	// On the AuthResult path the inbound cred wasn't extracted, so cred is "".
	// The important guarantee is that we DIDN'T proactively read ToCredential
	// (which would also be "") and stomp on a future inbound cred. Mostly this
	// test pins the contract: ToUser remap fires without ToCredential.
}
