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

package unauthorized

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// ServeHTTP responds to an HTTP Request with a 401 Unauthorized
func ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte("Unauthorized"))
}
