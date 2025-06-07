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
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/key"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/forwarding"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	strutil "github.com/trickstercache/trickster/v2/pkg/util/strings"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"
)

// Options defines a URL Path that is associated with an HTTP Handler
type Options struct {
	// Path indicates the HTTP Request's URL PATH to which this configuration applies
	Path string `yaml:"path,omitempty"`
	// MatchTypeName indicates the type of path match the router will apply to the path ('exact' or 'prefix')
	MatchTypeName string `yaml:"match_type,omitempty"`
	// HandlerName provides the name of the HTTP handler to use
	HandlerName string `yaml:"handler,omitempty"`
	// Methods provides the list of permitted HTTP request methods for this Path
	Methods []string `yaml:"methods,omitempty"`
	// CacheKeyParams provides the list of http request query parameters to be included
	//  in the hash for each request's cache key
	CacheKeyParams []string `yaml:"cache_key_params,omitempty"`
	// CacheKeyHeaders provides the list of http request headers to be included in the hash for each request's cache key
	CacheKeyHeaders []string `yaml:"cache_key_headers,omitempty"`
	// CacheKeyFormFields provides the list of http request body fields to be included
	// in the hash for each request's cache key
	CacheKeyFormFields []string `yaml:"cache_key_form_fields,omitempty"`
	// RequestHeaders is a map of headers that will be added to requests to the upstream Origin for this path
	RequestHeaders types.EnvStringMap `yaml:"request_headers,omitempty"`
	// RequestParams is a map of parameters that will be added to requests to the upstream Origin for this path
	RequestParams types.EnvStringMap `yaml:"request_params,omitempty"`
	// ResponseHeaders is a map of http headers that will be added to responses to the downstream client
	ResponseHeaders types.EnvStringMap `yaml:"response_headers,omitempty"`
	// ResponseCode sets a custom response code to be sent to downstream clients for this path.
	ResponseCode int `yaml:"response_code,omitempty"`
	// ResponseBody sets a custom response body to be sent to the donstream client for this path.
	ResponseBody string `yaml:"response_body,omitempty"`
	// CollapsedForwardingName indicates 'basic' or 'progressive' Collapsed Forwarding to be used by this path.
	CollapsedForwardingName string `yaml:"collapsed_forwarding,omitempty"`
	// ReqRewriterName is the name of a configured Rewriter that will modify the request prior to
	// processing by the backend client
	ReqRewriterName string `yaml:"req_rewriter_name,omitempty"`
	// NoMetrics, when set to true, disables metrics decoration for the path
	NoMetrics bool `yaml:"no_metrics"`
	// AuthenticatorName specifies the name of the optional Authenticator to attach to this Path
	AuthenticatorName string `yaml:"authenticator_name,omitempty"`

	// Handler is the HTTP Handler represented by the Path's HandlerName
	Handler http.Handler `yaml:"-"`
	// ResponseBodyBytes provides a byte slice version of the ResponseBody value
	ResponseBodyBytes []byte `yaml:"-"`
	// MatchType is the PathMatchType representation of MatchTypeName
	MatchType matching.PathMatchType `yaml:"-"`
	// CollapsedForwardingType is the typed representation of CollapsedForwardingName
	CollapsedForwardingType forwarding.CollapsedForwardingType `yaml:"-"`
	// KeyHasher points to an optional function that hashes the cacheKey with a custom algorithm
	// NOTE: This can be used by backends, but is not configurable by end users.
	KeyHasher key.HasherFunc `yaml:"-"`
	// Custom is a compiled list of any custom settings for this path from the config file
	Custom []string `yaml:"-"`
	// ReqRewriter is the rewriter handler as indicated by RuleName
	ReqRewriter rewriter.RewriteInstructions `yaml:"-"`
	// AuthOptions is the authenticator as indicated by AuthenticatorName
	AuthOptions *autho.Options `yaml:"-"`
	// HasCustomResponseBody is a boolean indicating if the response body is custom
	// this flag allows an empty string response to be configured as a return value
	HasCustomResponseBody bool `yaml:"-"`
}

// List is a slice of *Options
type List []*Options

// Lookup is a map of *Options
type Lookup map[string]*Options

