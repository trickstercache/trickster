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

// Package ir is the controller's translation intermediate representation: the desired data
// plane, independent of both the Kubernetes types and the configuration that realizes it
package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// Path match types, mirroring the Gateway API and Ingress vocabularies
const (
	// PathExact matches the request path in full
	PathExact = "exact"
	// PathPrefix matches whole leading path segments
	PathPrefix = "prefix"
	// PathRegex matches an anchored regular expression
	PathRegex = "regex"
)

// Listener protocols
const (
	ProtocolHTTP  = "http"
	ProtocolHTTPS = "https"
	// ProtocolGRPC marks a route carrying gRPC, whose responses end in trailers
	ProtocolGRPC = "grpc"
	// ProtocolTCP relays each connection unread to one route's backends
	ProtocolTCP = "tcp"
	// ProtocolTLS relays TLS connections unterminated, choosing the route by the server name offered
	ProtocolTLS = "tls"
	// ProtocolUDP relays datagrams to one route's backends, one session per client
	ProtocolUDP = "udp"
)

// IsStream reports whether a protocol relays bytes without reading them: tcp, tls or udp
func IsStream(protocol string) bool {
	return protocol == ProtocolTCP || protocol == ProtocolTLS || protocol == ProtocolUDP
}

// Path handlers a Policy may select, spelled as the generated path configuration carries them so
// translators and the compiler agree
const (
	// HandlerProxy proxies the request without consulting the object cache
	HandlerProxy = "proxy"
	// HandlerProxyCache serves the request from the object cache
	HandlerProxyCache = "proxycache"
)

// ConfiguredNames is what the running configuration defines that a route may name, checked while
// translating so one object's mistake does not fail the whole generated configuration
type ConfiguredNames struct {
	Caches         sets.Set[string]
	NegativeCaches sets.Set[string]
	Tracers        sets.Set[string]
	Rewriters      sets.Set[string]
	Authenticators sets.Set[string]
}

// Event reasons a Problem may carry; a Problem naming none is reported as Rejected
const (
	ReasonRejected           = "Rejected"
	ReasonInvalidAnnotation  = "InvalidAnnotation"
	ReasonInvalidCertificate = "InvalidCertificate"
	ReasonInvalidParameters  = "InvalidParameters"
)

// Problem is one thing a translator could not do, for logging now and for
// the Events and status conditions the controller publishes
type Problem struct {
	// Source is the object the problem is about
	Source Source
	// Detail explains what was dropped and why
	Detail string
	// Reason classifies the problem for the Event it becomes; empty is Rejected
	Reason string
}

// EventReason returns the problem's Event reason, Rejected when it names none
func (p Problem) EventReason() string {
	if p.Reason == "" {
		return ReasonRejected
	}
	return p.Reason
}

// String renders the problem for a log line
func (p Problem) String() string {
	return p.Source.Key() + ": " + p.Detail
}

// Object kinds a Source names; KindController marks a piece of the IR the controller
// synthesized rather than translated, such as an Ingress's listeners
const (
	KindGateway      = "Gateway"
	KindGatewayClass = "GatewayClass"
	KindHTTPRoute    = "HTTPRoute"
	KindGRPCRoute    = "GRPCRoute"
	KindTCPRoute     = "TCPRoute"
	KindTLSRoute     = "TLSRoute"
	KindUDPRoute     = "UDPRoute"
	KindIngress      = "Ingress"
	KindIngressClass = "IngressClass"
	KindController   = "Controller"
	// KindBackendTLSPolicy is a policy attached to a Service rather than a
	// route; problems with one are reported against the policy itself
	KindBackendTLSPolicy = "BackendTLSPolicy"
	// KindCachePolicy is the caching policy attached to a Gateway, route or Service; problems
	// with one are reported against the policy itself
	KindCachePolicy = "TricksterCachePolicy"
)

// Result header dispositions a Policy may select: whether the X-Trickster-Result response header
// reaches the client; empty exposes it
const (
	ResultHeaderExpose = "expose"
	ResultHeaderHide   = "hide"
)

