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

package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
)

// QueryAsPost serves the QUERY method (RFC 10008) on a path whose origin only accepts POST:
// it advertises the media types in Accept-Query, rejects others, and forwards QUERY as POST
func QueryAsPost(mediaTypes []string, next http.Handler) http.Handler {
	if len(mediaTypes) == 0 {
		return next
	}
	// shared by every response; its capacity of one makes a header Add copy rather than write into it
	acceptQuery := []string{FormatAcceptQuery(mediaTypes)}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// an origin that advertises its own Accept-Query replaces this one when its headers merge in
		w.Header()[headers.NameAcceptQuery] = acceptQuery
		if r.Method != methods.MethodQuery {
			next.ServeHTTP(w, r)
			return
		}
		ct := r.Header.Get(headers.NameContentType)
		if ct == "" {
			failures.HandleBadRequestResponse(w, r)
			return
		}
		if !acceptsMediaType(mediaTypes, ct) {
			failures.HandleMiscFailure(http.StatusUnsupportedMediaType, w)
			return
		}
		// a shallow copy, so handlers outside this one still see the client's method
		pr := r.WithContext(r.Context())
		pr.Method = http.MethodPost
		next.ServeHTTP(w, pr)
	})
}

// FormatAcceptQuery renders media types as an Accept-Query field value, an RFC 9651 list of strings
func FormatAcceptQuery(mediaTypes []string) string {
	var sb strings.Builder
	for i, mt := range mediaTypes {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(strconv.Quote(mt))
	}
	return sb.String()
}

// type and subtype are case-insensitive; parameters do not affect acceptance
func acceptsMediaType(accepted []string, ct string) bool {
	mt, _, _ := strings.Cut(ct, ";")
	mt = strings.TrimSpace(mt)
	for _, a := range accepted {
		if strings.EqualFold(a, mt) {
			return true
		}
	}
	return false
}
