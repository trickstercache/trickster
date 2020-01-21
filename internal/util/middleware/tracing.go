/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package middleware

import (
	"net/http"

	"github.com/Comcast/trickster/internal/util/tracing"
	"github.com/gorilla/mux"
)

func Trace(originName, originType string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

			r, span := tracing.PrepareRequest(r, r.Host) // TODO Host is not the best tracer name. Something Request level would be better, but paths are already in the trace

			defer span.End()

			next.ServeHTTP(w, r)
		})
	}
}
