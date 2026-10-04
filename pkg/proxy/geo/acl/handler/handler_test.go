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
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
)

const (
	addrAllowed = "192.0.2.1"
	addrDenied  = "198.51.100.1"
)

type fixedLocator struct{}

func (fixedLocator) Locate(addr netip.Addr) (geo.Location, error) {
	if addr == netip.MustParseAddr(addrAllowed) {
		return geo.Location{Country: geo.Code2{'U', 'S'}}, nil
	}
	return geo.Location{Country: geo.Code2{'F', 'R'}}, nil
}
func (fixedLocator) Serves() geo.Fields { return geo.FieldCountry }
func (fixedLocator) Close() error       { return nil }

func compile(t *testing.T, action options.Action) *acl.ACL {
	t.Helper()
	a, err := acl.Compile(&options.Options{Name: t.Name(), Allow: []string{"US"}, Action: action},
		fixedLocator{}, "fixed")
	require.NoError(t, err)
	return a
}

func serve(h http.Handler, addr string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = addr + ":4000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNew(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Denied-By", r.Header.Get(headers.NameTricksterGeoDenied))
		w.WriteHeader(http.StatusNoContent)
	})
	require.NotNil(t, New(nil, next))
	require.Nil(t, New(compile(t, 0), nil))

	h := New(compile(t, 0), next)
	w := serve(h, addrAllowed)
	require.Equal(t, http.StatusNoContent, w.Code)
	w = serve(h, addrDenied)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, options.DefaultMessage+"\n", w.Body.String())

	counting := compile(t, options.ActionCount)
	h = New(counting, next)
	w = serve(h, addrDenied)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Equal(t, counting.Name(), w.Header().Get("X-Denied-By"))
	w = serve(h, addrAllowed)
	require.Empty(t, w.Header().Get("X-Denied-By"))
}

func BenchmarkAllowed(b *testing.B) {
	a, err := acl.Compile(&options.Options{Name: "bench", Allow: []string{"US"}}, fixedLocator{}, "fixed")
	require.NoError(b, err)
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	h := New(a, next)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = addrAllowed + ":4000"
	w := httptest.NewRecorder()
	b.ReportAllocs()
	for b.Loop() {
		h.ServeHTTP(w, r)
	}
}
