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

// Package urls provides capabilities for manipulating URLs that are not
// provided by the builtin net/url package
package urls

import (
	"net/http"
	"net/url"
)

// Clone returns a deep copy of a *url.URL
func Clone(u *url.URL) *url.URL {
	u2 := FromParts(u.Scheme, u.Host, u.Path, u.RawQuery, u.Fragment)
	if u.User != nil {
		var user *url.Userinfo
		if p, ok := u.User.Password(); ok {
			user = url.UserPassword(u.User.Username(), p)
		} else {
			user = url.User(u.User.Username())
		}
		u2.User = user
	}
	return u2
}

// FromParts returns a *url.URL constructed from the provided parts
func FromParts(scheme, host, path, query, fragment string) *url.URL {
	return &url.URL{
		Scheme:   scheme,
		Host:     host,
		Path:     path,
		RawQuery: query,
		Fragment: fragment,
	}
}

// BuildUpstreamURL will merge the downstream request with the provided BaseURL
// to construct the full upstream URL
func BuildUpstreamURL(r *http.Request, u *url.URL) *url.URL {
	u2 := Clone(u)
	u2.Path += r.URL.Path
	u2.RawQuery = r.URL.RawQuery
	u2.Fragment = r.URL.Fragment
	u2.User = r.URL.User
	return u2
}

// Size returns the memory utilization in bytes of the URL
func Size(u *url.URL) int {
	if u == nil {
		return 0
	}
	return len(u.Fragment) + len(u.Host) + len(u.Opaque) + len(u.Path) +
		len(u.RawPath) + len(u.RawQuery) + len(u.Scheme) + 1
}
