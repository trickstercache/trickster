/*
 * Copyright 2026 The Trickster Authors
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

package options

import (
	"fmt"
	"net/http"
	"net/textproto"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"golang.org/x/net/http/httpguts"
)

const textPlainUTF8 = "text/plain; charset=utf-8"

// compileResponse builds the reject reply. Custom headers overlay the defaults, then
// Cache-Control is forced back to no-store so a limit response is not stored downstream.
func (o *Options) compileResponse() error {
	status := defaultStatus
	body := defaultBody
	h := make(http.Header)
	h.Set(headers.NameCacheControl, headers.ValueNoStore)
	h.Set(headers.NameContentType, textPlainUTF8)
	if o.Response != nil {
		if o.Response.Status != 0 {
			status = o.Response.Status
		}
		if o.Response.Body != "" {
			body = o.Response.Body
		}
		for name, value := range o.Response.Headers {
			if err := checkResponseHeader(name, value); err != nil {
				return err
			}
			h.Set(name, value)
		}
	}
	if status < 400 || status > 599 {
		return fmt.Errorf("response status %d is outside 400..599", status)
	}
	h.Set(headers.NameCacheControl, headers.ValueNoStore)
	for name, values := range h {
		h[name] = slices.Clip(values)
	}
	o.Status = status
	o.Header = h
	o.Body = []byte(body)
	return nil
}

func checkResponseHeader(name, value string) error {
	if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("response header %q contains a carriage return or line feed", name)
	}
	if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
		return fmt.Errorf("response header %q: %q is not a valid header", name, value)
	}
	canon := textproto.CanonicalMIMEHeaderKey(name)
	if _, blocked := blockedResponseHeaders[canon]; blocked {
		return fmt.Errorf("response header %s is managed by the limiter", canon)
	}
	return nil
}

var blockedResponseHeaders = map[string]struct{}{
	headers.NameRetryAfter:          {},
	headers.NameRateLimit:           {},
	headers.NameRateLimitPolicy:     {},
	headers.NameXRateLimitLimit:     {},
	headers.NameXRateLimitRemaining: {},
	headers.NameXRateLimitReset:     {},
	headers.NameContentLength:       {},
	headers.NameTransferEncoding:    {},
	headers.NameConnection:          {},
	headers.NameKeepAlive:           {},
	headers.NameProxyConnection:     {},
	headers.NameTe:                  {},
	headers.NameTrailer:             {},
	headers.NameUpgrade:             {},
}
