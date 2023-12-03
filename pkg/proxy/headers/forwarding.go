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

package headers

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/runtime"
)

const (
	// NameVia represents the HTTP Header Name of "Via"
	NameVia = "Via"
	// NameForwarded reqresents the HTTP Header Name of "Forwarded"
	NameForwarded = "Forwarded"
	// NameXForwardedFor represents the HTTP Header Name of "X-Forwarded-For"
	NameXForwardedFor = "X-Forwarded-For"
	// NameXForwardedServer represents the HTTP Header Name of "X-Forwarded-Server"
	NameXForwardedServer = "X-Forwarded-Server"
	// NameXForwardedHost represents the HTTP Header Name of "X-Forwarded-Host"
	NameXForwardedHost = "X-Forwarded-Host"
	// NameXForwardedProto represents the HTTP Header Name of "X-Forwarded-Proto"
	NameXForwardedProto = "X-Forwarded-Proto"
)

// Hop describes a collection of data about the forwarded request
// to be used in Via, Forwarded and X-Forwarded-* headers
type Hop struct {
	// RemoteAddr is the client address for which the request is being forwarded.
	RemoteAddr string
	// Host header as received by the proxy
	Host string
	// Scheme is the protocol scheme requested of the proxy
	Scheme string
	// Server is an identifier for the server running the Trickster process
	Server string
	// protocol indicates the HTTP protocol Version in proper format (.eg., "HTTP/1.1")
	// requested by the client
	Protocol string
	// Hops is a list of previous X-Forwarded-For or Forwarded headers to which we can append our hop
	Hops Hops
	// Via is the previous Via header, we will append ours to it.
	Via string
}

// Hops defines a list of Hop References
type Hops []*Hop

// HopHeaders defines a list of headers that Proxies should not pass through
var HopHeaders = []string{
	NameAcceptEncoding,
	NameConnection,
	NameProxyConnection,
	NameKeepAlive,
	NameProxyAuthenticate,
	NameProxyAuthorization,
	NameTe,
	NameTrailer,
	NameTransferEncoding,
	NameUpgrade,
	NameAcceptEncoding,
}

// ForwardingHeaders defines a list of headers that Proxies use to identify themselves in a request
var ForwardingHeaders = []string{
	NameXForwardedFor,
	NameXForwardedHost,
	NameXForwardedProto,
	NameXForwardedServer,
	NameForwarded,
	NameVia,
}

// MergeRemoveHeaders defines a list of headers that should be removed when Merging time series results
var MergeRemoveHeaders = []string{
	NameLastModified,
	NameDate,
	NameContentLength,
	NameContentType,
	NameTransferEncoding,
}

var forwardingFuncs = map[string]func(*http.Request, *Hop){
	"standard": AddForwarded,
	"x":        AddXForwarded,
	"none":     nil,
	"both":     AddForwardedAndX,
}

// IsValidForwardingType returns true if the input is a valid Forwarding Type name
// Valid names comprise the keys of the forwardingFuncs map
func IsValidForwardingType(input string) bool {
	_, ok := forwardingFuncs[input]
	return ok
}

// AddForwardingHeaders sets or appends to the forwarding headers to the provided request
func AddForwardingHeaders(r *http.Request, headerType string) {
	if r == nil {
		return
	}
	hop := HopsFromRequest(r)
	// Now we can safely remove any pre-existing Forwarding headers before we set them fresh
	StripClientHeaders(r.Header)
	StripForwardingHeaders(r.Header)
	SetVia(r, hop)
	if f, ok := forwardingFuncs[headerType]; ok && f != nil {
		f(r, hop)
	}
}

// String returns a "Forwarded" Header value
func (hop *Hop) String(expand ...bool) string {
	parts := make([]string, 0, 4)
	if hop.Server != "" {
		parts = append(parts, fmt.Sprintf("by=%s", formatForwardedAddress(hop.Server)))
	}
	if hop.RemoteAddr != "" {
		parts = append(parts, fmt.Sprintf("for=%s", formatForwardedAddress(hop.RemoteAddr)))
	}
	if hop.Host != "" {
		parts = append(parts, fmt.Sprintf("host=%s", formatForwardedAddress(hop.Host)))
	}
	if hop.Scheme != "" {
		parts = append(parts, fmt.Sprintf("proto=%s", formatForwardedAddress(hop.Scheme)))
	}
	currentHop := strings.Join(parts, ";")
	if (len(expand) == 1 && !expand[0]) || len(hop.Hops) == 0 {
		return currentHop
	}
	l := len(hop.Hops)
	parts = make([]string, l+1)
	for i := 0; i < l; i++ {
		parts[i] = hop.Hops[i].String(false)
	}
	parts[l] = currentHop
	return strings.Join(parts, ", ")
}

// XHeader returns an http.Header containing the "X-Forwarded-*" headers
func (hop *Hop) XHeader() http.Header {
	h := make(http.Header)
	if hop.Server != "" {
		h.Set(NameXForwardedServer, hop.Server)
	}
	if hop.RemoteAddr != "" {
		l := len(hop.Hops)
		if l > 0 {
			Hops := make([]string, l+1)
			for i, hop := range hop.Hops {
				Hops[i] = hop.RemoteAddr
			}
			Hops[l] = hop.RemoteAddr
			h.Set(NameXForwardedFor, strings.Join(Hops, ", "))
		} else {
			h.Set(NameXForwardedFor, hop.RemoteAddr)
		}
	}
	if hop.Host != "" {
		h.Set(NameXForwardedHost, hop.Host)
	}
	if hop.Scheme != "" {
		h.Set(NameXForwardedProto, hop.Scheme)
	}
	return h
}

