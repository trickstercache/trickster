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

// Package redact masks credentials in the request values that logs record.
package redact

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// Value replaces each redacted value.
const Value = "REDACTED"

// maxStackName is the longest parameter name lowercased without allocating.
const maxStackName = 64

// queryParams are redacted by default, matched without regard to case.
var queryParams = []string{
	"password", "passwd", "pass", "key", "api_key", "apikey", "access_token",
	"token", "code", "state", "client_secret", "signature", "sig",
}

// urlHeaders hold a URL whose query is redacted like the request's.
var urlHeaders = []string{headers.NameReferer, headers.NameLocation}

// Options configures the redaction applied to access logs and engine logs.
type Options struct {
	// Enabled turns redaction off only when explicitly false
	Enabled *bool `yaml:"enabled,omitempty"`
	// QueryParams adds query parameter names to the built-in list
	QueryParams []string `yaml:"query_params,omitempty"`
	// Headers adds header names to the built-in list
	Headers []string `yaml:"headers,omitempty"`
	// Cookies names the cookies whose values are redacted; none by default
	Cookies []string `yaml:"cookies,omitempty"`
}

// Clone returns a copy of the Options.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	if o.Enabled != nil {
		enabled := *o.Enabled
		out.Enabled = &enabled
	}
	out.QueryParams = slices.Clone(o.QueryParams)
	out.Headers = slices.Clone(o.Headers)
	out.Cookies = slices.Clone(o.Cookies)
	return &out
}

// IsEnabled reports whether redaction applies, which it does unless explicitly disabled.
func (o *Options) IsEnabled() bool {
	return o == nil || o.Enabled == nil || *o.Enabled
}

type rules struct {
	disabled    bool
	queryParams map[string]struct{}
	maxParamLen int
	headers     map[string]struct{}
	cookies     map[string]struct{}
}

var active atomic.Pointer[rules]

func init() {
	Configure(nil)
}

// Configure replaces the active rules; nil restores the defaults.
func Configure(o *Options) {
	r := &rules{
		disabled:    !o.IsEnabled(),
		queryParams: make(map[string]struct{}, len(queryParams)),
		headers:     make(map[string]struct{}),
		cookies:     make(map[string]struct{}),
	}
	names := queryParams
	if o != nil {
		names = append(slices.Clone(queryParams), o.QueryParams...)
		for _, h := range o.Headers {
			r.headers[http.CanonicalHeaderKey(h)] = struct{}{}
		}
		for _, c := range o.Cookies {
			r.cookies[c] = struct{}{}
		}
	}
	for _, name := range names {
		r.queryParams[string(asciiLower(make([]byte, len(name)), name))] = struct{}{}
		r.maxParamLen = max(r.maxParamLen, len(name))
	}
	active.Store(r)
}

// Query returns raw with the values of sensitive parameters replaced, or raw
// itself when nothing matches.
func Query(raw string) string {
	r := active.Load()
	if r.disabled || raw == "" {
		return raw
	}
	out, _ := r.query(raw)
	return out
}

// URI redacts the query of a request URI or URL string, keeping any fragment.
func URI(s string) string {
	r := active.Load()
	i := strings.IndexByte(s, '?')
	if r.disabled || i < 0 {
		return s
	}
	q, fragment := s[i+1:], ""
	if j := strings.IndexByte(q, '#'); j >= 0 {
		q, fragment = q[:j], q[j:]
	}
	out, changed := r.query(q)
	if !changed {
		return s
	}
	return s[:i+1] + out + fragment
}

// URL renders u for a log with any userinfo password and sensitive query
// values redacted.
func URL(u *url.URL) string {
	if u == nil {
		return ""
	}
	if active.Load().disabled {
		return u.String()
	}
	return URI(u.Redacted())
}

// Error renders err for a log, redacting the query of the URL that a wrapped
// *url.Error prints.
func Error(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	var urlErr *url.Error
	if active.Load().disabled || !errors.As(err, &urlErr) {
		return s
	}
	redacted := URI(urlErr.URL)
	if redacted == urlErr.URL {
		return s
	}
	s = strings.ReplaceAll(s, strconv.Quote(urlErr.URL), strconv.Quote(redacted))
	return strings.ReplaceAll(s, urlErr.URL, redacted)
}

// Header returns value with any credential it carries redacted.
func Header(name, value string) string {
	r := active.Load()
	if r.disabled || value == "" {
		return value
	}
	name = http.CanonicalHeaderKey(name)
	if _, ok := r.headers[name]; ok || headers.IsSensitive(name) {
		return Value
	}
	if slices.Contains(urlHeaders, name) {
		return URI(value)
	}
	return value
}

// Cookie returns value, or Value when the named cookie is configured as sensitive.
func Cookie(name, value string) string {
	r := active.Load()
	if r.disabled || value == "" {
		return value
	}
	if _, ok := r.cookies[name]; ok {
		return Value
	}
	return value
}

// query replaces the values of sensitive parameters, allocating only when one matches.
func (r *rules) query(raw string) (string, bool) {
	var sb strings.Builder
	matched := false
	for start := 0; ; {
		end := strings.IndexByte(raw[start:], '&')
		if end < 0 {
			end = len(raw)
		} else {
			end += start
		}
		pair := raw[start:end]
		key, value, _ := strings.Cut(pair, "=")
		if value != "" && r.isSensitiveParam(key) {
			if !matched {
				matched = true
				sb.Grow(len(raw) + len(Value))
				sb.WriteString(raw[:start])
			}
			sb.WriteString(key)
			sb.WriteByte('=')
			sb.WriteString(Value)
		} else if matched {
			sb.WriteString(pair)
		}
		if end == len(raw) {
			break
		}
		if matched {
			sb.WriteByte('&')
		}
		start = end + 1
	}
	if !matched {
		return raw, false
	}
	return sb.String(), true
}

func (r *rules) isSensitiveParam(key string) bool {
	if strings.ContainsAny(key, "%+") {
		if k, err := url.QueryUnescape(key); err == nil {
			key = k
		}
	}
	if key == "" || len(key) > r.maxParamLen {
		return false
	}
	var buf [maxStackName]byte
	var lower []byte
	if len(key) <= maxStackName {
		lower = buf[:len(key)]
	} else {
		lower = make([]byte, len(key))
	}
	_, ok := r.queryParams[string(asciiLower(lower, key))]
	return ok
}

// asciiLower writes s into dst, of the same length, with ASCII letters lowercased.
func asciiLower(dst []byte, s string) []byte {
	for i := range len(s) {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		dst[i] = c
	}
	return dst
}
