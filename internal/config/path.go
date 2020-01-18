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
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Comcast/trickster/internal/proxy/methods"
	ts "github.com/Comcast/trickster/internal/util/strings"
)

// PathMatchType enumerates the types of Path Matches used when registering Paths with the Router
type PathMatchType int

// KeyHasherFunc is a custom function that returns a hashed key value string for cache objects
type KeyHasherFunc func(path string, params url.Values, headers http.Header, body io.ReadCloser, extra string) string

const (
	// PathMatchTypeExact indicates the router will map the Path by exact match against incoming requests
	PathMatchTypeExact = PathMatchType(iota)
	// PathMatchTypePrefix indicates the router will map the Path by prefix against incoming requests
	PathMatchTypePrefix
)

var pathMatchTypeNames = map[string]PathMatchType{
	"exact":  PathMatchTypeExact,
	"prefix": PathMatchTypePrefix,
}

var pathMatchTypeValues = map[PathMatchType]string{
	PathMatchTypeExact:  "exact",
	PathMatchTypePrefix: "prefix",
}

func (t PathMatchType) String() string {
	if v, ok := pathMatchTypeValues[t]; ok {
		return v
	}
	return strconv.Itoa(int(t))
}

// PathConfig defines a URL Path that is associated with an HTTP Handler
type PathConfig struct {
	// Path indicates the HTTP Request's URL PATH to which this configuration applies
	Path string `toml:"path"`
	// MatchTypeName indicates the type of path match the router will apply to the path ('exact' or 'prefix')
	MatchTypeName string `toml:"match_type"`
	// HandlerName provides the name of the HTTP handler to use
	HandlerName string `toml:"handler"`
	// Methods provides the list of permitted HTTP request methods for this Path
	Methods []string `toml:"methods"`
	// CacheKeyParams provides the list of http request query parameters to be included in the hash for each request's cache key
	CacheKeyParams []string `toml:"cache_key_params"`
	// CacheKeyHeaders provides the list of http request headers to be included in the hash for each request's cache key
	CacheKeyHeaders []string `toml:"cache_key_headers"`
	// CacheKeyFormFields provides the list of http request body fields to be included in the hash for each request's cache key
	CacheKeyFormFields []string `toml:"cache_key_form_fields"`
	// RequestHeaders is a map of headers that will be added to requests to the upstream Origin for this path
	RequestHeaders map[string]string `toml:"request_headers"`
	// RequestParams is a map of headers that will be added to requests to the upstream Origin for this path
	RequestParams map[string]string `toml:"request_params"`
	// ResponseHeaders is a map of http headers that will be added to responses to the downstream client
	ResponseHeaders map[string]string `toml:"response_headers"`
	// ResponseCode sets a custom response code to be sent to downstream clients for this path.
	ResponseCode int `toml:"response_code"`
	// ResponseBody sets a custom response body to be sent to the donstream client for this path.
	ResponseBody string `toml:"response_body"`
	// NoMetrics, when set to true, disables metrics decoration for the path
	NoMetrics bool `toml:"no_metrics"`
	// CollapsedForwardingName indicates 'basic' or 'progressive' Collapsed Forwarding to be used by this path.
	CollapsedForwardingName string `toml:"collapsed_forwarding"`

	// Synthesized PathConfig Values
	//
	// Handler is the HTTP Handler represented by the Path's HandlerName
	Handler http.Handler `toml:"-"`
	// HasCustomResponseBody is a boolean indicating if the response body is custom
	// this flag allows an empty string response to be configured as a return value
	HasCustomResponseBody bool `toml:"-"`
	// ResponseBodyBytes provides a byte slice version of the ResponseBody value
	ResponseBodyBytes []byte `toml:"-"`
	// MatchType is the PathMatchType representation of MatchTypeName
	MatchType PathMatchType `toml:"-"`
	// CollapsedForwardingType is the typed representation of CollapsedForwardingName
	CollapsedForwardingType CollapsedForwardingType `toml:"-"`
	// OriginConfig is the reference to the PathConfig's parent Origin Config
	OriginConfig *OriginConfig `toml:"-"`
	// KeyHasher points to an optional function that hashes the cacheKey with a custom algorithm
	// NOTE: This is used by some origins like IronDB, but is not configurable by end users
	// due to a bug in the vendored toml package, this must be a slice to avoid panic
	KeyHasher []KeyHasherFunc `toml:"-"`

	custom []string `toml:"-"`
}

