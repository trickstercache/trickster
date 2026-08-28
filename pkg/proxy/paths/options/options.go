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
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/key"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	corso "github.com/trickstercache/trickster/v2/pkg/proxy/cors/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/forwarding"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
	strutil "github.com/trickstercache/trickster/v2/pkg/util/strings"

	"go.yaml.in/yaml/v3"
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
	// CORS overrides the backend CORS response-header policy for this path
	CORS *corso.Options `yaml:"cors,omitempty"`
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
	// Regexp is the compiled representation of Path when MatchType is
	// PathMatchTypeRegex, compiled once at config load; compiled regexps are
	// immutable and safe for concurrent use, so Clone copies the pointer
	Regexp *regexp.Regexp `yaml:"-"`
	// CollapsedForwardingType is the typed representation of CollapsedForwardingName
	CollapsedForwardingType forwarding.CollapsedForwardingType `yaml:"-"`
	// KeyHasher points to an optional function that hashes the cacheKey with a custom algorithm
	// NOTE: This can be used by backends, but is not configurable by end users.
	KeyHasher key.HasherFunc `yaml:"-"`
	// ReqRewriter is the rewriter handler as indicated by RuleName
	ReqRewriter rewriter.RewriteInstructions `yaml:"-"`
	// AuthOptions is the authenticator as indicated by AuthenticatorName
	AuthOptions *autho.Options `yaml:"-"`

	// identityKeyPart is the request_headers/request_params digest,
	// precomputed by Initialize; see IdentityKeyPart
	identityKeyPart string
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
	if o.CORS != nil {
		out.CORS = o.CORS.Clone()
	}
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

	// Expand "*" to all HTTP methods
	// If "*" is present, it replaces all other methods
	if slices.Contains(o.Methods, "*") {
		o.Methods = methods.AllHTTPMethods()
	}

	if isRegexPath(o.Path) {
		// a path starting with ^/ (or the escaped ^\/ form) is always treated
		// as a regex path, regardless of the configured match_type
		o.MatchTypeName = matching.PathMatchNameRegex
		o.MatchType = matching.PathMatchTypeRegex
	} else if o.MatchTypeName == "" {
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

	if o.MatchType == matching.PathMatchTypeRegex {
		if !strings.HasPrefix(o.Path, "^") {
			o.Path = "^" + o.Path
		}
		// compile errors are surfaced by Validate, which reports them with
		// full config context
		if re, err := regexp.Compile(o.Path); err == nil {
			o.Regexp = re
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
	if o.CORS != nil {
		if err := o.CORS.Initialize(""); err != nil {
			return err
		}
	}

	o.identityKeyPart = o.computeIdentityKeyPart()

	return nil
}

// ReplacesHeader reports whether request_headers replaces ("Name") or removes
// ("-Name") the named header upstream; "+Name" appends. name must be canonical.
func (o *Options) ReplacesHeader(name string) bool {
	if o == nil || len(o.RequestHeaders) == 0 {
		return false
	}
	for k := range o.RequestHeaders {
		if strings.HasPrefix(k, "+") {
			continue
		}
		k = strings.TrimPrefix(k, "-")
		if http.CanonicalHeaderKey(k) == name {
			return true
		}
	}
	return false
}

// ReplacesParam reports whether request_params replaces ("name") or removes
// ("-name") the named query/form parameter upstream; "+name" appends.
func (o *Options) ReplacesParam(name string) bool {
	if o == nil || len(o.RequestParams) == 0 {
		return false
	}
	if _, ok := o.RequestParams[name]; ok {
		return true
	}
	_, ok := o.RequestParams["-"+name]
	return ok
}

// IdentityKeyPart returns a collision-free digest of the configured
// request_headers/request_params for inclusion in derived cache keys.
func (o *Options) IdentityKeyPart() string {
	if o == nil {
		return ""
	}
	if o.identityKeyPart != "" ||
		(len(o.RequestHeaders) == 0 && len(o.RequestParams) == 0) {
		return o.identityKeyPart
	}
	return o.computeIdentityKeyPart()
}

// RefreshIdentityKeyPart recomputes the precomputed identity digest after a
// programmatic change to RequestHeaders or RequestParams
func (o *Options) RefreshIdentityKeyPart() {
	o.identityKeyPart = o.computeIdentityKeyPart()
}

func (o *Options) computeIdentityKeyPart() string {
	if len(o.RequestHeaders) == 0 && len(o.RequestParams) == 0 {
		return ""
	}
	h := sha256.New()
	var sizes [binary.MaxVarintLen64]byte
	writeStr := func(s string) {
		n := binary.PutUvarint(sizes[:], uint64(len(s)))
		h.Write(sizes[:n])
		h.Write([]byte(s))
	}
	writeMap := func(class byte, m map[string]string) {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			h.Write([]byte{class})
			writeStr(k)
			writeStr(m[k])
		}
	}
	writeMap('h', o.RequestHeaders)
	writeMap('p', o.RequestParams)
	return hex.EncodeToString(h.Sum(nil))
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

// isRegexPath returns true if the path pattern indicates a regex path via the
// auto-detection rule: a leading ^/ or the escaped ^\/ form
func isRegexPath(path string) bool {
	return strings.HasPrefix(path, "^/") || strings.HasPrefix(path, `^\/`)
}

func (o *Options) Validate() (bool, error) {
	normalized := matching.PathMatchName(strings.ToLower(string(o.MatchTypeName)))
	if _, ok := matching.Names[normalized]; !ok && o.MatchTypeName != "" &&
		!isRegexPath(o.Path) {
		return false, fmt.Errorf("invalid match_type: %s", o.MatchTypeName)
	}
	if o.MatchType == matching.PathMatchTypeRegex ||
		normalized == matching.PathMatchNameRegex || isRegexPath(o.Path) {
		if o.Regexp == nil {
			re, err := regexp.Compile(o.Path)
			if err != nil {
				return false, fmt.Errorf("invalid regex path %q: %w", o.Path, err)
			}
			o.Regexp = re
		}
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
	if o.CORS != nil {
		if _, err := o.CORS.Validate(); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Validate validates each path Options in the List; name is the name of the
// backend the List belongs to, used to provide context in errors
func (l List) Validate(name string) error {
	for _, o := range l {
		if o == nil {
			continue
		}
		_, err := o.Validate()
		if err != nil {
			return fmt.Errorf("backend %q: %w", name, err)
		}
	}
	return nil
}

// RegexShadowedByCatchAll returns true if the List contains one or more regex
// paths alongside a catch-all classic prefix path ("/"), which prefix-matches
// every request and therefore precludes the regex tier (evaluated only after
// exact and prefix both miss) from ever being reached
func (l List) RegexShadowedByCatchAll() bool {
	var hasRegex, hasCatchAll bool
	for _, o := range l {
		if o == nil {
			continue
		}
		switch o.MatchType {
		case matching.PathMatchTypeRegex:
			hasRegex = true
		case matching.PathMatchTypePrefix:
			if o.Path == "/" {
				hasCatchAll = true
			}
		}
	}
	return hasRegex && hasCatchAll
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

// Match returns the path Options that the lm router would select for the
// provided request method and path, mirroring router precedence exactly:
// exact match first, then longest prefix, then regex (longest pattern string
// first, ties broken by config order, first match wins). Within a tier, a
// matched path whose Options do not permit the method returns nil (where the
// router would respond 405 Method Not Allowed) rather than falling through to
// the next tier. HEAD implicitly matches a GET entry unless HEAD is
// explicitly configured for the same path. Returns nil when no path matches.
func (l List) Match(method, path string) *Options {
	method = strings.ToUpper(method)
	// exact tier
	if candidates := l.withPath(matching.PathMatchTypeExact, path); len(candidates) > 0 {
		return matchMethod(candidates, method)
	}
	// prefix tier: the longest matching prefix wins regardless of method
	var longest string
	var matched bool
	for _, o := range l {
		if o == nil || o.MatchType != matching.PathMatchTypePrefix {
			continue
		}
		if strings.HasPrefix(path, o.Path) && (!matched || len(o.Path) > len(longest)) {
			longest = o.Path
			matched = true
		}
	}
	if matched {
		return matchMethod(l.withPath(matching.PathMatchTypePrefix, longest), method)
	}
	// regex tier: longest pattern first, config order breaks ties,
	// first match wins
	regexes := make(List, 0, len(l))
	for _, o := range l {
		if o != nil && o.MatchType == matching.PathMatchTypeRegex && o.Regexp != nil {
			regexes = append(regexes, o)
		}
	}
	slices.SortStableFunc(regexes, func(a, b *Options) int {
		return len(b.Path) - len(a.Path)
	})
	for _, o := range regexes {
		if o.Regexp.MatchString(path) {
			return matchMethod(l.withPath(matching.PathMatchTypeRegex, o.Path), method)
		}
	}
	return nil
}

// withPath returns the members of the List having the provided MatchType and
// exact Path string, in config order
func (l List) withPath(t matching.PathMatchType, path string) List {
	out := make(List, 0, len(l))
	for _, o := range l {
		if o != nil && o.MatchType == t && o.Path == path {
			out = append(out, o)
		}
	}
	return out
}

// matchMethod returns the first candidate permitting the method; a HEAD
// request additionally matches a GET entry when no candidate explicitly
// permits HEAD, mirroring the router's implicit HEAD-for-GET registration
func matchMethod(candidates List, method string) *Options {
	for _, o := range candidates {
		if slices.ContainsFunc(o.Methods, func(m string) bool {
			return strings.EqualFold(m, method)
		}) {
			return o
		}
	}
	if method == http.MethodHead {
		for _, o := range candidates {
			if slices.ContainsFunc(o.Methods, func(m string) bool {
				return strings.EqualFold(m, http.MethodGet)
			}) {
				return o
			}
		}
	}
	return nil
}

func (o *Options) UnmarshalYAML(value *yaml.Node) error {
	type loadOptions Options
	lo := loadOptions(*New())
	if err := value.Decode(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
