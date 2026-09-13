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

// Package parts reads and writes the parts of an HTTP request through one set
// of accessors, so components that name request parts in their configuration
// share a single definition of each part. Getters allocate nothing beyond the
// strings they must build and tolerate a nil request or URL.
package parts

import (
	"net/http"
	"net/url"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

// Method is the request method
func Method(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.Method
}

// Scheme is the request URL's scheme
func Scheme(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.Scheme
}

// Host is the request URL's host, with any port
func Host(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.Host
}

// Hostname is the request URL's host without its port
func Hostname(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.Hostname()
}

// Port is the request URL's port, or empty when it names none
func Port(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.Port()
}

// Path is the request URL's path
func Path(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.Path
}

// RawQuery is the request URL's query string, without the leading '?'
func RawQuery(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.RawQuery
}

// URL is the full request URL
func URL(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.String()
}

// URLNoParams is the request URL without its query string
func URLNoParams(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	u := *r.URL
	u.RawQuery = ""
	u.ForceQuery = false
	return u.String()
}

// Query parses the request's query string; callers evaluating several
// parameters should parse once and pass the values to Param and HasParam
func Query(r *http.Request) url.Values {
	if r == nil || r.URL == nil {
		return nil
	}
	return r.URL.Query()
}

// Header is the first value of the named header; name must be canonical
func Header(r *http.Request, name string) string {
	if r == nil {
		return ""
	}
	return matching.HeaderValue(r.Header, name)
}

// HasHeader reports whether the named header is present; name must be canonical
func HasHeader(r *http.Request, name string) bool {
	return r != nil && matching.HasHeader(r.Header, name)
}

// Param is the first value of the named parameter in parsed query values
func Param(values url.Values, name string) string {
	return matching.QueryValue(values, name)
}

// HasParam reports whether the named parameter is present in parsed query values
func HasParam(values url.Values, name string) bool {
	return matching.HasQuery(values, name)
}

// SetMethod replaces the request method
func SetMethod(r *http.Request, method string) {
	if r != nil {
		r.Method = method
	}
}

// SetScheme replaces the request URL's scheme
func SetScheme(r *http.Request, scheme string) {
	if r != nil && r.URL != nil {
		r.URL.Scheme = scheme
	}
}

// SetRawQuery replaces the request URL's query string
func SetRawQuery(r *http.Request, rawQuery string) {
	if r != nil && r.URL != nil {
		r.URL.RawQuery = rawQuery
	}
}

// SetHost replaces the request URL's host and port together
func SetHost(r *http.Request, host string) {
	if r != nil && r.URL != nil {
		r.URL.Host = host
	}
}

// SetHostname replaces the request URL's hostname, keeping its port
func SetHostname(r *http.Request, hostname string) {
	if r != nil && r.URL != nil {
		r.URL.Host = urls.ReplaceHostname(r.URL.Host, hostname)
	}
}

// SetPort replaces the request URL's port, keeping its hostname
func SetPort(r *http.Request, port string) {
	if r != nil && r.URL != nil {
		r.URL.Host = urls.ReplacePort(r.URL.Host, port)
	}
}

// SetPath replaces the request URL's path
func SetPath(r *http.Request, path string) {
	if r != nil && r.URL != nil {
		r.URL.Path = path
	}
}
