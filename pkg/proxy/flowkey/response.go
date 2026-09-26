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

// HTTPResponse returns an extractor that reads the key source from a response's headers, as the
// value an upstream hands a client to send back: a header of the same name, or the cookie that a
// Set-Cookie sets. The key is the one HTTP reads from the request that sends the value back. A
// source that OnHTTPResponse rejects is never present.
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

// setCookieValue returns the key of the value a Set-Cookie line gives the named cookie, trimmed
// of whitespace as a browser trims it and of quotes as HTTP trims them; a line that expires the
// cookie at once deletes it, and so sets nothing
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