// Source identifies the Kubernetes object a piece of the IR came from. It
// is what status is written back to and what generated names derive from.
type Source struct {
	// Kind is the object kind (Gateway, HTTPRoute, Ingress)
	Kind string `json:"kind"`
	// Namespace and Name identify the object
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Generation is the object generation the IR was built from, so status
	// writeback can report the generation it observed
	Generation int64 `json:"generation,omitempty"`
	// UID is the object's unique identifier, so an Event can be attached to exactly this object
	UID string `json:"uid,omitempty"`
}

// Key returns the source's stable namespace/name identity, excluding the
// generation, so it does not change as an object is edited
func (s Source) Key() string {
	return s.Kind + "/" + s.Namespace + "/" + s.Name
}

func (s Source) compare(o Source) int {
	if c := strings.Compare(s.Kind, o.Kind); c != 0 {
		return c
	}
	if c := strings.Compare(s.Namespace, o.Namespace); c != 0 {
		return c
	}
	return strings.Compare(s.Name, o.Name)
}

// IR is the whole desired data plane
type IR struct {
	// Listeners are the ports and protocols served
	Listeners []Listener `json:"listeners,omitempty"`
	// Routes map requests onto backend groups
	Routes []Route `json:"routes,omitempty"`
	// Backends are the weighted target groups routes dispatch to
	Backends []BackendGroup `json:"backends,omitempty"`
	// Certs are the TLS certificates listeners serve, by Secret reference
	Certs []CertRef `json:"certs,omitempty"`
	// Policies are the per-route overrides (caching, timeouts) attached by
	// annotation or CRD
	Policies []Policy `json:"policies,omitempty"`
}

// Listener is one served port
type Listener struct {
	// Name is the listener's stable identity within the IR
	Name string `json:"name"`
	// Section is the Gateway's own name for the listener, which its status entry is keyed by
	Section string `json:"section,omitempty"`
	// External marks a listener the controller attaches to rather than mints: the operator's own,
	// which an Ingress is served on, named as the rest of the configuration knows it
	External bool `json:"external,omitempty"`
	// Port is the TCP port served; unset for an external listener
	Port int `json:"port,omitempty"`
	// Protocol is http, https, tcp, tls or udp; unset for an external listener
	Protocol string `json:"protocol,omitempty"`
	// Hostname optionally restricts the listener to one hostname, which may
	// carry a single leading wildcard label
	Hostname string `json:"hostname,omitempty"`
	// CertRefs names the CertRef entries this listener serves; empty for
	// a plaintext listener
	CertRefs []string `json:"cert_refs,omitempty"`
	// Source is the Gateway that declared it
	Source Source `json:"source"`
}

// Route maps matching requests onto backend groups
type Route struct {
	// Name is the route's stable identity within the IR
	Name string `json:"name"`
	// Source is the HTTPRoute or Ingress that declared it
	Source Source `json:"source"`
	// Hostnames the route answers for; empty answers any hostname the
	// listener admits
	Hostnames []string `json:"hostnames,omitempty"`
	// Listeners names the Listener entries this route is attached to; a route naming none compiles
	// to nothing rather than landing on the default frontend
	Listeners []string `json:"listeners,omitempty"`
	// Rank orders routes whose matches meet at one slot, lower first; translators assign it from
	// object age, oldest first, so every replica reaches the same order
	Rank int `json:"rank,omitempty"`
	// Protocol is grpc for a route whose backends speak gRPC, or tcp, tls or udp for one that
	// relays its listener's bytes unread; empty is plain HTTP
	Protocol string `json:"protocol,omitempty"`
	// Rules are evaluated in the order the source declared them
	Rules []Rule `json:"rules,omitempty"`
}

