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

package bodyfilter

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// Handler returns a handler for the BodyFilter, which supports
func Handler(maxSize int64, truncateOnly bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch && r.Method != http.MethodPost &&
			r.Method != http.MethodPut {
			next.ServeHTTP(w, r)
			return
		}
		b, err := request.GetBody(r, maxSize)

		if err != nil && (!truncateOnly || err != failures.ErrPayloadTooLarge) {
			failures.HandleBadRequestResponse(w, nil)
			return
		}
		if !truncateOnly && int64(len(b)) > maxSize {
			failures.HandlePayloadTooLarge(w, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