// New returns a newly-instantiated path *Options
func New() *Options {
	return &Options{
		Path:                    "/",
		Methods:                 methods.CacheableHTTPMethods(),
		HandlerName:             providers.Proxy,
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
		Path:                    o.Path,
		MatchTypeName:           o.MatchTypeName,
		MatchType:               o.MatchType,
		HandlerName:             o.HandlerName,
		Handler:                 o.Handler,
		RequestHeaders:          maps.Clone(o.RequestHeaders),
		RequestParams:           maps.Clone(o.RequestParams),
		ReqRewriter:             o.ReqRewriter,
		ReqRewriterName:         o.ReqRewriterName,
		ResponseHeaders:         maps.Clone(o.ResponseHeaders),
		ResponseBody:            o.ResponseBody,
		ResponseBodyBytes:       o.ResponseBodyBytes,
		CollapsedForwardingName: o.CollapsedForwardingName,
		CollapsedForwardingType: o.CollapsedForwardingType,
		NoMetrics:               o.NoMetrics,
		HasCustomResponseBody:   o.HasCustomResponseBody,
		Methods:                 slices.Clone(o.Methods),
		CacheKeyParams:          slices.Clone(o.CacheKeyParams),
		CacheKeyHeaders:         slices.Clone(o.CacheKeyHeaders),
		CacheKeyFormFields:      slices.Clone(o.CacheKeyFormFields),
		Custom:                  slices.Clone(o.Custom),
		KeyHasher:               o.KeyHasher,
		AuthenticatorName:       o.AuthenticatorName,
	}
	if o.AuthOptions != nil {
		c.AuthOptions = o.AuthOptions.Clone()
	}
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
		case "authenticator_name":
			o.AuthenticatorName = o2.AuthenticatorName
			if o2.AuthOptions != nil {
				o.AuthOptions = o2.AuthOptions.Clone()
			}
		}
	}
	o.Custom = strutil.Unique(o.Custom)
}

var pathMembers = []string{"path", "match_type", "handler", "methods", "cache_key_params",
	"cache_key_headers", "default_ttl", "request_params", "request_headers", "response_headers",
	"response_headers", "response_code", "response_body", "no_metrics", "collapsed_forwarding",
	"req_rewriter_name", "authenticator_name",
}

var errInvalidConfigMetadata = errors.New("invalid config y")

func OverlayYAMLData(
	backendName string,
	paths Lookup,
	y yamlx.KeyLookup,
) (Lookup, error) {
	if y == nil {
		return nil, errInvalidConfigMetadata
	}
	out := make(Lookup, len(paths))
	for k, o := range paths {
		p := o.Clone()
		if len(p.Methods) == 0 {
			p.Methods = []string{http.MethodGet}
		}
		p.Custom = make([]string, 0)
		for _, pm := range pathMembers {
			if y.IsDefined("backends", backendName, "paths", k, pm) {
				p.Custom = append(p.Custom, pm)
			}
		}
		if y.IsDefined("backends", backendName, "paths", k, "response_body") {
			p.ResponseBodyBytes = []byte(p.ResponseBody)
			p.HasCustomResponseBody = true
		}
		if y.IsDefined("backends", backendName, "paths", k, "collapsed_forwarding") {
			if _, ok := forwarding.CollapsedForwardingTypeNames[p.CollapsedForwardingName]; !ok {
				return nil, fmt.Errorf("invalid collapsed_forwarding name: %s", p.CollapsedForwardingName)
			}
			p.CollapsedForwardingType =
				forwarding.GetCollapsedForwardingType(p.CollapsedForwardingName)
		} else {
			p.CollapsedForwardingType = forwarding.CFTypeBasic
		}
		if mt, ok := matching.Names[strings.ToLower(p.MatchTypeName)]; ok {
			p.MatchType = mt
			p.MatchTypeName = p.MatchType.String()
		} else {
			p.MatchType = matching.PathMatchTypeExact
			p.MatchTypeName = p.MatchType.String()
		}
		out[p.Path] = p
	}
	return out, nil
}

func (o *Options) Validate() error {
	// placeholder for future validations as needed (currently there are none)
	return nil
}

func (l Lookup) Validate() error {
	for _, o := range l {
		if err := o.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (o List) ToLookup() Lookup {
	out := make(Lookup, len(o))
	for _, o2 := range o {
		out[o2.Path+"-"+strings.Join(o2.Methods, "-")] = o2
	}
	return out
}
