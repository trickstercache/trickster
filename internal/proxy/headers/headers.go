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

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/timeseries"
)

const (
	// Common HTTP Header Values
	ValueNoCache         = "no-cache"
	ValueApplicationJSON = "application/json"

	// Common HTTP Header Names
	NameCacheControl      = "cache-control"
	NameAllowOrigin       = "access-control-allow-origin"
	NameContentType       = "content-type"
	NameContentEncoding   = "content-encoding"
	NameContentLength     = "content-length"
	NameAuthorization     = "authorization"
	NameAcceptEncoding    = "accept-encoding"
	NamePragma            = "pragma"
	NameExpires           = "expires"
	NameDate              = "date"
	NameLastModified      = "last-modified"
	NameIfModifiedSince   = "if-modified-since"
	NameIfUnmodifiedSince = "if-unmodified-since"
	NameIfMatch           = "if-match"
	NameIfNoneMatch       = "if-none-match"
	NameETag              = "etag"

	NameXAccelerator    = "x-accelerator"
	NameTricksterResult = "x-trickster-result"
	NameXForwardedBy    = "x-forwarded-by"
	NameXForwardedFor   = "x-forwarded-for"
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

func ExtractHeader(headers http.Header, header string) (string, bool) {
	if Value, ok := headers[header]; ok {
		return strings.Join(Value, "; "), true
	}
	return "", false
}

func RemoveClientHeaders(headers http.Header) {
	headers.Del(NameAcceptEncoding)
}
