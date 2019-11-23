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
	"fmt"
	"net/http"
	"strings"

	"github.com/Comcast/trickster/internal/runtime"
	"github.com/Comcast/trickster/internal/timeseries"
)

const (
	// Common HTTP Header Values

	// ValueNoCache represents the HTTP Header Value of "no-cache"
	ValueNoCache = "no-cache"
	// ValueApplicationJSON represents the HTTP Header Value of "application/json"
	ValueApplicationJSON = "application/json"
	// ValueTextPlain represents the HTTP Header Value of "text/plain"
	ValueTextPlain = "text/plain"

	ValuePublic            = "public"
	ValuePrivate           = "private"
	ValueMaxAge            = "max-age"
	ValueSharedMaxAge      = "s-maxage"
	ValueMustRevalidate    = "must-revalidate"
	ValueNoStore           = "no-store"
	ValueProxyRevalidate   = "proxy-revalidate"
	ValueNoTransform       = "no-transform"
	ValueXFormUrlEncoded   = "application/x-www-form-urlencoded"
	ValueMultipartFormData = "multipart/form-data"

	// Common HTTP Header Names

	// NameCacheControl represents the HTTP Header Name of "Cache-Control"
	NameCacheControl = "Cache-Control"
	// NameAllowOrigin represents the HTTP Header Name of "Access-Control-Allow-Origin"
	NameAllowOrigin = "Access-Control-Allow-Origin"
	// NameContentType represents the HTTP Header Name of "Content-Type"
	NameContentType = "Content-Type"
	// NameContentEncoding represents the HTTP Header Name of "Content-Encoding"
	NameContentEncoding = "Content-Encoding"
	// NameContentLength represents the HTTP Header Name of "Content-Length"
	NameContentLength = "Content-Length"
	// NameAuthorization represents the HTTP Header Name of "Authorization"
	NameAuthorization = "Authorization"
	// NameXAccelerator represents the HTTP Header Name of "X-Accelerator"
	NameXAccelerator = "X-Accelerator"
	// NameTricksterResult represents the HTTP Header Name of "X-Trickster-Result"
	NameTricksterResult = "X-Trickster-Result"
	// NameXForwardedBy represents the HTTP Header Name of "X-Forwarded-By"
	NameXForwardedBy = "X-Forwarded-By"
	// NameXForwardedFor represents the HTTP Header Name of "X-Forwarded-For"
	NameXForwardedFor = "X-Forwarded-For"
	// NameAcceptEncoding represents the HTTP Header Name of "Accept-Encoding"
	NameAcceptEncoding = "Accept-Encoding"
	// NameSetCookie represents the HTTP Header Name of "Set-Cookie"
	NameSetCookie = "Set-Cookie"

	NameExpires           = "expires"
	NameLastModified      = "last-modified"
	NameDate              = "date"
	NameETag              = "etag"
	NamePragma            = "pragma"
	NameIfModifiedSince   = "if-modified-since"
	NameIfUnmodifiedSince = "if-unmodified-since"
	NameIfNoneMatch       = "if-none-match"
	NameIfMatch           = "if-match"
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

// UpdateHeaders updates the provided headers collection with the provided updates
func UpdateHeaders(headers http.Header, updates map[string]string) {
	if headers == nil || updates == nil || len(updates) == 0 {
		return
	}
	for k, v := range updates {
		if len(k) == 0 {
			continue
		}
		if k[0:1] == "-" {
			k = k[1:]
			headers.Del(k)
			continue
		}
		if k[0:1] == "+" {
			k = k[1:]
			headers.Add(k, v)
			continue
		}
		headers.Set(k, v)
	}
}

// AddProxyHeaders injects standard Trickster headers into proxied upstream HTTP requests
func AddProxyHeaders(remoteAddr string, headers http.Header) {
	if remoteAddr != "" {
		headers.Add(NameXForwardedFor, remoteAddr)
		headers.Add(NameXForwardedBy, runtime.ApplicationName+" "+runtime.ApplicationVersion)
	}
}

// AddResponseHeaders injects standard Trickster headers into downstream HTTP responses
func AddResponseHeaders(headers http.Header) {
	// We're read only and a harmless API, so allow all CORS
	headers.Set(NameAllowOrigin, "*")
	headers.Set(NameXAccelerator, runtime.ApplicationName+" "+runtime.ApplicationVersion)
}

// SetResultsHeader adds a response header summarizing Trickster's handling of the HTTP request
func SetResultsHeader(headers http.Header, engine, status, ffstatus string, fetched timeseries.ExtentList) {

	if headers == nil || engine == "" {
		return
	}

	parts := append(make([]string, 0, 4), fmt.Sprintf("engine=%s", engine))

	if status != "" {
		parts = append(parts, fmt.Sprintf("status=%s", status))
	}

	if fetched != nil && len(fetched) > 0 {
		fp := make([]string, 0, len(fetched))
		for _, v := range fetched {
			fp = append(fp, fmt.Sprintf("%d:%d", v.Start.Unix(), v.End.Unix()))
		}
		parts = append(parts, fmt.Sprintf("fetched=[%s]", strings.Join(fp, ",")))
	}

	if ffstatus != "" {
		parts = append(parts, fmt.Sprintf("ffstatus=%s", ffstatus))
	}

	headers.Set(NameTricksterResult, strings.Join(parts, "; "))

}

// ExtractHeader returns the value for the provided header name, and a boolean indicating if the header was present
func ExtractHeader(headers http.Header, header string) (string, bool) {
	if Value, ok := headers[header]; ok {
		return strings.Join(Value, "; "), true
	}
	return "", false
}

// RemoveClientHeaders strips certain headers from the HTTP request to facililate acceleration
func RemoveClientHeaders(headers http.Header) {
	headers.Del(NameAcceptEncoding)
}

