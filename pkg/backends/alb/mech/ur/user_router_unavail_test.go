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
	"sync/atomic"
	"testing"

	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	at "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// UR routes by username and never consults target health. A user mapped to
// a backend that has gone unavailable stays mapped to it with no failover.
// When a user mapping carries a *healthcheck.Status whose value is below
// the configured floor, the request must fall through to the default
// handler instead of dispatching to the unhealthy backend.
func TestServeHTTPSkipsUnhealthyUserTarget(t *testing.T) {
	var userHits, defaultHits atomic.Int64
	userHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		userHits.Add(1)
	})
	defaultHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defaultHits.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	failing := &healthcheck.Status{}
	failing.Set(healthcheck.StatusFailing)

	h := &Handler{
		options: &uropt.Options{
			DefaultHandler: defaultHandler,
			Users: uropt.UserMappingOptionsByUser{
				"alice": {
					ToHandler: userHandler,
					ToStatus:  failing,
				},
			},
		},
	}

	r, _ := http.NewRequest("GET", "http://example.com/", nil)
	r = request.SetResources(r, &request.Resources{
		AuthResult: &at.AuthResult{Username: "alice", Status: at.AuthSuccess},
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if userHits.Load() != 0 {
		t.Errorf("unhealthy user-mapped handler invoked %d times; expected 0",
			userHits.Load())
	}
	if defaultHits.Load() != 1 {
		t.Errorf("expected fall-through to default handler; got %d defaults",
			defaultHits.Load())
	}
}
