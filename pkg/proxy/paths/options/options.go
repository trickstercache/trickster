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
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
	strutil "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

// Options defines a URL Path that is associated with an HTTP Handler
type Options struct {
	// Path indicates the HTTP Request's URL PATH to which this configuration applies
	Path string `yaml:"path,omitempty"`
	// MatchTypeName indicates the type of path match the router will apply to the path ('exact' or 'prefix')
	MatchTypeName matching.PathMatchName `yaml:"match_type,omitempty"`
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
	ResponseBody *string `yaml:"response_body,omitempty"`
	// CollapsedForwardingName indicates 'basic' or 'progressive' Collapsed Forwarding to be used by this path.
	CollapsedForwardingName string `yaml:"collapsed_forwarding,omitempty"`
	// ReqRewriterName is the name of a configured Rewriter that will modify the request prior to
	// processing by the backend client
	ReqRewriterName string `yaml:"req_rewriter_name,omitempty"`
	// NoMetrics, when set to true, disables metrics decoration for the path
	NoMetrics bool `yaml:"no_metrics,omitempty"`
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
	// ReqRewriter is the rewriter handler as indicated by RuleName
	ReqRewriter rewriter.RewriteInstructions `yaml:"-"`
	// AuthOptions is the authenticator as indicated by AuthenticatorName
	AuthOptions *autho.Options `yaml:"-"`
}

// List is a slice of *Options
type List []*Options

// Lookup is a map of *Options
type Lookup map[string]*Options

var _ types.ConfigOptions[Options] = &Options{}

// New returns a newly-instantiated path *Options
func New() *Options {
	return &Options{
		Path:                    DefaultPath,
		Methods:                 methods.CacheableHTTPMethods(),
		HandlerName:             providers.Proxy,
		MatchTypeName:           matching.PathMatchNameExact,
		MatchType:               matching.PathMatchTypeExact,
		CollapsedForwardingName: forwarding.CFNameBasic,
		CollapsedForwardingType: forwarding.CFTypeBasic,
		CacheKeyParams:          make([]string, 0),
		CacheKeyHeaders:         make([]string, 0),
		CacheKeyFormFields:      make([]string, 0),
		RequestHeaders:          make(map[string]string),
		RequestParams:           make(map[string]string),
		ResponseHeaders:         make(map[string]string),
		KeyHasher:               nil,
	}
}

// Clone returns an exact copy of the subject Options
func (o *Options) Clone() *Options {
	out := pointers.Clone(o)
	out.RequestHeaders = maps.Clone(o.RequestHeaders)
	out.RequestParams = maps.Clone(o.RequestParams)
	out.ResponseHeaders = maps.Clone(o.ResponseHeaders)
	out.Methods = slices.Clone(o.Methods)
	out.CacheKeyParams = slices.Clone(o.CacheKeyParams)
	out.CacheKeyHeaders = slices.Clone(o.CacheKeyHeaders)
	out.CacheKeyFormFields = slices.Clone(o.CacheKeyFormFields)

	out.ResponseBody = pointers.Clone(o.ResponseBody)
	if out.ResponseBody != nil {
		out.ResponseBodyBytes = []byte(*out.ResponseBody)
	}
	if o.AuthOptions != nil {
		out.AuthOptions = o.AuthOptions.Clone()
	}
	return out
}

// Initialize sets up the path Options with default values and overlays
// any values that were set during YAML unmarshaling
func (o *Options) Initialize(_ string) error {
	if len(o.Methods) == 0 {
		o.Methods = []string{http.MethodGet}
	}

	if o.MatchTypeName == "" {
		o.MatchTypeName = matching.PathMatchNameExact
		o.MatchType = matching.PathMatchTypeExact
	} else {
		o.MatchTypeName = matching.PathMatchName(strings.ToLower(string(o.MatchTypeName)))
		if mt, ok := matching.Names[o.MatchTypeName]; ok {
			o.MatchType = mt
		} else {
			o.MatchType = matching.PathMatchTypeExact
			o.MatchTypeName = matching.PathMatchNameExact
		}
	}

	if o.CollapsedForwardingName == "" {
		o.CollapsedForwardingType = forwarding.CFTypeBasic
	} else {
		o.CollapsedForwardingType = forwarding.GetCollapsedForwardingType(o.CollapsedForwardingName)
	}

	if o.ResponseBody != nil && *o.ResponseBody != "" {
		o.ResponseBodyBytes = []byte(*o.ResponseBody)
	}

	return nil
}

