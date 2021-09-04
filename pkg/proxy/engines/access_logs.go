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

package engines

import (
	"net/http"

	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

func logUpstreamRequest(logger interface{}, backendName, backendProvider, handlerName, method,
	path, userAgent string, responseCode, size int, requestDuration float64) {
	tl.Debug(logger, "upstream request",
		tl.Pairs{
			"backendName":     backendName,
			"backendProvider": backendProvider,
			"handlerName":     handlerName,
			"method":          method,
			"uri":             path,
			"userAgent":       userAgent,
			"code":            responseCode,
			"size":            size,
			"durationMS":      int(requestDuration * 1000),
		})
}

func logDownstreamRequest(logger interface{}, r *http.Request) {
	tl.Debug(logger, "downtream request",
		tl.Pairs{
			"uri":       r.RequestURI,
			"method":    r.Method,
			"userAgent": r.UserAgent(),
			"clientIP":  r.RemoteAddr,
		})
}
