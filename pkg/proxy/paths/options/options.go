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

package options

import (
	"net/http"

	"github.com/tricksterproxy/trickster/pkg/cache/key"
	"github.com/tricksterproxy/trickster/pkg/proxy/forwarding"
	"github.com/tricksterproxy/trickster/pkg/proxy/methods"
	"github.com/tricksterproxy/trickster/pkg/proxy/paths/matching"
	"github.com/tricksterproxy/trickster/pkg/proxy/request/rewriter"
	"github.com/tricksterproxy/trickster/pkg/util/strings"
	ts "github.com/tricksterproxy/trickster/pkg/util/strings"
)

// Options defines a URL Path that is associated with an HTTP Handler
type Options struct {
	// Path indicates the HTTP Request's URL PATH to which this configuration applies
	Path string `toml:"path"`
	// MatchTypeName indicates the type of path match the router will apply to the path ('exact' or 'prefix')
	MatchTypeName string `toml:"match_type"`
	// HandlerName provides the name of the HTTP handler to use
	HandlerName string `toml:"handler"`
	// Methods provides the list of permitted HTTP request methods for this Path
	Methods []string `toml:"methods"`
	// CacheKeyParams provides the list of http request query parameters to be included
	//  in the hash for each request's cache key
	CacheKeyParams []string `toml:"cache_key_params"`
	// CacheKeyHeaders provides the list of http request headers to be included in the hash for each request's cache key
	CacheKeyHeaders []string `toml:"cache_key_headers"`
	// CacheKeyFormFields provides the list of http request body fields to be included
	// in the hash for each request's cache key
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
	// CollapsedForwardingName indicates 'basic' or 'progressive' Collapsed Forwarding to be used by this path.
	CollapsedForwardingName string `toml:"collapsed_forwarding"`
	// ReqRewriterName is the name of a configured Rewriter that will modify the request prior to
	// processing by the origin client
	ReqRewriterName string `toml:"req_rewriter_name"`

	// Handler is the HTTP Handler represented by the Path's HandlerName
	Handler http.Handler `toml:"-"`
	// ResponseBodyBytes provides a byte slice version of the ResponseBody value
	ResponseBodyBytes []byte `toml:"-"`
	// MatchType is the PathMatchType representation of MatchTypeName
	MatchType matching.PathMatchType `toml:"-"`
	// CollapsedForwardingType is the typed representation of CollapsedForwardingName
	CollapsedForwardingType forwarding.CollapsedForwardingType `toml:"-"`
	// KeyHasher points to an optional function that hashes the cacheKey with a custom algorithm
	// NOTE: This is used by some origins like IronDB, but is not configurable by end users.
	// Due to a bug in the vendored toml package, this must be a slice to avoid panic
	KeyHasher []key.HasherFunc `toml:"-"`
	// Custom is a compiled list of any custom settings for this path from the config file
	Custom []string `toml:"-"`
	// ReqRewriter is the rewriter handler as indicated by RuleName
	ReqRewriter rewriter.RewriteInstructions

	// NoMetrics, when set to true, disables metrics decoration for the path
	NoMetrics bool `toml:"no_metrics"`
	// HasCustomResponseBody is a boolean indicating if the response body is custom
	// this flag allows an empty string response to be configured as a return value
	HasCustomResponseBody bool `toml:"-"`
}

// NewOptions returns a newly-instantiated *Options
func NewOptions() *Options {
	return &Options{
		Path:                    "/",
		Methods:                 methods.CacheableHTTPMethods(),
		HandlerName:             "proxy",
		MatchTypeName:           "exact",
		MatchType:               matching.PathMatchTypeExact,
		CollapsedForwardingName: "basic",
		CollapsedForwardingType: forwarding.CFTypeBasic,
		CacheKeyParams:          make([]string, 0),
		CacheKeyHeaders:         make([]string, 0),
		CacheKeyFormFields:      make([]string, 0),
		Custom:                  make([]string, 0),
		RequestHeaders:          make(map[string]string),
		RequestParams:           make(map[string]string),
		ResponseHeaders:         make(map[string]string),
		KeyHasher:               nil,
	}
}

