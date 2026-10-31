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
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// redactedValue replaces a sensitive header value in config views and logs.
const redactedValue = "*****"

var sensitiveCredentials = sets.New([]string{
	NameAuthorization, NameProxyAuthorization, NameCookie, NameSetCookie,
	NameXAPIKey, NameAPIKey, NameXGoogAPIKey, NameXAmzSecurityToken,
})

// sensitiveNameParts mark a configured header or parameter name, such as X-Consul-Token or
// api_key, whose value config views redact although it is not a well-known credential header
var sensitiveNameParts = []string{
	"auth", "token", "secret", "password", "passwd", "key", "credential", "cookie", "session",
	"signature",
}

// IsSensitive reports whether the named header carries a credential.
func IsSensitive(name string) bool {
	_, ok := sensitiveCredentials[http.CanonicalHeaderKey(name)]
	return ok
}

// IsSensitiveName reports whether a configured header or parameter name likely carries a
// credential: a well-known credential header, or a name containing a part like "token".
func IsSensitiveName(name string) bool {
	if IsSensitive(name) {
		return true
	}
	name = strings.ToLower(name)
	for _, part := range sensitiveNameParts {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

// HideAuthorizationCredentials replaces, with 5 asterisks, each value in a configured header
// or parameter map whose name IsSensitiveName reports as carrying a credential.
func HideAuthorizationCredentials[m ~map[K]V, K ~string, V ~string](headers m) {
	// keys match canonically behind any '+'/'-' update operator; an empty
	// value carries no credential (it can declare an opt-out) and is preserved
	for k, v := range headers {
		_, name := ParseUpdateKey(string(k))
		if IsSensitiveName(name) && v != "" {
			headers[k] = redactedValue
		}
	}
}

// SanitizeForLogging returns a sanitized version of the provided Header map,
// that does not include senstiive info like credentials or cookies
func SanitizeForLogging(h http.Header) Lookup {
	if h == nil {
		return nil
	}
	out := make(Lookup)
	for _, hn := range allowList {
		if v := h.Get(hn); v != "" {
			out[hn] = v
		}
	}
	return out
}

var allowList = []string{
	NameAccept, NameAcceptEncoding, NameAcceptLanguage,
	NameCacheControl, NameConnection, NameContentLength, NameContentType,
	NameDate, NameHost, NameIfModifiedSince, NameIfNoneMatch, NameRange,
	NameUserAgent, NameVia, NameXForwardedFor, NameTricksterResult,
}