// Rule is one match-to-backend mapping within a route
type Rule struct {
	// Matches are alternatives: any one matching selects this rule
	Matches []Match `json:"matches,omitempty"`
	// BackendGroup names the BackendGroup this rule dispatches to
	BackendGroup string `json:"backend_group,omitempty"`
	// Policy optionally names a Policy entry overriding the defaults
	Policy string `json:"policy,omitempty"`
	// Filters are applied to every request the rule matches, in order,
	// before any member's own
	Filters []Filter `json:"filters,omitempty"`
	// Timeouts bounds the upstream exchange for requests the rule matches
	Timeouts *RuleTimeouts `json:"timeouts,omitempty"`
	// Retry repeats failed idempotent upstream requests the rule matches
	Retry *RuleRetry `json:"retry,omitempty"`
}

// RuleTimeouts bounds a rule's upstream exchange, in milliseconds; zero is unbounded
type RuleTimeouts struct {
	// RequestMS bounds the whole exchange, retries included
	RequestMS int64 `json:"request_ms,omitempty"`
	// BackendRequestMS bounds each attempt
	BackendRequestMS int64 `json:"backend_request_ms,omitempty"`
}

// Clone returns a copy of the timeouts, nil for nil
func (t *RuleTimeouts) Clone() *RuleTimeouts {
	if t == nil {
		return nil
	}
	out := *t
	return &out
}

// RuleRetry is a rule's retry policy for idempotent requests
type RuleRetry struct {
	// Codes are the upstream status codes retried; a failed connection is always retried
	Codes []int `json:"codes,omitempty"`
	// Attempts is the number of retries after the first attempt
	Attempts int `json:"attempts,omitempty"`
	// BackoffMS is the wait before each retry
	BackoffMS int64 `json:"backoff_ms,omitempty"`
}

// Clone returns a deep copy of the retry policy, nil for nil
func (r *RuleRetry) Clone() *RuleRetry {
	if r == nil {
		return nil
	}
	out := *r
	out.Codes = slices.Clone(r.Codes)
	return &out
}

// Redirect returns the rule's redirect filter, or nil when it forwards; a redirecting rule
// dispatches to no backend, so its group may be empty and its members are not emitted
func (r Rule) Redirect() *RedirectFilter {
	for i := range r.Filters {
		if r.Filters[i].Type == FilterRedirect && r.Filters[i].Redirect != nil {
			return r.Filters[i].Redirect
		}
	}
	return nil
}

// Filter types
const (
	// FilterRequestHeaders sets, adds and removes request headers
	FilterRequestHeaders = "request_headers"
	// FilterResponseHeaders sets, adds and removes response headers
	FilterResponseHeaders = "response_headers"
	// FilterRedirect answers with a redirection instead of forwarding
	FilterRedirect = "redirect"
	// FilterURLRewrite changes the upstream Host header and path
	FilterURLRewrite = "url_rewrite"
	// FilterMirror copies a share of requests to another Service, whose responses are discarded
	FilterMirror = "mirror"
)

// Filter is one transformation of a matched request or its response. Exactly
// the field its Type names is set.
type Filter struct {
	Type            string            `json:"type"`
	RequestHeaders  *HeaderFilter     `json:"request_headers,omitempty"`
	ResponseHeaders *HeaderFilter     `json:"response_headers,omitempty"`
	Redirect        *RedirectFilter   `json:"redirect,omitempty"`
	URLRewrite      *URLRewriteFilter `json:"url_rewrite,omitempty"`
	Mirror          *MirrorFilter     `json:"mirror,omitempty"`
}

// MirrorFilter names the Service that receives copies of matched requests
type MirrorFilter struct {
	Service ServiceTarget `json:"service"`
	// TLS is the upstream TLS the Service's BackendTLSPolicy asks for, or nil
	TLS *BackendTLS `json:"tls,omitempty"`
	// Percent is the share of requests copied, 1-100
	Percent int `json:"percent,omitempty"`
}

// Clone returns a deep copy of the mirror filter, nil for nil
func (m *MirrorFilter) Clone() *MirrorFilter {
	if m == nil {
		return nil
	}
	out := *m
	out.TLS = m.TLS.Clone()
	return &out
}

