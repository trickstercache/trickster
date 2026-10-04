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
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	geoacl "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed"
	geofeedopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

const (
	geoClientUS     = "192.0.2.1"
	geoClientFrance = "198.51.100.1"
)

func compiledGeoACL(t *testing.T, name string, o *geoaclopts.Options) *geoaclopts.Options {
	t.Helper()
	feed, err := geofeed.New(t.Name(), &geofeedopts.Options{Entries: []string{
		geoClientUS + ",US", geoClientFrance + ",FR",
	}})
	if err != nil {
		t.Fatal(err)
	}
	o.Name = name
	a, err := geoacl.Compile(o, feed, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	o.Compiled = a
	return o
}

type countingHandler struct{ calls atomic.Int32 }

func (h *countingHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.calls.Add(1)
	w.WriteHeader(http.StatusNoContent)
}

func serveFrom(h http.Handler, client string) int {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = net.JoinHostPort(client, "4000")
	r = request.SetResources(r, &request.Resources{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestPathGeoACLReferences(t *testing.T) {
	usOnly := compiledGeoACL(t, "us-only", &geoaclopts.Options{Allow: []string{"US"}})
	noUS := compiledGeoACL(t, "no-us", &geoaclopts.Options{Deny: []string{"US"}})
	backend := &bo.Options{GeoACLName: usOnly.Name, GeoACLOptions: usOnly}
	for _, test := range []struct {
		name           string
		path           *po.Options
		us, france     int
		backendApplies bool
	}{
		{"inherits", po.New(), http.StatusNoContent, http.StatusForbidden, true},
		{"replaces", &po.Options{GeoACLName: noUS.Name, GeoACLOptions: noUS}, http.StatusForbidden,
			http.StatusNoContent, false},
		{"clears", &po.Options{GeoACLName: reserved.ReferenceNone}, http.StatusNoContent, http.StatusNoContent, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := attachGeoACL(&countingHandler{}, test.path, backend)
			if got := serveFrom(h, geoClientUS); got != test.us {
				t.Errorf("US client: status %d; want %d", got, test.us)
			}
			if got := serveFrom(h, geoClientFrance); got != test.france {
				t.Errorf("French client: status %d; want %d", got, test.france)
			}
		})
	}

	// a route with no geo ACL is the handler it was given
	next := &countingHandler{}
	if h, ok := attachGeoACL(next, po.New(), &bo.Options{}).(*countingHandler); !ok || h != next {
		t.Error("a route with no geo ACL was wrapped")
	}
	uncompiled := &bo.Options{GeoACLName: "x", GeoACLOptions: &geoaclopts.Options{Name: "x"}}
	if geoACLFor(po.New(), uncompiled) != nil {
		t.Error("geo ACL options with nothing compiled into them gated a route")
	}
}

func TestGeoACLRunsAheadOfAuthAndCache(t *testing.T) {
	usOnly := compiledGeoACL(t, "us-only", &geoaclopts.Options{Allow: []string{"US"}})
	const authName = "denies-everyone"
	backend := &bo.Options{
		Name: "geo", Provider: "reverseproxycache", GeoACLName: usOnly.Name, GeoACLOptions: usOnly,
		AuthenticatorName: authName, AuthOptions: basicAuthOptions(t, authName, false),
	}
	origin := &countingHandler{}
	path := po.New()
	path.Handler = origin
	h := applyMiddleware(backend, path, nil, nil, nil, routeLogging{}, nil, nil)
	// a refused client is answered before the authenticator, the cache or the origin is reached
	if got := serveFrom(h, geoClientFrance); got != http.StatusForbidden {
		t.Errorf("refused client: status %d; want %d", got, http.StatusForbidden)
	}
	// an allowed client reaches the authenticator, which turns it away
	if got := serveFrom(h, geoClientUS); got != http.StatusUnauthorized {
		t.Errorf("allowed client: status %d; want %d", got, http.StatusUnauthorized)
	}
	if origin.calls.Load() != 0 {
		t.Error("the origin was reached")
	}
}
