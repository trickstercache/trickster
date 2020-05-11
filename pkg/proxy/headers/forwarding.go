/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"sync"

	"github.com/tricksterproxy/trickster/pkg/runtime"
)

const (
	// NameVia represents the HTTP Header Name of "Via"
	NameVia = "Via"
	// NameForwarded reqresents teh HTTP Header Name o "Forwarded"
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

var hopHeaders = []string{
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

var viaHeader string
var once sync.Once
var onceVia = func() {
	viaHeader = strings.Trim(runtime.ApplicationName+" "+runtime.ApplicationVersion, " ")
}

var forwardingFuncs = map[string]func(*http.Request, *ForwardedData){
	"standard": AddForwarded,
	"x":        AddXForwarded,
	"none":     SetVia,
	"both":     AddForwardedAndX,
}

// IsValidForwardingType returns true if the input is a valid Forwarding Type name
// Valid names comprise the keys of the forwardingFuncs map
func IsValidForwardingType(input string) bool {
	_, ok := forwardingFuncs[input]
	return ok
}

// AddForwardingHeaders sets or appends to the forwarding headers to the provided request
func AddForwardingHeaders(inbound, outbound *http.Request, headerType string) {
	SetVia(outbound, nil)
	if headerType == "none" {
		return
	}
	fd := fdFromRequest(inbound)
	if f, ok := forwardingFuncs[headerType]; ok {
		f(outbound, fd)
	}
}

// ForwardedData describes a collection of data about the forwarded request
// to be used in Via, Forwarded and X-Forwarded-* headers
type ForwardedData struct {
	// RemoteAddr is the client address for which the request is being forwarded.
	RemoteAddr string
	// Host header as received by the proxy
	Host string
	// Scheme is the protocol scheme requested of the proxy
	Scheme string
	// Server is an identier for the server running the Trickster process
	Server string
	// Protocol indicates the HTTP Protocol Version in proper format (.eg., "HTTP/1.1")
	// requested by the client
	Protocol string
	// Fors is a list of previous X-Forwarded-For or Forwarded headers to which we can append our hop
	Hops []*ForwardedData
}

// String returns a "Forwarded" Header value
func (fd *ForwardedData) String(expand ...bool) string {
	parts := make([]string, 0, 4)
	if fd.Server != "" {
		parts = append(parts, fmt.Sprintf("by=%s", fd.Server))
	}
	if fd.RemoteAddr != "" {
		parts = append(parts, fmt.Sprintf("for=%s", fd.RemoteAddr))
	}
	if fd.Host != "" {
		parts = append(parts, fmt.Sprintf("host=%s", fd.Host))
	}
	if fd.Scheme != "" {
		parts = append(parts, fmt.Sprintf("proto=%s", fd.Scheme))
	}
	currentFor := strings.Join(parts, ";")
	if (len(expand) == 1 && !expand[0]) || len(fd.Hops) == 0 {
		return currentFor
	}
	l := len(fd.Hops)
	parts = make([]string, l+1)
	for i := 0; i < l; i++ {
		parts[i] = fd.Hops[i].String(false)
	}
	parts[l] = currentFor
	return strings.Join(parts, ", ")
}

// XHeader returns an http.Header containing the "X-Forwarded-*" headers
func (fd *ForwardedData) XHeader() http.Header {
	h := make(http.Header)
	if fd.Server != "" {
		h.Set(NameXForwardedServer, fd.Server)
	}
	if fd.RemoteAddr != "" {
		l := len(fd.Hops)
		if l > 0 {
			hops := make([]string, l+1)
			for i, hop := range fd.Hops {
				hops[i] = hop.RemoteAddr
			}
			hops[l] = fd.RemoteAddr
			h.Set(NameXForwardedFor, strings.Join(hops, ", "))
		} else {
			h.Set(NameXForwardedFor, fd.RemoteAddr)
		}
	}
	if fd.Host != "" {
		h.Set(NameXForwardedHost, fd.Host)
	}
	if fd.Scheme != "" {
		h.Set(NameXForwardedProto, fd.Scheme)
	}
	return h
}

// AddForwarded sets or appends to the standard Forwarded header to the provided request
func AddForwarded(r *http.Request, fd *ForwardedData) {
	r.Header.Set(NameForwarded, fd.String())
}

// AddXForwarded sets or appends to the "X-Forwarded-*" headers to the provided request
func AddXForwarded(r *http.Request, fd *ForwardedData) {
	h := fd.XHeader()
	Merge(r.Header, h)
}

// AddForwardedAndX sets or appends to the to the "X-Forwarded-*" headers
// headers, and to the standard Forwarded header to the provided request
func AddForwardedAndX(r *http.Request, fd *ForwardedData) {
	h := fd.XHeader()
	h.Set(NameForwarded, fd.String())
	Merge(r.Header, h)
}

// SetVia sets the "Via" header to the provided request
func SetVia(r *http.Request, fd *ForwardedData) {
	if r != nil {
		setVia(r.Header)
	}
}

// setVia sets the "Via" header to the provided request
func setVia(h http.Header) {
	if h == nil {
		h = make(http.Header)
	}
	once.Do(onceVia)
	h.Set(NameVia, viaHeader)
}

func fdFromRequest(r *http.Request) *ForwardedData {

	// TODO: Get any previously-passed headers forwarded headers

	if r.Header == nil {
		r.Header = make(http.Header)
	}

	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	fd := &ForwardedData{
		RemoteAddr: clientIP,
		Scheme:     r.URL.Scheme,
		Protocol:   r.Proto,
	}
	return fd
}

// AddResponseHeaders injects standard Trickster headers into downstream HTTP responses
func AddResponseHeaders(h http.Header) {
	// We're read only and a harmless API, so allow all CORS
	h.Set(NameAllowOrigin, "*")
	setVia(h)
}

// RemoveClientHeaders strips certain headers from the HTTP request to facililate acceleration
func RemoveClientHeaders(headers http.Header) {
	for _, k := range hopHeaders {
		headers.Del(k)
	}
}
