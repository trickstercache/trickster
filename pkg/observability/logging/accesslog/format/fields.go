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

// Package format compiles Apache-style %-token access log format strings
// into fast per-request line renderers.
package format

import (
	"net/http"
	"slices"
	"strings"
	"time"
)

// Fields holds the per-request values available to access log tokens
type Fields struct {
	// StartTime is when the request was received
	StartTime time.Time
	// Duration is the total time taken to serve the request
	Duration time.Duration
	// ClientIP is the client's IP address as resolved through the listener's trusted proxies
	ClientIP string
	// RemoteIP is the IP address of the connection peer, which is a proxy when one is trusted
	RemoteIP string
	// User is the authenticated username, if any
	User string
	// Method is the HTTP request method
	Method string
	// RequestURI is the original request URI (path + query)
	RequestURI string
	// Path is the request URL path
	Path string
	// Query is the raw query string without the leading '?'
	Query string
	// Proto is the request protocol (e.g., HTTP/1.1)
	Proto string
	// Host is the requested virtual host
	Host string
	// LocalIP is the IP of the listener that accepted the request
	LocalIP string
	// LocalPort is the port of the listener that accepted the request
	LocalPort string
	// Status is the response status code
	Status int
	// BytesWritten is the number of response body bytes written
	BytesWritten int64
	// ReqHeader is the request header collection
	ReqHeader http.Header
	// RespHeader is the response header collection
	RespHeader http.Header
	// Backend is the Trickster backend name that served the request
	Backend string
	// Provider is the backend provider type
	Provider string
	// PathConfig is the matched path configuration's path
	PathConfig string
	// CacheStatus is the cache result (hit, phit, kmiss, ...)
	CacheStatus string
	// Engine is the proxy engine that handled the request
	Engine string
	// UpstreamAddr is the host:port of the origin the request was proxied to
	UpstreamAddr string
	// UpstreamStatus is the status code the origin answered with, or zero when none was made
	UpstreamStatus int
	// UpstreamDuration is the time taken by the origin exchange
	UpstreamDuration time.Duration
	// TraceID and SpanID identify the request's trace when tracing is enabled
	TraceID, SpanID string
	// RequestID is the request's X-Request-ID, received or assigned
	RequestID string
	// Extra holds the static values declared for the route at registration
	Extra Extra
}

// ExtraField is one declared key and its static value.
type ExtraField struct {
	Key, Value string
}

// Extra is a key-sorted list of static values available to %{key}e tokens.
type Extra []ExtraField

// NewExtra returns the sorted Extra list for m, or nil when m is empty.
func NewExtra(m map[string]string) Extra {
	if len(m) == 0 {
		return nil
	}
	out := make(Extra, 0, len(m))
	for k, v := range m {
		out = append(out, ExtraField{Key: k, Value: v})
	}
	slices.SortFunc(out, func(a, b ExtraField) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// Get returns the value declared for key, and whether it was declared.
func (e Extra) Get(key string) (string, bool) {
	for _, f := range e {
		if f.Key == key {
			return f.Value, true
		}
	}
	return "", false
}