// HeaderFilter modifies headers: Set replaces, Add appends and Remove
// deletes, applied in that order
type HeaderFilter struct {
	Set    []Header `json:"set,omitempty"`
	Add    []Header `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// Header is one header name and value
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Path modifier types
const (
	// PathReplaceFull replaces the whole request path with Value
	PathReplaceFull = "full"
	// PathReplacePrefix replaces the matched path prefix with Value, keeping
	// the rest of the path
	PathReplacePrefix = "prefix"
)

// PathModifier rewrites the request path
type PathModifier struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// RedirectFilter describes the Location a redirecting rule answers with;
// an unset field takes the request's own value
type RedirectFilter struct {
	Scheme   string        `json:"scheme,omitempty"`
	Hostname string        `json:"hostname,omitempty"`
	Port     int           `json:"port,omitempty"`
	Path     *PathModifier `json:"path,omitempty"`
	// StatusCode is the redirection status, 302 when unset
	StatusCode int `json:"status_code,omitempty"`
}

// URLRewriteFilter changes what the upstream sees: Hostname replaces the
// Host header and Path the request path
type URLRewriteFilter struct {
	Hostname string        `json:"hostname,omitempty"`
	Path     *PathModifier `json:"path,omitempty"`
}

// Match is one request-matching predicate set; every populated field must
// match for the Match to select its rule
type Match struct {
	// Path is the path predicate
	Path PathMatch `json:"path"`
	// Methods restricts the match to these HTTP methods; nil matches them all
	Methods []string `json:"methods,omitempty"`
	// MethodSpecific reports that the source named a method, which ranks the match ahead of one
	// that did not; a subset awarded after a conflict confers no such rank
	MethodSpecific bool `json:"method_specific,omitempty"`
	// Headers are required request headers
	Headers []KeyValueMatch `json:"headers,omitempty"`
	// QueryParams are required query parameters
	QueryParams []KeyValueMatch `json:"query_params,omitempty"`
}

// PathMatch is a request path predicate
type PathMatch struct {
	// Type is exact, prefix, or regex
	Type string `json:"type"`
	// Value is the path or pattern; a prefix is normalized by NormalizePrefix
	Value string `json:"value"`
}

// NormalizePrefix returns the one spelling of an element-wise path prefix: both APIs read a
// trailing slash as nothing, so /foo/ and /foo are one rule
func NormalizePrefix(path string) string {
	if trimmed := strings.TrimSuffix(path, "/"); trimmed != "" {
		return trimmed
	}
	return "/"
}

// CatchAllRegex matches every path and loses to every other route: the regex tier is tried
// last, longest first, so the shortest catch-all on no host is the last thing tried
const CatchAllRegex = "^/"

// KeyValueMatch is a header or query parameter predicate
type KeyValueMatch struct {
	Name string `json:"name"`
	// Value is compared exactly unless Regex is set
	Value string `json:"value"`
	// Regex compares Value as an anchored regular expression
	Regex bool `json:"regex,omitempty"`
}

// BackendGroup is a weighted set of targets a rule dispatches across
type BackendGroup struct {
	// Name is the group's stable identity within the IR
	Name string `json:"name"`
	// Source is the route that declared it, for naming and status
	Source Source `json:"source"`
	// RuleIndex is the source rule's position, part of generated names
	RuleIndex int `json:"rule_index"`
	// Members are the weighted targets
	Members []BackendMember `json:"members,omitempty"`
}

// BackendMember is one weighted target within a group
type BackendMember struct {
	// RefIndex is the member's position in the source's backendRefs, part
	// of generated names
	RefIndex int `json:"ref_index"`
	// Service is the target Service; zero when Invalid
	Service ServiceTarget `json:"service"`
	// Weight is the relative share of traffic, at least 1 for a valid
	// member; a zero-weight backendRef is dropped by the translator
	Weight int `json:"weight"`
	// Invalid marks a backendRef that could not be resolved; the compiler realizes it as a fixed
	// error response holding its share of the traffic, as the Gateway API requires
	Invalid bool `json:"invalid,omitempty"`
	// InvalidReason explains Invalid, for status and Events
	InvalidReason string `json:"invalid_reason,omitempty"`
	// Filters are applied to requests this member receives, after the
	// rule's own
	Filters []Filter `json:"filters,omitempty"`
	// TLS, when set, makes the upstream connection TLS as a
	// BackendTLSPolicy on the Service requires
	TLS *BackendTLS `json:"tls,omitempty"`
	// Policy optionally names a Policy entry overlaid on the rule's for this member alone, as a
	// cache policy attached to the member's Service is
	Policy string `json:"policy,omitempty"`
}

// BackendTLS is how an upstream's certificate is verified. Exactly one of
// CACertificates and System is set.
type BackendTLS struct {
	// Hostname is sent as SNI and verified against the certificate
	Hostname string `json:"hostname"`
	// CACertificates are PEM bundles trusted to sign the certificate; they travel in the IR so a
	// rotated bundle changes the hash, and a CA certificate is public material
	CACertificates []string `json:"ca_certificates,omitempty"`
	// System trusts the well-known system certificate authorities instead
	System bool `json:"system,omitempty"`
}

// ServiceTarget is a resolved Kubernetes Service port
type ServiceTarget struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Port is the Service port number
	Port int32 `json:"port"`
	// PortName is the Service port's name when it has one; the endpoint
	// routing mode selects by name where possible
	PortName string `json:"port_name,omitempty"`
	// H2C is set when the port's appProtocol says the Service serves cleartext HTTP/2
	H2C bool `json:"h2c,omitempty"`
	// TargetPortName is a headless Service port's target named rather than numbered, which only
	// its pods resolve; Port is then the Service port, usable only where the pods are discovered
	TargetPortName string `json:"target_port_name,omitempty"`
	// Scheme is http or https for the upstream connection
	Scheme string `json:"scheme,omitempty"`
}

// CertRef is a TLS certificate held in a Kubernetes Secret
type CertRef struct {
	// Name is the ref's stable identity within the IR
	Name string `json:"name"`
	// Namespace and SecretName locate the kubernetes.io/tls Secret
	Namespace  string `json:"namespace"`
	SecretName string `json:"secret_name"`
	// Source is the Gateway or Ingress that referenced it
	Source Source `json:"source"`
}

// Key returns the namespace/name identity the certificate store is keyed by
func (c CertRef) Key() string {
	return c.Namespace + "/" + c.SecretName
}

// Policy is a per-route override of the generated-backend defaults, from annotations or class
// parameters; unset fields fall back to the configured defaults
type Policy struct {
	// Name is the policy's stable identity within the IR
	Name string `json:"name"`
	// Source is the object that carried the annotation or CRD
	Source Source `json:"source"`
	// Handler overrides the generated backend's path handler (proxy,
	// proxycache); empty takes the default
	Handler string `json:"handler,omitempty"`
	// CacheName overrides which configured cache is used
	CacheName string `json:"cache_name,omitempty"`
	// TimeoutMS overrides the upstream timeout; 0 takes the default
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
	// RoutingMode overrides the default routing mode for this route
	RoutingMode string `json:"routing_mode,omitempty"`
	// MaxTTLMS caps how long a cached object is served before revalidation;
	// 0 takes the backend default
	MaxTTLMS int64 `json:"max_ttl_ms,omitempty"`
	// NegativeCacheName overrides which configured negative cache is used
	NegativeCacheName string `json:"negative_cache_name,omitempty"`
	// RequestHeaders and ResponseHeaders are header updates applied to the route's requests and
	// responses; a name prefixed with '-' deletes the header and one prefixed with '+' appends
	RequestHeaders  map[string]string `json:"request_headers,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	// CORSMode is preserve, merge, replace or disable
	CORSMode string `json:"cors_mode,omitempty"`
	// CORSHeaders are the CORS response headers merge and replace apply
	CORSHeaders map[string]string `json:"cors_headers,omitempty"`
	// CollapsedForwarding is basic or progressive
	CollapsedForwarding string `json:"collapsed_forwarding,omitempty"`
	// RewriteTarget replaces the matched portion of the request path on the
	// way upstream. A regex match may reference its captures as ${1}.
	RewriteTarget string `json:"rewrite_target,omitempty"`
	// TracingName, ReqRewriterName and AuthenticatorName name configured objects every backend
	// under the policy uses; only the configuration or a class's parameters set them
	TracingName       string `json:"tracing_name,omitempty"`
	ReqRewriterName   string `json:"req_rewriter_name,omitempty"`
	AuthenticatorName string `json:"authenticator_name,omitempty"`
	// HealthMode is the health mode of generated discovery-backed ALBs
	HealthMode string `json:"health_mode,omitempty"`
	// LoadBalancing is the mechanism that spreads traffic across a Service's endpoints in the
	// endpoint routing mode; empty is round robin. The weights between a rule's backendRefs
	// are always apportioned by round robin, whatever this says.
	LoadBalancing string `json:"load_balancing,omitempty"`
	// LoadBalancingKey is what the hrw mechanism keeps together, such as client_ip
	LoadBalancingKey string `json:"load_balancing_key,omitempty"`
	// Provider makes the generated backend a time series provider (prometheus, influxdb, ...)
	// whose own API paths it then accelerates; only a cache policy sets it
	Provider string `json:"provider,omitempty"`
	// CacheKeyParams and CacheKeyHeaders are the request query parameters and headers hashed
	// into the cache key of every path the policy governs; nil inherits and an empty list clears
	CacheKeyParams  []string `json:"cache_key_params"`
	CacheKeyHeaders []string `json:"cache_key_headers"`
	// ResultHeader is expose or hide: whether the X-Trickster-Result response header reaches
	// the client; empty exposes it
	ResultHeader string `json:"result_header,omitempty"`
}