// NewPathConfig returns a newly-instantiated *PathConfig
func NewPathConfig() *PathConfig {
	return &PathConfig{
		Path:                    "/",
		Methods:                 methods.CacheableHTTPMethods(),
		HandlerName:             "proxy",
		MatchTypeName:           "exact",
		MatchType:               PathMatchTypeExact,
		CollapsedForwardingName: "basic",
		CollapsedForwardingType: CFTypeBasic,
		CacheKeyParams:          make([]string, 0),
		CacheKeyHeaders:         make([]string, 0),
		CacheKeyFormFields:      make([]string, 0),
		custom:                  make([]string, 0),
		RequestHeaders:          make(map[string]string),
		RequestParams:           make(map[string]string),
		ResponseHeaders:         make(map[string]string),
		KeyHasher:               nil,
	}
}

// Clone returns an exact copy of the subject PathConfig
func (p *PathConfig) Clone() *PathConfig {
	c := &PathConfig{
		Path:                    p.Path,
		OriginConfig:            p.OriginConfig,
		MatchTypeName:           p.MatchTypeName,
		MatchType:               p.MatchType,
		HandlerName:             p.HandlerName,
		Handler:                 p.Handler,
		RequestHeaders:          ts.CloneMap(p.RequestHeaders),
		RequestParams:           ts.CloneMap(p.RequestParams),
		ResponseHeaders:         ts.CloneMap(p.ResponseHeaders),
		ResponseBody:            p.ResponseBody,
		ResponseBodyBytes:       p.ResponseBodyBytes,
		CollapsedForwardingName: p.CollapsedForwardingName,
		CollapsedForwardingType: p.CollapsedForwardingType,
		NoMetrics:               p.NoMetrics,
		HasCustomResponseBody:   p.HasCustomResponseBody,
		Methods:                 make([]string, len(p.Methods)),
		CacheKeyParams:          make([]string, len(p.CacheKeyParams)),
		CacheKeyHeaders:         make([]string, len(p.CacheKeyHeaders)),
		CacheKeyFormFields:      make([]string, len(p.CacheKeyFormFields)),
		custom:                  make([]string, len(p.custom)),
		KeyHasher:               p.KeyHasher,
	}
	copy(c.Methods, p.Methods)
	copy(c.CacheKeyParams, p.CacheKeyParams)
	copy(c.CacheKeyHeaders, p.CacheKeyHeaders)
	copy(c.CacheKeyFormFields, p.CacheKeyFormFields)
	copy(c.custom, p.custom)
	return c

}

// Merge merges the non-default values of the provided PathConfig into the subject PathConfig
func (p *PathConfig) Merge(p2 *PathConfig) {

	if p2.OriginConfig != nil {
		p.OriginConfig = p2.OriginConfig
	}

	for _, c := range p2.custom {
		switch c {
		case "path":
			p.Path = p2.Path
		case "match_type":
			p.MatchType = p2.MatchType
			p.MatchTypeName = p2.MatchTypeName
		case "handler":
			p.HandlerName = p2.HandlerName
			p.Handler = p2.Handler
		case "methods":
			p.Methods = p2.Methods
		case "cache_key_params":
			p.CacheKeyParams = p2.CacheKeyParams
		case "cache_key_headers":
			p.CacheKeyHeaders = p2.CacheKeyHeaders
		case "cache_key_form_fields":
			p.CacheKeyFormFields = p2.CacheKeyFormFields
		case "request_headers":
			p.RequestHeaders = p2.RequestHeaders
		case "request_params":
			p.RequestParams = p2.RequestParams
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
		case "collapsed_forwarding":
			p.CollapsedForwardingName = p2.CollapsedForwardingName
			p.CollapsedForwardingType = p2.CollapsedForwardingType
		}
	}
}