// AddForwarded sets or appends to the standard Forwarded header to the provided request
func AddForwarded(r *http.Request, hop *Hop) {
	r.Header.Set(NameForwarded, hop.String())
}

// AddXForwarded sets or appends to the "X-Forwarded-*" headers to the provided request
func AddXForwarded(r *http.Request, hop *Hop) {
	h := hop.XHeader()
	Merge(r.Header, h)
}

// AddForwardedAndX sets or appends to the to the "X-Forwarded-*" headers
// headers, and to the standard Forwarded header to the provided request
func AddForwardedAndX(r *http.Request, hop *Hop) {
	h := hop.XHeader()
	h.Set(NameForwarded, hop.String())
	Merge(r.Header, h)
}

// SetVia sets the "Via" header to the provided request
func SetVia(r *http.Request, hop *Hop) {
	if r == nil || r.Header == nil || hop == nil {
		return
	}
	if hop.Via != "" {
		r.Header.Set(NameVia, hop.Via+", "+hop.Protocol+" "+runtime.Server)
	} else {
		r.Header.Set(NameVia, hop.Protocol+" "+runtime.Server)
	}
}

// HopsFromRequest extracts a Hop reference that includes a list of any previous hops
func HopsFromRequest(r *http.Request) *Hop {
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	hop := &Hop{
		RemoteAddr: clientIP,
		Scheme:     r.URL.Scheme,
		Protocol:   r.Proto,
	}
	if r.Header == nil {
		return hop
	}
	hop.Via = r.Header.Get(NameVia)
	hop.Hops = HopsFromHeader(r.Header)
	return hop
}

// HopsFromHeader extracts a hop from the header
func HopsFromHeader(h http.Header) Hops {
	if _, ok := h[NameForwarded]; ok {
		return parseForwardHeaders(h)
	} else if _, ok := h[NameXForwardedFor]; ok {
		return parseXForwardHeaders(h)
	}
	return nil
}

func parseForwardHeaders(h http.Header) Hops {
	var hops Hops
	fh := h.Get(NameForwarded)
	if fh != "" {
		fwds := strings.Split(strings.Replace(fh, " ", "", -1), ",")
		hops = make(Hops, 0, len(fwds))
		for _, f := range fwds {
			hop := &Hop{}
			var ok bool
			parts := strings.Split(f, ";")
			for _, p := range parts {
				subparts := strings.Split(p, "=")
				if len(subparts) == 2 {
					switch subparts[0] {
					case "for":
						hop.RemoteAddr = subparts[1]
						ok = true
					case "by":
						hop.Server = subparts[1]
					case "proto":
						hop.Protocol = subparts[1]
					case "host":
						hop.Host = subparts[1]
					}
				}
			}
			if ok {
				hop.normalizeAddresses()
				hops = append(hops, hop)
			}
		}
	}
	return hops
}

func (hop *Hop) normalizeAddresses() {
	hop.Host = normalizeAddress(hop.Host)
	hop.RemoteAddr = normalizeAddress(hop.RemoteAddr)
	hop.Server = normalizeAddress(hop.Server)
}

const v6LB = `["`
const v6RB = `"]`

func normalizeAddress(input string) string {
	input = strings.TrimPrefix(input, v6LB)
	input = strings.TrimSuffix(input, v6RB)
	return input
}

func formatForwardedAddress(input string) string {
	if isV6Address(input) {
		input = v6LB + input + v6RB
	}
	return input
}

func parseXForwardHeaders(h http.Header) Hops {
	xff := h.Get(NameXForwardedFor)
	if xff != "" {
		fwds := strings.Split(strings.Replace(xff, " ", "", -1), ",")
		hops := make(Hops, len(fwds))
		for i, f := range fwds {
			hop := &Hop{RemoteAddr: f}
			hop.Host = h.Get(NameXForwardedHost)
			hop.Protocol = h.Get(NameXForwardedProto)
			hop.Server = h.Get(NameXForwardedServer)
			hop.normalizeAddresses()
			hops[i] = hop
		}
		return hops
	}
	return nil
}

// AddResponseHeaders injects standard Trickster headers into downstream HTTP responses
func AddResponseHeaders(h http.Header) {
	// We're read only and a harmless API, so allow all CORS
	h.Set(NameAllowOrigin, "*")
}

// StripClientHeaders strips certain headers from the HTTP request to facililate acceleration
func StripClientHeaders(h http.Header) {
	for _, k := range HopHeaders {
		h.Del(k)
	}
}

// StripForwardingHeaders strips certain headers from the HTTP request to facililate acceleration
func StripForwardingHeaders(h http.Header) {
	for _, k := range ForwardingHeaders {
		h.Del(k)
	}
}

func isV6Address(input string) bool {
	ip := net.ParseIP(input)
	return ip != nil && strings.Contains(input, ":")
}

// StripMergeHeaders strips certain headers from the HTTP request to facililate acceleration when
// merging HTTP responses from multiple origins
func StripMergeHeaders(h http.Header) {
	for _, k := range MergeRemoveHeaders {
		h.Del(k)
	}
}