// Overlay returns the policy with every field the other sets written over it: header updates
// fold in that order, one operation per header whatever its spelling; a list the other sets,
// even empty, replaces; Name and Source are kept
func (p Policy) Overlay(o *Policy) Policy {
	out := p.Clone()
	if o == nil {
		return out
	}
	overlayString(&out.Handler, o.Handler)
	overlayString(&out.CacheName, o.CacheName)
	overlayString(&out.RoutingMode, o.RoutingMode)
	overlayString(&out.NegativeCacheName, o.NegativeCacheName)
	overlayString(&out.CORSMode, o.CORSMode)
	overlayString(&out.CollapsedForwarding, o.CollapsedForwarding)
	overlayString(&out.RewriteTarget, o.RewriteTarget)
	overlayString(&out.TracingName, o.TracingName)
	overlayString(&out.ReqRewriterName, o.ReqRewriterName)
	overlayString(&out.AuthenticatorName, o.AuthenticatorName)
	overlayString(&out.HealthMode, o.HealthMode)
	overlayString(&out.LoadBalancing, o.LoadBalancing)
	overlayString(&out.LoadBalancingKey, o.LoadBalancingKey)
	overlayString(&out.Provider, o.Provider)
	overlayString(&out.ResultHeader, o.ResultHeader)
	if o.TimeoutMS > 0 {
		out.TimeoutMS = o.TimeoutMS
	}
	if o.MaxTTLMS > 0 {
		out.MaxTTLMS = o.MaxTTLMS
	}
	out.RequestHeaders = overlayHeaders(out.RequestHeaders, o.RequestHeaders)
	out.ResponseHeaders = overlayHeaders(out.ResponseHeaders, o.ResponseHeaders)
	out.CORSHeaders = overlayHeaders(out.CORSHeaders, o.CORSHeaders)
	if o.CacheKeyParams != nil {
		out.CacheKeyParams = slices.Clone(o.CacheKeyParams)
	}
	if o.CacheKeyHeaders != nil {
		out.CacheKeyHeaders = slices.Clone(o.CacheKeyHeaders)
	}
	return out
}

