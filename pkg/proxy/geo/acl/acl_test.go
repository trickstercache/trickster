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

package acl

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed"
	geofeedopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const (
	testLocatorName = "acl-test-locator"
	addrUS          = "192.0.2.1"
	addrTexas       = "192.0.2.2"
	addrFrance      = "198.51.100.1"
	addrNowhere     = "203.0.113.1"
	addrFailing     = "203.0.113.2"
	addrPrivate     = "10.1.2.3"
)

var errLookup = errors.New("lookup failed")

type fakeLocator struct { // places a fixed set of addresses
	serves geo.Fields
}

func code(s string) geo.Code2 { return geo.Code2{s[0], s[1]} }

func (fakeLocator) Locate(addr netip.Addr) (geo.Location, error) {
	switch addr.String() {
	case addrUS:
		return geo.Location{Country: code("US"), Continent: code("NA")}, nil
	case addrTexas:
		return geo.Location{Country: code("US"), Continent: code("NA"), Subdivision: [3]byte{'T', 'X'}}, nil
	case addrFrance:
		return geo.Location{Country: code("FR"), Continent: code("EU")}, nil
	case addrFailing:
		return geo.Location{}, errLookup
	}
	return geo.Location{}, nil
}

func (f fakeLocator) Serves() geo.Fields { return f.serves }
func (fakeLocator) Close() error         { return nil }

type requestLocator struct{ fakeLocator } // places a request by a header

func (requestLocator) LocateRequest(r *http.Request) (geo.Location, error) {
	if c, ok := geo.ParseCode2(r.Header.Get("X-Country")); ok {
		return geo.Location{Country: c}, nil
	}
	return geo.Location{}, nil
}

func compile(t *testing.T, o *options.Options) *ACL {
	t.Helper()
	if o.Name == "" {
		o.Name = t.Name()
	}
	require.NoError(t, o.Validate())
	a, err := Compile(o, fakeLocator{serves: geo.FieldsAll}, testLocatorName)
	require.NoError(t, err)
	return a
}

func TestCheck(t *testing.T) {
	allow := func(edit func(*options.Options)) *options.Options {
		o := &options.Options{Allow: []string{"US"}}
		if edit != nil {
			edit(o)
		}
		return o
	}
	deny := func(edit func(*options.Options)) *options.Options {
		o := &options.Options{Deny: []string{"US-TX", "continent:EU"}}
		if edit != nil {
			edit(o)
		}
		return o
	}
	unknown := func(v options.Verdict) func(*options.Options) {
		return func(o *options.Options) { o.Unknown = v }
	}
	exempt := func(o *options.Options) { o.Exempt = []string{"private", addrFrance} }
	count := func(o *options.Options) { o.Action = options.ActionCount }
	for _, tc := range []struct {
		name string
		o    *options.Options
		want map[string]Result
	}{
		{"allow", allow(nil), map[string]Result{addrUS: ResultAllowed, addrTexas: ResultAllowed,
			addrFrance: ResultDenied, addrNowhere: ResultDenied, addrFailing: ResultDenied, addrPrivate: ResultDenied,
			"": ResultDenied}},
		{"allow unknown allow", allow(unknown(options.VerdictAllow)), map[string]Result{addrUS: ResultAllowed,
			addrFrance: ResultDenied, addrNowhere: ResultAllowed, addrFailing: ResultAllowed, "": ResultAllowed}},
		{"allow exempt", allow(exempt), map[string]Result{addrFrance: ResultExempt, addrPrivate: ResultExempt,
			"127.0.0.1": ResultExempt, "100.64.0.1": ResultExempt, "fd00::1": ResultExempt, "fe80::1": ResultExempt,
			"::ffff:" + addrFrance: ResultExempt, addrNowhere: ResultDenied}},
		{"allow count", allow(count), map[string]Result{addrUS: ResultAllowed, addrFrance: ResultCounted}},
		{"deny", deny(nil), map[string]Result{addrUS: ResultAllowed, addrTexas: ResultDenied, addrFrance: ResultDenied,
			addrNowhere: ResultAllowed, addrFailing: ResultAllowed}},
		{"deny unknown deny", deny(unknown(options.VerdictDeny)), map[string]Result{addrUS: ResultAllowed,
			addrNowhere: ResultDenied, "": ResultDenied}},
		{"deny count", deny(count), map[string]Result{addrTexas: ResultCounted, addrUS: ResultAllowed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := compile(t, tc.o)
			for addr, want := range tc.want {
				var a1 netip.Addr
				if addr != "" {
					a1 = netip.MustParseAddr(addr)
				}
				require.Equal(t, want.String(), a.Check(a1, PlaneNative).String(), addr)
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.RemoteAddr = net.JoinHostPort(addr, "1234")
				if addr == "" {
					r.RemoteAddr = "pipe"
				}
				require.Equal(t, want.String(), a.CheckRequest(r).String(), addr)
			}
		})
	}
}