// Clone returns an exact copy of the subject Options
func (o *Options) Clone() *Options {
	c := &Options{
		Path: o.Path,
		//		OriginConfig:            o.OriginConfig,
		MatchTypeName:           o.MatchTypeName,
		MatchType:               o.MatchType,
		HandlerName:             o.HandlerName,
		Handler:                 o.Handler,
		RequestHeaders:          ts.CloneMap(o.RequestHeaders),
		RequestParams:           ts.CloneMap(o.RequestParams),
		ReqRewriter:             o.ReqRewriter,
		ReqRewriterName:         o.ReqRewriterName,
		ResponseHeaders:         ts.CloneMap(o.ResponseHeaders),
		ResponseBody:            o.ResponseBody,
		ResponseBodyBytes:       o.ResponseBodyBytes,
		CollapsedForwardingName: o.CollapsedForwardingName,
		CollapsedForwardingType: o.CollapsedForwardingType,
		NoMetrics:               o.NoMetrics,
		HasCustomResponseBody:   o.HasCustomResponseBody,
		Methods:                 make([]string, len(o.Methods)),
		CacheKeyParams:          make([]string, len(o.CacheKeyParams)),
		CacheKeyHeaders:         make([]string, len(o.CacheKeyHeaders)),
		CacheKeyFormFields:      make([]string, len(o.CacheKeyFormFields)),
		Custom:                  make([]string, len(o.Custom)),
		KeyHasher:               o.KeyHasher,
	}
	copy(c.Methods, o.Methods)
	copy(c.CacheKeyParams, o.CacheKeyParams)
	copy(c.CacheKeyHeaders, o.CacheKeyHeaders)
	copy(c.CacheKeyFormFields, o.CacheKeyFormFields)
	copy(c.Custom, o.Custom)
	return c
}

// Merge merges the non-default values of the provided Options into the subject Options
func (o *Options) Merge(o2 *Options) {
	if o.Custom == nil {
		o.Custom = make([]string, 0, len(o2.Custom))
	}
	for _, c := range o2.Custom {
		o.Custom = append(o.Custom, c)
		switch c {
		case "path":
			o.Path = o2.Path
		case "match_type":
			o.MatchType = o2.MatchType
			o.MatchTypeName = o2.MatchTypeName
		case "handler":
			o.HandlerName = o2.HandlerName
			o.Handler = o2.Handler
		case "methods":
			o.Methods = o2.Methods
		case "cache_key_params":
			o.CacheKeyParams = o2.CacheKeyParams
		case "cache_key_headers":
			o.CacheKeyHeaders = o2.CacheKeyHeaders
		case "cache_key_form_fields":
			o.CacheKeyFormFields = o2.CacheKeyFormFields
		case "request_headers":
			o.RequestHeaders = o2.RequestHeaders
		case "request_params":
			o.RequestParams = o2.RequestParams
		case "response_headers":
			o.ResponseHeaders = o2.ResponseHeaders
		case "response_code":
			o.ResponseCode = o2.ResponseCode
		case "response_body":
			o.ResponseBody = o2.ResponseBody
			o.HasCustomResponseBody = true
			o.ResponseBodyBytes = o2.ResponseBodyBytes
		case "no_metrics":
			o.NoMetrics = o2.NoMetrics
		case "collapsed_forwarding":
			o.CollapsedForwardingName = o2.CollapsedForwardingName
			o.CollapsedForwardingType = o2.CollapsedForwardingType
		case "req_rewriter_name":
			o.ReqRewriterName = o2.ReqRewriterName
			o.ReqRewriter = o2.ReqRewriter
		}
	}
	o.Custom = strings.Unique(o.Custom)
}