func overlayString(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func overlayHeaders(base, over map[string]string) map[string]string {
	// a header is one header however it is spelled and whichever operator names it, so the
	// two maps fold into one operation per header with the more specific map's the last word
	if len(over) == 0 {
		return base
	}
	var u headers.Updates
	u.Merge(base)
	u.Merge(over)
	return u.Render()
}

// Canonical returns a deep copy in a deterministic order, so two IRs describing the same data
// plane hash equal; rule order is the source's precedence and is kept
func (i *IR) Canonical() *IR {
	if i == nil {
		return nil
	}
	out := &IR{
		Listeners: slices.Clone(i.Listeners),
		Routes:    slices.Clone(i.Routes),
		Backends:  slices.Clone(i.Backends),
		Certs:     slices.Clone(i.Certs),
		Policies:  slices.Clone(i.Policies),
	}
	slices.SortStableFunc(out.Listeners, func(a, b Listener) int {
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortStableFunc(out.Routes, func(a, b Route) int {
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortStableFunc(out.Backends, func(a, b BackendGroup) int {
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortStableFunc(out.Certs, func(a, b CertRef) int {
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortStableFunc(out.Policies, func(a, b Policy) int {
		return strings.Compare(a.Name, b.Name)
	})
	for li := range out.Listeners {
		out.Listeners[li].CertRefs = sortedClone(out.Listeners[li].CertRefs)
	}
	for ri := range out.Routes {
		r := &out.Routes[ri]
		r.Hostnames = sortedClone(r.Hostnames)
		r.Listeners = sortedClone(r.Listeners)
		r.Rules = slices.Clone(r.Rules)
		for ki := range r.Rules {
			rule := &r.Rules[ki]
			rule.Matches = slices.Clone(rule.Matches)
			for mi := range rule.Matches {
				m := &rule.Matches[mi]
				m.Methods = sortedClone(m.Methods)
				m.Headers = sortedKV(m.Headers)
				m.QueryParams = sortedKV(m.QueryParams)
			}
			rule.Filters = CloneFilters(rule.Filters)
			rule.Timeouts = rule.Timeouts.Clone()
			rule.Retry = rule.Retry.Clone()
		}
	}
	for pi := range out.Policies {
		out.Policies[pi] = out.Policies[pi].Clone()
	}
	for bi := range out.Backends {
		g := &out.Backends[bi]
		g.Members = slices.Clone(g.Members)
		slices.SortStableFunc(g.Members, func(a, b BackendMember) int {
			return a.RefIndex - b.RefIndex
		})
		for mi := range g.Members {
			m := &g.Members[mi]
			m.Filters = CloneFilters(m.Filters)
			m.TLS = m.TLS.Clone()
		}
	}
	return out
}

// CloneFilters deep-copies a filter list; order is the source's and is kept
func CloneFilters(in []Filter) []Filter {
	if len(in) == 0 {
		return nil
	}
	out := make([]Filter, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}

// Clone returns a deep copy of the filter
func (f Filter) Clone() Filter {
	f.RequestHeaders = f.RequestHeaders.Clone()
	f.ResponseHeaders = f.ResponseHeaders.Clone()
	f.Redirect = f.Redirect.Clone()
	f.URLRewrite = f.URLRewrite.Clone()
	f.Mirror = f.Mirror.Clone()
	return f
}

// Clone returns a deep copy of the header filter, nil for nil
func (h *HeaderFilter) Clone() *HeaderFilter {
	if h == nil {
		return nil
	}
	return &HeaderFilter{
		Set: slices.Clone(h.Set), Add: slices.Clone(h.Add),
		Remove: slices.Clone(h.Remove),
	}
}

// Clone returns a copy of the path modifier, nil for nil
func (p *PathModifier) Clone() *PathModifier {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

// Clone returns a deep copy of the redirect filter, nil for nil
func (r *RedirectFilter) Clone() *RedirectFilter {
	if r == nil {
		return nil
	}
	c := *r
	c.Path = r.Path.Clone()
	return &c
}

// Clone returns a deep copy of the URL rewrite filter, nil for nil
func (u *URLRewriteFilter) Clone() *URLRewriteFilter {
	if u == nil {
		return nil
	}
	c := *u
	c.Path = u.Path.Clone()
	return &c
}

// Clone returns a deep copy of the policy
func (p Policy) Clone() Policy {
	p.RequestHeaders = maps.Clone(p.RequestHeaders)
	p.ResponseHeaders = maps.Clone(p.ResponseHeaders)
	p.CORSHeaders = maps.Clone(p.CORSHeaders)
	p.CacheKeyParams = slices.Clone(p.CacheKeyParams)
	p.CacheKeyHeaders = slices.Clone(p.CacheKeyHeaders)
	return p
}

// Clone returns a deep copy of the backend TLS settings, nil for nil
func (t *BackendTLS) Clone() *BackendTLS {
	if t == nil {
		return nil
	}
	c := *t
	c.CACertificates = slices.Clone(t.CACertificates)
	return &c
}

func sortedClone(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func sortedKV(s []KeyValueMatch) []KeyValueMatch {
	if len(s) == 0 {
		return nil
	}
	out := slices.Clone(s)
	slices.SortStableFunc(out, func(a, b KeyValueMatch) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Value, b.Value)
	})
	return out
}

// Hash is the content hash of the canonical IR, covering everything the compiler reads and
// excluding Source generations and UIDs, which change without altering configuration
func (i *IR) Hash() string {
	c := i.Canonical()
	if c == nil {
		c = &IR{}
	}
	c.stripVolatile()
	b, err := json.Marshal(c)
	if err != nil {
		// the IR is plain data with no unmarshalable types, so this cannot
		// happen; a distinct value beats a panic in the reconcile loop
		return "unhashable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (i *IR) stripVolatile() {
	for x := range i.Listeners {
		i.Listeners[x].Source = i.Listeners[x].Source.stable()
	}
	for x := range i.Routes {
		i.Routes[x].Source = i.Routes[x].Source.stable()
	}
	for x := range i.Backends {
		i.Backends[x].Source = i.Backends[x].Source.stable()
	}
	for x := range i.Certs {
		i.Certs[x].Source = i.Certs[x].Source.stable()
	}
	for x := range i.Policies {
		i.Policies[x].Source = i.Policies[x].Source.stable()
	}
}

func (s Source) stable() Source {
	s.Generation = 0
	s.UID = ""
	return s
}

// Merge returns one IR holding everything in both; translators name objects by kind,
// namespace and name, so two translators' output never collides
func Merge(a, b *IR) *IR {
	if a == nil {
		a = &IR{}
	}
	if b == nil {
		return a
	}
	return &IR{
		Listeners: append(slices.Clone(a.Listeners), b.Listeners...),
		Routes:    append(slices.Clone(a.Routes), b.Routes...),
		Backends:  append(slices.Clone(a.Backends), b.Backends...),
		Certs:     append(slices.Clone(a.Certs), b.Certs...),
		Policies:  append(slices.Clone(a.Policies), b.Policies...),
	}
}

// IsEmpty reports whether the IR describes no data plane at all
func (i *IR) IsEmpty() bool {
	return i == nil || (len(i.Listeners) == 0 && len(i.Routes) == 0 &&
		len(i.Backends) == 0 && len(i.Certs) == 0 && len(i.Policies) == 0)
}

// Sources returns every distinct object the IR was built from, in a
// deterministic order, for status writeback
func (i *IR) Sources() []Source {
	if i == nil {
		return nil
	}
	seen := make(map[string]Source)
	add := func(s Source) {
		if _, ok := seen[s.Key()]; !ok {
			seen[s.Key()] = s
		}
	}
	for _, l := range i.Listeners {
		add(l.Source)
	}
	for _, r := range i.Routes {
		add(r.Source)
	}
	for _, c := range i.Certs {
		add(c.Source)
	}
	out := make([]Source, 0, len(seen))
	for _, s := range seen {
		out = append(out, s)
	}
	slices.SortFunc(out, Source.compare)
	return out
}
