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

package flowkey

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// attrMaxAge is the Set-Cookie attribute whose value, when not positive, deletes the cookie
const attrMaxAge = "Max-Age"

// HTTPResponse extracts the value an upstream hands a client in the named header or Set-Cookie,
// keyed as HTTP keys it coming back; sources OnHTTPResponse rejects never have one.
func HTTPResponse(ks KeySource) func(http.Header) Value {
	name := ks.Name
	switch ks.Kind {
	case KeyHeader:
		return func(h http.Header) Value {
			if v := h[name]; len(v) > 0 {
				return valueOf(v[0])
			}
			return Value{}
		}
	case KeyCookie:
		return func(h http.Header) Value {
			var out Value
			// a later line setting the cookie replaces an earlier one, as it does in a browser
			for _, line := range h[headers.NameSetCookie] {
				if v, ok := setCookieValue(line, name); ok {
					out = v
				}
			}
			return out
		}
	}
	return func(http.Header) Value { return Value{} }
}

// setCookieValue returns the key of a Set-Cookie line's value for the named cookie, trimmed as a
// browser and HTTP trim it; a line that expires the cookie sets nothing
func setCookieValue(line, name string) (Value, bool) {
	pair, attrs, _ := strings.Cut(line, ";")
	n, v, ok := strings.Cut(pair, "=")
	if !ok || strings.Trim(n, " \t") != name {
		return Value{}, false
	}
	if deletesCookie(attrs) {
		return Value{}, true
	}
	return valueOf(strings.Trim(strings.Trim(v, " \t"), `"`)), true
}

// deletesCookie reports whether the last Max-Age among the attributes of a Set-Cookie line is not
// positive; one that is not a number is ignored, as a browser ignores it
func deletesCookie(attrs string) bool {
	var deletes bool
	for attrs != "" {
		var attr string
		attr, attrs, _ = strings.Cut(attrs, ";")
		n, v, _ := strings.Cut(attr, "=")
		if !strings.EqualFold(strings.Trim(n, " \t"), attrMaxAge) {
			continue
		}
		if secs, err := strconv.Atoi(strings.Trim(v, " \t")); err == nil {
			deletes = secs <= 0
		}
	}
	return deletes
}
