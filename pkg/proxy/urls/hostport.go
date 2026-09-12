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

package urls

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// SplitHostPort separates a host into hostname and port, tolerating a bare
// hostname and a bracketed IPv6 literal
func SplitHostPort(host string) (hostname, port string) {
	u := url.URL{Host: host}
	return u.Hostname(), u.Port()
}

// JoinHostPort joins a hostname and an optional port with exactly one colon,
// bracketing an IPv6 literal whether or not it arrived bracketed
func JoinHostPort(hostname, port string) string {
	hostname = strings.TrimPrefix(strings.TrimSuffix(hostname, "]"), "[")
	if port != "" {
		return net.JoinHostPort(hostname, port)
	}
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]"
	}
	return hostname
}

// ReplaceHostname returns host with its hostname replaced and its port kept
func ReplaceHostname(host, hostname string) string {
	_, port := SplitHostPort(host)
	return JoinHostPort(hostname, port)
}

// ReplacePort returns host with its port replaced and its hostname kept
func ReplacePort(host, port string) string {
	hostname, _ := SplitHostPort(host)
	return JoinHostPort(hostname, port)
}

// RequestScheme is the scheme a request arrived on
func RequestScheme(r *http.Request) string {
	if r != nil && r.TLS != nil {
		return "https"
	}
	return "http"
}
