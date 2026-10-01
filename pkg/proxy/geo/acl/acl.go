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

// Package acl compiles geo ACLs and judges clients by them: an address or a request in, a verdict out.
package acl

import (
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// Plane is the kind of listener a client is judged on
type Plane uint8

const (
	// PlaneHTTP judges HTTP requests in the route chain
	PlaneHTTP Plane = iota
	// PlaneNative judges native protocol sessions
	PlaneNative
	// PlaneStream judges tcp, tls and udp flows
	PlaneStream
	planeCount
)

var planeNames = [planeCount]string{"http", "native", "stream"}

// String returns the plane's name
func (p Plane) String() string {
	return planeNames[p]
}

// Result is a geo ACL's judgment of a client
type Result uint8

const (
	// ResultAllowed allows the client
	ResultAllowed Result = iota
	// ResultDenied refuses the client
	ResultDenied
	// ResultCounted would have refused the client, but the geo ACL only counts
	ResultCounted
	// ResultExempt allows the client with no lookup, since its address is exempt
	ResultExempt
	resultCount
)

var resultNames = [resultCount]string{"allowed", "denied", "counted", "exempt"}

// String returns the result's name
func (r Result) String() string {
	return resultNames[r]
}

const (
	lookupFound = iota
	lookupNotFound
	lookupError
	lookupCount
)

var lookupNames = [lookupCount]string{"found", "not_found", "error"}

var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10") // carrier-grade NAT, which netip does not count as private

const textPlainUTF8 = headers.ValueTextPlain + "; charset=utf-8"

// Response is a geo ACL's HTTP refusal, built once
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// ACL is a compiled geo ACL; it is safe for concurrent use
type ACL struct {
	name           string
	list           *geo.List
	allowList      bool
	unknownDenies  bool
	count          bool
	exemptPrivate  bool
	exempt         clientip.Trusted
	locator        locator.Locator
	requestLocator locator.RequestLocator
	response       Response
	denial         backends.Denial
	decisions      [planeCount][resultCount]counter
	lookups        [lookupCount]counter
}

type counter interface{ Inc() }

// Compile builds an ACL from validated options and the locator they name, and fails when the locator cannot
// fill a location field the ACL's entries are matched against
func Compile(o *options.Options, loc locator.Locator, locatorName string) (*ACL, error) {
	list, err := geo.ParseList(o.Entries())
	if err != nil {
		return nil, fmt.Errorf("geo ACL %q: %w", o.Name, err)
	}
	served := loc.Serves()
	if served.Has(geo.FieldCountry) {
		// a country's continent is known without the locator
		served |= geo.FieldContinent
	}
	if needs := list.Needs(); !served.Has(needs) {
		return nil, fmt.Errorf("geo ACL %q lists entries by %s, but geo locator %q serves only %s",
			o.Name, needs&^served, locatorName, loc.Serves())
	}
	private, exempt, err := options.ParseExempt(o.Exempt)
	if err != nil {
		return nil, fmt.Errorf("geo ACL %q: %w", o.Name, err)
	}
	a := &ACL{
		name:          o.Name,
		list:          list,
		allowList:     o.IsAllowList(),
		count:         o.Action == options.ActionCount,
		exemptPrivate: private,
		exempt:        exempt,
		locator:       loc,
		response:      buildResponse(o),
		denial:        backends.Denial{Reason: backends.DenialLocation, Message: o.EffectiveMessage()},
	}
	switch o.Unknown {
	case options.VerdictAllow:
		a.unknownDenies = false
	case options.VerdictDeny:
		a.unknownDenies = true
	default:
		// as an unlisted location: denied by an allow list, allowed by a deny list
		a.unknownDenies = a.allowList
	}
	if rl, ok := loc.(locator.RequestLocator); ok {
		a.requestLocator = rl
	}
	for p := range planeCount {
		for r := range resultCount {
			a.decisions[p][r] = metrics.GeoACLDecisions.WithLabelValues(o.Name, planeNames[p], resultNames[r])
		}
	}
	for i := range lookupCount {
		a.lookups[i] = metrics.GeoLocatorLookups.WithLabelValues(locatorName, lookupNames[i])
	}
	return a, nil
}

func buildResponse(o *options.Options) Response {
	h := http.Header{}
	h.Set(headers.NameCacheControl, headers.ValueNoStore)
	h.Set(headers.NameContentType, textPlainUTF8)
	body := []byte(o.EffectiveMessage() + "\n")
	if o.Response != nil {
		for name, value := range o.Response.Headers {
			h.Set(name, value)
		}
		if o.Response.Body != "" {
			body = []byte(o.Response.Body)
		}
	}
	// full slices, so a later Add to a written header copies rather than writes into the shared array
	for name, v := range h {
		h[name] = slices.Clip(v)
	}
	return Response{Status: o.EffectiveStatus(), Header: h, Body: body}
}

// Name returns the geo ACL's name
func (a *ACL) Name() string {
	return a.name
}

// Message returns what a refused client is told
func (a *ACL) Message() string {
	return a.denial.Message
}

// Response returns the HTTP refusal
func (a *ACL) Response() Response {
	return a.response
}

// ReadsAddresses reports whether the ACL can judge a bare address, which every plane but HTTP requires
func (a *ACL) ReadsAddresses() bool {
	return a.requestLocator == nil
}

// Check judges a client address on plane. An address that is not valid has no location.
func (a *ACL) Check(addr netip.Addr, plane Plane) Result {
	addr = addr.Unmap()
	if a.isExempt(addr) {
		return a.record(plane, ResultExempt)
	}
	if !addr.IsValid() {
		return a.decide(plane, addr, geo.Location{})
	}
	loc, err := a.locator.Locate(addr)
	return a.decide(plane, addr, a.counted(loc, err))
}

// CheckRequest judges an HTTP request by its resolved client address, or by the request itself when the
// locator reads requests
func (a *ACL) CheckRequest(r *http.Request) Result {
	addr, _ := netip.ParseAddr(request.ClientIP(r))
	addr = addr.Unmap()
	if a.isExempt(addr) {
		return a.record(PlaneHTTP, ResultExempt)
	}
	if a.requestLocator != nil {
		loc, err := a.requestLocator.LocateRequest(r)
		return a.decide(PlaneHTTP, addr, a.counted(loc, err))
	}
	if !addr.IsValid() {
		return a.decide(PlaneHTTP, addr, geo.Location{})
	}
	loc, err := a.locator.Locate(addr)
	return a.decide(PlaneHTTP, addr, a.counted(loc, err))
}

// WriteResponse writes the HTTP refusal
func (a *ACL) WriteResponse(w http.ResponseWriter) {
	h := w.Header()
	maps.Copy(h, a.response.Header)
	w.WriteHeader(a.response.Status)
	_, _ = w.Write(a.response.Body)
}

// SessionGate returns the ACL as a native protocol session gate
func (a *ACL) SessionGate() backends.SessionGate {
	return sessionGate{a}
}

type sessionGate struct {
	acl *ACL
}

func (g sessionGate) Admit(client netip.Addr) *backends.Denial {
	if g.acl.Check(client, PlaneNative) == ResultDenied {
		return &g.acl.denial
	}
	return nil
}

func (a *ACL) isExempt(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	if a.exemptPrivate && (addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() ||
		sharedAddressSpace.Contains(addr)) {
		return true
	}
	return len(a.exempt) > 0 && a.exempt.Contains(addr)
}

func (a *ACL) counted(loc geo.Location, err error) geo.Location {
	// a failed lookup is judged as no location
	switch {
	case err != nil:
		a.lookups[lookupError].Inc()
		return geo.Location{}
	case loc.IsZero():
		a.lookups[lookupNotFound].Inc()
	default:
		a.lookups[lookupFound].Inc()
	}
	return loc
}

func (a *ACL) decide(plane Plane, addr netip.Addr, loc geo.Location) Result {
	var denied bool
	if loc.IsZero() {
		denied = a.unknownDenies
	} else {
		denied = a.list.Match(loc) != a.allowList
	}
	result := ResultAllowed
	if denied {
		result = ResultDenied
		if a.count {
			result = ResultCounted
		}
		if logger.DebugEnabled() {
			logger.Debug("geo ACL denied a client", logging.Pairs{
				keys.GeoACL: a.name, keys.Plane: planeNames[plane], keys.ClientIP: addr.String(),
				keys.Location: loc.String(), keys.Result: resultNames[result],
			})
		}
	}
	return a.record(plane, result)
}

func (a *ACL) record(plane Plane, r Result) Result {
	a.decisions[plane][r].Inc()
	return r
}
