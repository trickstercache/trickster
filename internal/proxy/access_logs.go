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

package proxy

import (
	"net/http"

	"github.com/Comcast/trickster/internal/util/log"
)

func logUpstreamRequest(originName, originType, handlerName, method, path, userAgent string, responseCode, size, executionTime int) {
	log.Debug("upstream request",
		log.Pairs{
			"originName":  originName,
			"originType":  originType,
			"handlerName": handlerName,
			"method":      method,
			"uri":         path,
			"userAgent":   userAgent,
			"code":        responseCode,
			"size":        size,
			"duration":    executionTime,
		})
}

func logDownstreamRequest(r *http.Request) {
	log.Debug("downtream request",
		log.Pairs{
			"uri":       r.RequestURI,
			"method":    r.Method,
			"userAgent": r.UserAgent(),
			"clientIP":  r.RemoteAddr,
		})
}
