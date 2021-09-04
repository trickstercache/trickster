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

package handler

import (
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	strutil "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

// HandleCompression wraps an HTTP response in a compression writer
// Compress types is a map[string]interface{} like: {"text/plain": nil, "application/json": nil}
// Any matching ContentType handled by the compression handler will be compressed
func HandleCompression(next http.Handler, compressTypes strutil.Lookup) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		// if the client requested a No-Transform, then serve as-is
		if strings.Contains(r.Header.Get(headers.NameCacheControl), headers.ValueNoTransform) {
			next.ServeHTTP(w, r)
			return
		}

		// setup the request's encoder profile to compress the provided content types
		ep := &profile.Profile{
			CompressTypes: compressTypes,
			Level:         -1,
		}

		// this checks the Client's accept-encoding header to identify any compatible encodings
		enc := r.Header.Get(headers.NameAcceptEncoding)
		ep.SupportedHeaderVal, ep.Supported = providers.GetCompatibleWebProviders(enc)

		r = r.WithContext(profile.ToContext(r.Context(), ep))

		// Set up the compression encoder here:
		ew := NewEncoder(w, ep)
		next.ServeHTTP(ew, r)
		ew.Close()

	})
}
