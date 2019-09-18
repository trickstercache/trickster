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

package config

import (
	"net/http"
	"time"
)

// PathConfig defines a URL Path that is associated with an HTTP Handler
type PathConfig struct {
	// Path indicates the HTTP Request's URL PATH to which this configuration applies
	Path string `toml:"path"`
	// HandlerName provides the name of the HTTP handler to use
	HandlerName string `toml:"handler"`
	// Methods provides the list of permitted HTTP request methods for this Path
	Methods []string `toml:"methods"`
	// CacheKeyParams provides the list of http request query parameters to be included in the hash for each query's cache key
	CacheKeyParams []string `toml:"cache_key_params"`
	// CacheKeyHeaders provides the list of http request headers to be included in the hash for each query's cache key
	CacheKeyHeaders []string `toml:"cache_key_headers"`
	// DefaultTTLSecs indicates the TTL Cache for this path. If
	DefaultTTLSecs int `toml:"default_ttl_secs"`
	// RequestHeaders is a map of headers that will be added to requests to the upstream Origin for this path
	RequestHeaders map[string]string `toml:"request_headers"`
	// ResponseHeaders is a map of http headers that will be added to responses to the downstream client
	ResponseHeaders map[string]string `toml:"response_headers"`
	// ResponseCode sets a custom response code to be sent to downstream clients for this path.
	ResponseCode int `toml:"response_code"`
	// ResponseBody sets a custom response body to be sent to the donstream client for this path.
	ResponseBody string `toml:"response_body"`
	// NoMetrics, when set to true, disables metrics decoration for the path
	NoMetrics bool `toml:"no_metrics"`

	// Synthesized PathConfig Values
	//
	// DefaultTTL is the time.Duration representation of DefaultTTLSecs
	DefaultTTL time.Duration `toml:"-"`
	// Handler is the HTTP Handler represented by the Path's HandlerName
	Handler http.Handler `toml:"-"`
	// Order is this Path's order index in the list of configured Paths
	Order int `toml:"-"`
	// HasCustomResponseBody is a boolean indicating if the response body is custom
	// this flag allows an empty string response to be configured as a return value
	HasCustomResponseBody bool `toml:"-"`
	// ResponseBodyBytes provides a byte slice version of the ResponseBody value
	ResponseBodyBytes []byte `toml:"-"`

	custom []string `toml:"-"`
}

// Merge merges the non-default values of the provided ProxyPathConfig into the subject ProxyPathConfig
func (p *PathConfig) Merge(p2 *PathConfig) {
	for _, c := range p.custom {
		switch c {
		case "path":
			p.Path = p2.Path
		case "handler":
			p.HandlerName = p2.HandlerName
			p.Handler = p2.Handler
		case "methods":
			p.Methods = p2.Methods
		case "cache_key_params":
			p.CacheKeyParams = p2.CacheKeyParams
		case "cache_key_headers":
			p.CacheKeyHeaders = p2.CacheKeyHeaders
		case "default_ttl_secs":
			p.DefaultTTLSecs = p2.DefaultTTLSecs
			p.DefaultTTL = p2.DefaultTTL
		case "request_headers":
			p.RequestHeaders = p2.RequestHeaders
		case "response_headers":
			p.ResponseHeaders = p2.ResponseHeaders
		case "response_code":
			p.ResponseCode = p2.ResponseCode
		case "response_body":
			p.ResponseBody = p2.ResponseBody
			p.HasCustomResponseBody = true
			p.ResponseBodyBytes = p2.ResponseBodyBytes
		case "no_metrics":
			p.NoMetrics = p2.NoMetrics
		}
	}
}