func TestCheckCounts(t *testing.T) {
	a := compile(t, &options.Options{Name: "acl-counts", Allow: []string{"US"}, Exempt: []string{addrPrivate}})
	denied := metrics.GeoACLDecisions.WithLabelValues("acl-counts", PlaneStream.String(), ResultDenied.String())
	exempt := metrics.GeoACLDecisions.WithLabelValues("acl-counts", PlaneStream.String(), ResultExempt.String())
	failed := metrics.GeoLocatorLookups.WithLabelValues(testLocatorName, "error")
	found := metrics.GeoLocatorLookups.WithLabelValues(testLocatorName, "found")
	d0, e0, f0, ok0 := testutil.ToFloat64(denied), testutil.ToFloat64(exempt), testutil.ToFloat64(failed),
		testutil.ToFloat64(found)
	a.Check(netip.MustParseAddr(addrFailing), PlaneStream)
	a.Check(netip.MustParseAddr(addrFrance), PlaneStream)
	a.Check(netip.MustParseAddr(addrPrivate), PlaneStream)
	require.Equal(t, d0+2, testutil.ToFloat64(denied))
	require.Equal(t, e0+1, testutil.ToFloat64(exempt))
	require.Equal(t, f0+1, testutil.ToFloat64(failed))
	require.Equal(t, ok0+1, testutil.ToFloat64(found))
}

func TestCheckRequestClientIP(t *testing.T) {
	a := compile(t, &options.Options{Allow: []string{"US"}})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = addrFrance + ":1"
	require.Equal(t, ResultDenied, a.CheckRequest(r))
	// the address the listener's trusted proxies resolved is the one judged
	r = r.WithContext(tctx.WithClientIP(r.Context(), addrUS))
	require.Equal(t, ResultAllowed, a.CheckRequest(r))
}

func TestRequestLocator(t *testing.T) {
	o := &options.Options{Name: t.Name(), Deny: []string{"FR"}, Exempt: []string{addrPrivate}}
	a, err := Compile(o, requestLocator{fakeLocator{serves: geo.FieldCountry}}, testLocatorName)
	require.NoError(t, err)
	require.False(t, a.ReadsAddresses())
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = addrUS + ":1"
	r.Header.Set("X-Country", "fr")
	require.Equal(t, ResultDenied, a.CheckRequest(r))
	r.Header.Set("X-Country", "us")
	require.Equal(t, ResultAllowed, a.CheckRequest(r))
	r.RemoteAddr = addrPrivate + ":1"
	r.Header.Set("X-Country", "fr")
	require.Equal(t, ResultExempt, a.CheckRequest(r))
}

