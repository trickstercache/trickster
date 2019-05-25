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

package headers

import (
	"net/http"
	"strings"

	"github.com/Comcast/trickster/internal/config"
)

const (
	// Common HTTP Header Values
	ValueNoCache         = "no-cache"
	ValueApplicationJSON = "application/json"

	// Common HTTP Header Names
	NameCacheControl    = "Cache-Control"
	NameAllowOrigin     = "Access-Control-Allow-Origin"
	NameContentType     = "Content-Type"
	NameContentEncoding = "Content-Encoding"
	NameContentLength   = "Content-Length"
	NameAuthorization   = "Authorization"
	NameXAccelerator    = "X-Accelerator"
	NameXForwardedBy    = "X-Forwarded-By"
	NameXForwardedFor   = "X-Forwarded-For"
	NameAcceptEncoding  = "Accept-Encoding"
)

// CopyHeaders returns an exact copy of an http.Header collection
func CopyHeaders(h http.Header) http.Header {
	headers := make(http.Header)
	for k, v := range h {
		headers[k] = make([]string, len(v))
		copy(headers[k], v)
	}
	return headers
}

func AddProxyHeaders(remoteAddr string, headers http.Header) {
	if remoteAddr != "" {
		headers.Add(NameXForwardedFor, remoteAddr)
		headers.Add(NameXForwardedBy, config.ApplicationName+" "+config.ApplicationVersion)
	}
}

func AddResponseHeaders(headers http.Header) {
	// We're read only and a harmless API, so allow all CORS
	headers.Set(NameAllowOrigin, "*")
	headers.Set(NameXAccelerator, config.ApplicationName+" "+config.ApplicationVersion)
}

func ExtractHeader(headers http.Header, header string) (string, bool) {
	if Value, ok := headers[header]; ok {
		return strings.Join(Value, "; "), true
	}
	return "", false
}

func RemoveClientHeaders(headers http.Header) {
	headers.Del(NameAcceptEncoding)
}
