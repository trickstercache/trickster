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

package header

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/header/options"

	"github.com/stretchr/testify/require"
)

const (
	countryHeader     = "CF-IPCountry"
	subdivisionHeader = "CloudFront-Viewer-Country-Region"
	continentHeader   = "X-Continent"
	trustedPeer       = "10.0.0.1"
	untrustedPeer     = "198.51.100.1"
)

var (
	_ locator.Locator        = (*Locator)(nil)
	_ locator.RequestLocator = (*Locator)(nil)
)

func newLocator(subdivision, continent string) *Locator {
	o := &options.Options{Country: countryHeader, Subdivision: subdivision, Continent: continent}
	o.Initialize()
	return New(o)
}

func locate(t *testing.T, l *Locator, peer string, headers map[string]string) string {
	t.Helper()
	// the request comes from peer through a listener that trusts trustedPeer, as the daemon serves it
	trusted, err := clientip.ParseTrusted([]string{trustedPeer})
	require.NoError(t, err)
	var got string
	h := clientip.Middleware(trusted, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		loc, err := l.LocateRequest(r)
		require.NoError(t, err)
		got = loc.String()
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = peer + ":1234"
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), r)
	return got
}

func TestLocateRequest(t *testing.T) {
	l := newLocator(subdivisionHeader, "")
	require.Equal(t, geo.FieldsAll, l.Serves())
	for name, tc := range map[string]struct {
		peer    string
		headers map[string]string
		want    string
	}{
		"trusted":                      {trustedPeer, map[string]string{countryHeader: "us"}, "US"},
		"untrusted":                    {untrustedPeer, map[string]string{countryHeader: "US"}, ""},
		"no header":                    {trustedPeer, nil, ""},
		"unknown value":                {trustedPeer, map[string]string{countryHeader: "XX"}, ""},
		"tor":                          {trustedPeer, map[string]string{countryHeader: "t1"}, ""},
		"empty":                        {trustedPeer, map[string]string{countryHeader: " "}, ""},
		"malformed":                    {trustedPeer, map[string]string{countryHeader: "USA"}, ""},
		"subdivision":                  {trustedPeer, map[string]string{countryHeader: "US", subdivisionHeader: "TX"}, "US-TX"},
		"subdivision with country":     {trustedPeer, map[string]string{countryHeader: "US", subdivisionHeader: "US-tx"}, "US-TX"},
		"subdivision of another":       {trustedPeer, map[string]string{countryHeader: "US", subdivisionHeader: "CA-QC"}, "US"},
		"malformed subdivision":        {trustedPeer, map[string]string{countryHeader: "US", subdivisionHeader: "TEXAS"}, "US"},
		"subdivision from untrusted":   {untrustedPeer, map[string]string{countryHeader: "US", subdivisionHeader: "TX"}, ""},
		"spoofed forwarding untrusted": {untrustedPeer, map[string]string{countryHeader: "US", "X-Forwarded-For": trustedPeer}, ""},
	} {
		require.Equal(t, tc.want, locate(t, l, tc.peer, tc.headers), name)
	}

	// a request no trusted listener resolved has no location, so a listener with no trusted_proxies believes none
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(countryHeader, "US")
	loc, err := l.LocateRequest(r)
	require.NoError(t, err)
	require.True(t, loc.IsZero())
	r = r.WithContext(tctx.WithClientIP(r.Context(), trustedPeer))
	loc, err = l.LocateRequest(r)
	require.NoError(t, err)
	require.True(t, loc.IsZero(), "an address recorded without a trusted peer believes no header")
}

func TestContinent(t *testing.T) {
	l := newLocator("", continentHeader)
	require.Equal(t, geo.FieldCountry|geo.FieldContinent, l.Serves())
	trusted, err := clientip.ParseTrusted([]string{trustedPeer})
	require.NoError(t, err)
	var got geo.Location
	h := clientip.Middleware(trusted, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = l.LocateRequest(r)
	}))
	for header, want := range map[string]string{"OC": "OC", "": "NA", "ZZ": "NA", "XY": "NA"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = trustedPeer + ":1"
		r.Header.Set(countryHeader, "US")
		if header != "" {
			r.Header.Set(continentHeader, header)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		require.Equal(t, want, got.Continent.String(), header)
	}
	loc, err := l.Locate(netip.MustParseAddr(trustedPeer))
	require.NoError(t, err)
	require.True(t, loc.IsZero())
	require.NoError(t, l.Close())
}
