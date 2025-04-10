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

	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

var sensitiveCredentials = sets.New([]string{NameAuthorization})

// HideAuthorizationCredentials replaces any sensitive HTTP header values with 5
// asterisks sensitive headers are defined in the sensitiveCredentials map
func HideAuthorizationCredentials[m ~map[K]V, K ~string, V ~string](headers m) {
	// strip Authorization Headers
	for k := range headers {
		if _, ok := sensitiveCredentials[string(k)]; ok {
			headers[k] = "*****"
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

var allowList = []string{NameAccept, NameAcceptEncoding, NameAcceptLanguage,
	NameCacheControl, NameConnection, NameContentLength, NameContentType,
	NameDate, NameHost, NameIfModifiedSince, NameIfNoneMatch, NameRange,
	NameUserAgent, NameVia, NameXForwardedFor, NameTricksterResult}
