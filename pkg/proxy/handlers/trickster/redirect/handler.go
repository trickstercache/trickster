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

package redirect

import (
	"net/http"
	"net/url"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

// DefaultRedirectCode is the status a redirect path answers with when its
// response_code is not a redirection
const DefaultRedirectCode = http.StatusFound

// HandleRedirect answers with a redirection to the request's own URL as the
// path's request rewriter and request_headers left it: a rewriter that sets
// the scheme, hostname, port or path decides those parts of the Location, a
// request Host update decides the hostname the rewriter did not set, and
// whatever neither set is taken from the incoming request. The path's
// response_code selects the redirection status, and its response_headers
// are applied.
func HandleRedirect(w http.ResponseWriter, r *http.Request) {
	if w == nil || r == nil {
		return
	}
	code := DefaultRedirectCode
	if p := request.GetResources(r); p != nil && p.PathConfig != nil {
		pc := p.PathConfig
		if pc.ResponseCode >= 300 && pc.ResponseCode < 400 {
			code = pc.ResponseCode
		}
		// the forwarding engines apply a path's request headers as they
		// build the upstream request; a redirect never reaches one, so they
		// are applied here, ahead of the Location they may change
		if len(pc.RequestHeaders) > 0 {
			headers.UpdateRequestHeaders(r, pc.RequestHeaders)
		}
		if len(pc.ResponseHeaders) > 0 {
			headers.UpdateHeaders(w.Header(), pc.ResponseHeaders)
		}
	}
	w.Header().Set(headers.NameLocation, Location(r).String())
	w.WriteHeader(code)
}

// Location composes the URL a redirect answers with. A component the request
// URL carries was set by a rewriter and is kept; the rest comes from the
// request. An explicit scheme with no explicit port drops the request's port,
// since the redirect target is that scheme's well-known port, and a port that
// is the well-known one for the scheme is omitted.
func Location(r *http.Request) *url.URL {
	out := &url.URL{Path: "/"}
	if r == nil {
		return out
	}
	var scheme, hostname, port string
	if r.URL != nil {
		out.Path, out.RawPath, out.RawQuery = r.URL.Path, r.URL.RawPath, r.URL.RawQuery
		scheme, hostname, port = r.URL.Scheme, r.URL.Hostname(), r.URL.Port()
	}
	explicitScheme := scheme != ""
	if !explicitScheme {
		scheme = urls.RequestScheme(r)
	}
	reqHost, reqPort := urls.SplitHostPort(r.Host)
	if hostname == "" {
		hostname = reqHost
	}
	if port == "" && !explicitScheme {
		port = reqPort
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	out.Scheme = scheme
	out.Host = urls.JoinHostPort(hostname, port)
	if out.Path == "" {
		out.Path = "/"
	}
	return out
}