func TestCompileServes(t *testing.T) {
	o := &options.Options{Name: t.Name(), Deny: []string{"US-TX"}}
	_, err := Compile(o, fakeLocator{serves: geo.FieldCountry | geo.FieldContinent}, testLocatorName)
	require.ErrorContains(t, err, "lists entries by subdivision")
	o.Deny = []string{"continent:EU"}
	_, err = Compile(o, fakeLocator{serves: geo.FieldCountry}, testLocatorName)
	require.NoError(t, err, "a country's continent is known without the locator")
	_, err = Compile(o, fakeLocator{serves: geo.FieldSubdivision}, testLocatorName)
	require.Error(t, err)
	o.Deny = []string{"UK"}
	_, err = Compile(o, fakeLocator{serves: geo.FieldsAll}, testLocatorName)
	require.ErrorIs(t, err, geo.ErrInvalidEntry)
	o.Deny = []string{"FR"}
	o.Exempt = []string{"nope"}
	_, err = Compile(o, fakeLocator{serves: geo.FieldsAll}, testLocatorName)
	require.ErrorIs(t, err, options.ErrInvalidExempt)
}

func TestResponse(t *testing.T) {
	a := compile(t, &options.Options{Allow: []string{"US"}})
	require.Equal(t, options.DefaultMessage, a.Message())
	w := httptest.NewRecorder()
	a.WriteResponse(w)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, options.DefaultMessage+"\n", w.Body.String())
	require.Equal(t, headers.ValueNoStore, w.Header().Get(headers.NameCacheControl))
	require.Equal(t, "text/plain; charset=utf-8", w.Header().Get(headers.NameContentType))

	a = compile(t, &options.Options{Deny: []string{"FR"}, Message: "Nope.", Response: &options.ResponseOptions{
		Status: http.StatusUnavailableForLegalReasons,
		Headers: map[string]string{
			headers.NameContentType: headers.ValueApplicationJSON,
			headers.NameLink:        `<https://example.com/legal>; rel="blocked-by"`,
		},
		Body: `{"error":"blocked"}`,
	}})
	require.Equal(t, "Nope.", a.Message())
	w = httptest.NewRecorder()
	a.WriteResponse(w)
	require.Equal(t, http.StatusUnavailableForLegalReasons, w.Code)
	require.Equal(t, `{"error":"blocked"}`, w.Body.String())
	require.Equal(t, headers.ValueNoStore, w.Header().Get(headers.NameCacheControl), "headers merge over the default")
	require.Equal(t, headers.ValueApplicationJSON, w.Header().Get(headers.NameContentType))
	require.NotEmpty(t, w.Header().Get(headers.NameLink))
	// a header added after the refusal is written never reaches the next refusal
	w.Header().Add(headers.NameCacheControl, headers.ValuePrivate)
	w = httptest.NewRecorder()
	a.WriteResponse(w)
	require.Equal(t, []string{headers.ValueNoStore}, w.Header().Values(headers.NameCacheControl))
	require.Equal(t, a.Response().Status, w.Code)
}

func TestSessionGate(t *testing.T) {
	a := compile(t, &options.Options{Allow: []string{"US"}, Message: "Not from there."})
	g := a.SessionGate()
	require.Nil(t, g.Admit(netip.MustParseAddr(addrUS)))
	d := g.Admit(netip.MustParseAddr(addrFrance))
	require.NotNil(t, d)
	require.Equal(t, backends.DenialLocation, d.Reason)
	require.Equal(t, "Not from there.", d.Message)
	counted := compile(t, &options.Options{Name: "gate-count", Allow: []string{"US"}, Action: options.ActionCount})
	require.Nil(t, counted.SessionGate().Admit(netip.MustParseAddr(addrFrance)))
	require.True(t, a.ReadsAddresses())
	require.Equal(t, t.Name(), a.Name())
}

func BenchmarkCheck(b *testing.B) {
	feed, err := geofeed.New(testLocatorName, &geofeedopts.Options{Entries: []string{
		addrUS + ",US", addrFrance + ",FR", "2001:db8::/32,CA",
	}})
	require.NoError(b, err)
	o := &options.Options{Name: "bench", Allow: []string{"US", "CA", "MX"}, Exempt: []string{"private"}}
	a, err := Compile(o, feed, testLocatorName)
	require.NoError(b, err)
	addrs := []netip.Addr{netip.MustParseAddr(addrUS), netip.MustParseAddr(addrFrance),
		netip.MustParseAddr(addrNowhere)}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		a.Check(addrs[i%len(addrs)], PlaneNative)
	}
}
