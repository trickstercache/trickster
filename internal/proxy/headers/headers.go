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

	// ValueApplicationJSON represents the HTTP Header Value of "application/json"
	ValueApplicationJSON = "application/json"
	// ValueMaxAge represents the HTTP Header Value of "max-age"
	ValueMaxAge = "max-age"
	// ValueMultipartFormData represents the HTTP Header Value of "multipart/form-data"
	ValueMultipartFormData = "multipart/form-data"
	// ValueMustRevalidate represents the HTTP Header Value of "must-revalidate"
	ValueMustRevalidate = "must-revalidate"
	// ValueNoCache represents the HTTP Header Value of "no-cache"
	ValueNoCache = "no-cache"
	// ValueNoStore represents the HTTP Header Value of "no-store"
	ValueNoStore = "no-store"
	// ValueNoTransform represents the HTTP Header Value of "no-transform"
	ValueNoTransform = "no-transform"
	// ValuePrivate represents the HTTP Header Value of "private"
	ValuePrivate = "private"
	// ValueProxyRevalidate represents the HTTP Header Value of "proxy-revalidate"
	ValueProxyRevalidate = "proxy-revalidate"
	// ValuePublic represents the HTTP Header Value of "public"
	ValuePublic = "public"
	// ValueSharedMaxAge represents the HTTP Header Value of "s-maxage"
	ValueSharedMaxAge = "s-maxage"
	// ValueTextPlain represents the HTTP Header Value of "text/plain"
	ValueTextPlain = "text/plain"
	// ValueXFormURLEncoded represents the HTTP Header Value of "application/x-www-form-urlencoded"
	ValueXFormURLEncoded = "application/x-www-form-urlencoded"

	// ValueMultipartByteRanges represents the HTTP Header prefix for a Multipart Byte Range response
	ValueMultipartByteRanges = "multipart/byteranges; boundary="

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
	// NameContentRange represents the HTTP Header Name of "Content-Range"
	NameContentRange = "Content-Range"
	// NameTricksterResult represents the HTTP Header Name of "X-Trickster-Result"
	NameTricksterResult = "X-Trickster-Result"
	// NameVia represents the HTTP Header Name of "Via"
	NameVia = "Via"
	// NameXForwardedFor represents the HTTP Header Name of "X-Forwarded-For"
	NameXForwardedFor = "X-Forwarded-For"
	// NameAcceptEncoding represents the HTTP Header Name of "Accept-Encoding"
	NameAcceptEncoding = "Accept-Encoding"
	// NameSetCookie represents the HTTP Header Name of "Set-Cookie"
	NameSetCookie = "Set-Cookie"
	// NameRange represents the HTTP Header Name of "Range"
	NameRange = "Range"
	// NameTransferEncoding represents the HTTP Header Name of "Transfer-Encoding"
	NameTransferEncoding = "Transfer-Encoding"
	// NameIfModifiedSince represents the HTTP Header Name of "If-Modified-Since"
	NameIfModifiedSince = "If-Modified-Since"
	// NameIfUnmodifiedSince represents the HTTP Header Name of "If-Unodified-Since"
	NameIfUnmodifiedSince = "If-Unmodified-Since"
	// NameIfNoneMatch represents the HTTP Header Name of "If-None-Match"
	NameIfNoneMatch = "If-None-Match"
	// NameIfMatch represents the HTTP Header Name of "If-Match"
	NameIfMatch = "If-Match"
	// NameDate represents the HTTP Header Name of "date"
	NameDate = "Date"
	// NamePragma represents the HTTP Header Name of "pragma"
	NamePragma = "Pragma"
	// NameLastModified represents the HTTP Header Name of "last-modified"
	NameLastModified = "Last-Modified"
	// NameExpires represents the HTTP Header Name of "expires"
	NameExpires = "Expires"
	// NameETag represents the HTTP Header Name of "etag"
	NameETag = "Etag"
)

// Merge merges the source http.Header map into destination map.
// If a key exists in both maps, the source value wins.
// If the destination map is nil, the source map will not be merged
func Merge(dst, src http.Header) {
	if src == nil || len(src) == 0 || dst == nil {
		return
	}
	for k, sv := range src {
		if len(sv) == 0 {
			continue
		}
		dst[k] = []string{sv[0]}
	}
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
		headers.Set(NameXForwardedFor, remoteAddr)
		headers.Set(NameVia, runtime.ApplicationName+" "+runtime.ApplicationVersion)
	}
}

// AddResponseHeaders injects standard Trickster headers into downstream HTTP responses
func AddResponseHeaders(headers http.Header) {
	// We're read only and a harmless API, so allow all CORS
	headers.Set(NameAllowOrigin, "*")
	headers.Set(NameVia, runtime.ApplicationName+" "+runtime.ApplicationVersion)
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

// String returns the string representation of the headers as if
// they were transmitted over the wire (Header1: value1\nHeader2: value2\n\n)
func String(h http.Header) string {
	if h == nil || len(h) == 0 {
		return "\n\n"
	}
	sb := strings.Builder{}
	for k, v := range h {
		sb.WriteString(fmt.Sprintf("%s: %s\n", k, v))
	}
	// add the header section end new line
	sb.WriteString("\n")
	return sb.String()
}