// Initialize initializes all path options in the lookup
func (l Lookup) Initialize() error {
	for _, o := range l {
		if err := o.Initialize(""); err != nil {
			return err
		}
	}
	return nil
}

func (o *Options) Validate() (bool, error) {
	normalized := matching.PathMatchName(strings.ToLower(string(o.MatchTypeName)))
	if _, ok := matching.Names[normalized]; !ok && o.MatchTypeName != "" {
		return false, fmt.Errorf("invalid match_type: %s", o.MatchTypeName)
	}
	for _, method := range o.Methods {
		if !methods.IsValidMethod(method) {
			return false, fmt.Errorf("invalid HTTP method: %s", method)
		}
	}
	if o.CollapsedForwardingName != "" {
		if _, ok := forwarding.CollapsedForwardingTypeNames[o.CollapsedForwardingName]; !ok {
			return false, fmt.Errorf("invalid collapsed_forwarding name: %s", o.CollapsedForwardingName)
		}
	}
	if o.ResponseCode != 0 && (o.ResponseCode < 100 || o.ResponseCode >= 600) {
		return false, fmt.Errorf("invalid response_code: %d (must be between 100 and 599)", o.ResponseCode)
	}
	return true, nil
}

func (l List) Validate() error {
	for _, o := range l {
		_, err := o.Validate()
		if err != nil {
			return err
		}
	}
	return nil
}

func (l List) Clone() List {
	out := make(List, len(l))
	for i, o := range l {
		out[i] = o.Clone()
	}
	return out
}

func (l List) Initialize() error {
	for _, o := range l {
		if err := o.Initialize(""); err != nil {
			return err
		}
	}
	return nil
}

func (l List) Overlay(l2 List) List {
	l2ByPath := make(map[string][]*Options)
	for _, o2 := range l2 {
		if o2 != nil {
			l2ByPath[o2.Path] = append(l2ByPath[o2.Path], o2)
		}
	}
	l2Processed := make(map[string]bool)
	out := make(List, 0, len(l)+len(l2))
	for _, o := range l {
		if o == nil {
			continue
		}
		l2Matches, hasMatch := l2ByPath[o.Path]
		if !hasMatch {
			out = append(out, o)
			continue
		}
		replacedMethods := make(map[string]bool)
		for _, o2 := range l2Matches {
			if o2 == nil {
				continue
			}
			l2Processed[o.Path] = true
			remainingMethods := make([]string, 0, len(o.Methods))
			for _, m := range o.Methods {
				if !replacedMethods[m] {
					remainingMethods = append(remainingMethods, m)
				}
			}
			if methods.HasAll(remainingMethods, o2.Methods) {
				out = append(out, o2)
				for _, m := range remainingMethods {
					replacedMethods[m] = true
				}
			} else if !methods.HasAny(remainingMethods, o2.Methods) {
				if len(remainingMethods) > 0 {
					oClone := o.Clone()
					oClone.Methods = remainingMethods
					out = append(out, oClone)
				} else {
					out = append(out, o)
				}
				out = append(out, o2)
			} else {
				overlappingMethods := make([]string, 0)
				for _, m := range remainingMethods {
					if slices.Contains(o2.Methods, m) {
						overlappingMethods = append(overlappingMethods, m)
						replacedMethods[m] = true
					}
				}
				oClone := o.Clone()
				oClone.Methods = strutil.Pare(remainingMethods, overlappingMethods)
				if len(oClone.Methods) > 0 {
					out = append(out, oClone)
				}
				out = append(out, o2)
			}
		}
	}
	for _, o2 := range l2 {
		if o2 != nil && !l2Processed[o2.Path] {
			out = append(out, o2)
		}
	}
	return out
}

func (o *Options) UnmarshalYAML(unmarshal func(any) error) error {
	type loadOptions Options
	lo := loadOptions(*(New()))
	if err := unmarshal(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
