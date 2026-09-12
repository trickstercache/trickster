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

// Package compile turns the translation IR into a configuration overlay, emitted as YAML so it
// travels the same load, validate and apply path as a file, under the kgw-- prefix
package compile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	dproviders "github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	kubeopts "github.com/trickstercache/trickster/v2/pkg/kube/options"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"

	"go.yaml.in/yaml/v3"
)

var (
	// ErrNoOptions indicates Compile was called without a kubernetes config
	ErrNoOptions = errors.New("no kubernetes options provided")
	// ErrUnsupportedRoutingMode indicates a routing mode this build cannot
	// compile yet
	ErrUnsupportedRoutingMode = errors.New("unsupported routing mode")
)

// handler names for generated paths
const (
	handlerProxy      = ir.HandlerProxy
	handlerProxyCache = ir.HandlerProxyCache
	// handlerLocalResponse serves a fixed response without an upstream; it
	// is how an unresolvable backendRef holds its share of the traffic
	handlerLocalResponse = "localresponse"
	// handlerRedirect answers with a redirection to the request URL as the
	// path's rewriter left it; it is how a redirecting rule is served
	handlerRedirect = "redirect"
)

// clusterDomain is the in-cluster DNS suffix a Service address is built on
const clusterDomain = "svc"

// unresolvedOriginURL is the origin an unresolvable backendRef's backend carries to validate;
// it is never dialed, since the backend answers from a localresponse path
const unresolvedOriginURL = "https://unresolved.kgw.invalid"

// unmatchedOriginURL is the origin the 404 responder carries, for the same
// reason and with the same guarantee
const unmatchedOriginURL = "https://unmatched.kgw.invalid"

// redirectOriginURL is the origin a redirecting rule's backend carries, for
// the same reason: every path answers from the redirect handler
const redirectOriginURL = "https://redirect.kgw.invalid"

// Compile turns the IR into an overlay ready for config.LoadWithOverlay; its Version hashes the
// emitted bytes rather than the IR, since the options change the output too. A model selecting a
// time series provider needs CompileWith, which supplies the provider's paths.
func Compile(model *ir.IR, opts *kubecfg.Options) (*config.Overlay, error) {
	overlay, _, err := CompileWith(model, opts, nil)
	return overlay, err
}

// RouteBackends names the generated backends serving one Kubernetes object
type RouteBackends struct {
	Source   ir.Source
	Backends []string
}

// Manifest maps every routed Kubernetes object to the generated backends serving it, ordered by
// source, so a metric on a generated backend can be joined back to the object it serves
type Manifest []RouteBackends

// CompileWithManifest compiles the IR and also reports which generated
// backends serve which object
func CompileWithManifest(model *ir.IR, opts *kubecfg.Options) (*config.Overlay, Manifest, error) {
	return CompileWith(model, opts, nil)
}

// CompileWith compiles the IR with a source of the paths each time series provider predefines,
// which a rule selecting a provider is served through; nil refuses such a rule
func CompileWith(model *ir.IR, opts *kubecfg.Options, providers ProviderPaths,
) (*config.Overlay, Manifest, error) {
	if opts == nil {
		return nil, nil, ErrNoOptions
	}
	doc, err := buildDocument(model, opts, providers)
	if err != nil {
		return nil, nil, err
	}
	var data []byte
	if !doc.isEmpty() {
		// an IR with nothing in it must yield no overlay data at all,
		// rather than a document of empty sections
		if data, err = yaml.Marshal(doc); err != nil {
			return nil, nil, fmt.Errorf("marshal generated configuration: %w", err)
		}
	}
	sum := sha256.Sum256(data)
	return &config.Overlay{
		Data:    data,
		Prefix:  Prefix,
		Version: hex.EncodeToString(sum[:]),
	}, manifest(model, doc), nil
}

func manifest(model *ir.IR, doc *document) Manifest {
	if model == nil || doc == nil {
		return nil
	}
	index := make(map[string]int)
	var out Manifest
	for _, r := range model.Routes {
		id := SourceName(r.Source)
		if _, ok := index[id]; ok {
			continue
		}
		index[id] = len(out)
		out = append(out, RouteBackends{Source: r.Source})
	}
	for _, name := range slices.Sorted(maps.Keys(doc.Backends)) {
		id, _, _ := strings.Cut(name, ruleInfix)
		if i, ok := index[id]; ok {
			out[i].Backends = append(out[i].Backends, name)
		}
	}
	slices.SortFunc(out, func(a, b RouteBackends) int {
		return strings.Compare(a.Source.Key(), b.Source.Key())
	})
	return out
}

// document is the overlay's YAML shape: a projection of the configuration types, so the YAML
// carries only what the controller sets; a test pins it to the real structs
type document struct {
	Discovery        map[string]*discoveryDoc `yaml:"discovery,omitempty"`
	Listeners        map[string]*listenerDoc  `yaml:"listeners,omitempty"`
	Backends         map[string]*backendDoc   `yaml:"backends,omitempty"`
	RequestRewriters map[string]*rewriterDoc  `yaml:"request_rewriters,omitempty"`
}

func (d *document) isEmpty() bool {
	return len(d.Discovery) == 0 && len(d.Listeners) == 0 && len(d.Backends) == 0 &&
		len(d.RequestRewriters) == 0
}

func (d *document) discoverer(opts *kubecfg.Options) string {
	// every generated ALB shares it: the entry is the connection, and what each
	// ALB selects is its own query.
	name := DiscovererName()
	if d.Discovery == nil {
		conn := opts.Connection
		if conn == nil {
			conn = kubeopts.New()
		}
		d.Discovery = map[string]*discoveryDoc{name: {
			Provider: dproviders.Kubernetes, Kubernetes: conn.Clone(),
		}}
	}
	return name
}

// discoveryDoc is the projection of a generated discovery entry
type discoveryDoc struct {
	Provider   string            `yaml:"provider,omitempty"`
	Kubernetes *kubeopts.Options `yaml:"kubernetes,omitempty"`
}

func (d *document) rewriter(name string, instructions [][]string) {
	if d.RequestRewriters == nil {
		d.RequestRewriters = make(map[string]*rewriterDoc)
	}
	d.RequestRewriters[name] = &rewriterDoc{Instructions: instructions}
}

type listenerDoc struct {
	Protocol        string `yaml:"protocol,omitempty"`
	Port            int    `yaml:"port,omitempty"`
	TLSListenPort   int    `yaml:"tls_port,omitempty"`
	TLSRuntimeCerts bool   `yaml:"tls_runtime_certs,omitempty"`
}

type backendDoc struct {
	Provider             string       `yaml:"provider,omitempty"`
	IsTemplate           bool         `yaml:"is_template,omitempty"`
	OriginURL            string       `yaml:"origin_url,omitempty"`
	RuleName             string       `yaml:"rule_name,omitempty"`
	Hosts                []string     `yaml:"hosts,omitempty"`
	ListenerNames        []string     `yaml:"listener_names,omitempty"`
	CacheName            string       `yaml:"cache_name,omitempty"`
	CacheKeyPrefix       string       `yaml:"cache_key_prefix,omitempty"`
	NegativeCacheName    string       `yaml:"negative_cache_name,omitempty"`
	MaxTTL               string       `yaml:"max_ttl,omitempty"`
	Timeout              string       `yaml:"timeout,omitempty"`
	TLS                  *tlsDoc      `yaml:"tls,omitempty"`
	HealthCheck          *ho.Options  `yaml:"healthcheck,omitempty"`
	TracingConfigName    string       `yaml:"tracing_name,omitempty"`
	ReqRewriterName      string       `yaml:"req_rewriter_name,omitempty"`
	AuthenticatorName    string       `yaml:"authenticator_name,omitempty"`
	PathRoutingDisabled  bool         `yaml:"path_routing_disabled,omitempty"`
	PathDefaultsDisabled bool         `yaml:"path_defaults_disabled,omitempty"`
	AnyHostRouting       bool         `yaml:"any_host_routing,omitempty"`
	AccessLog            *alo.Options `yaml:"access_log,omitempty"`
	H2CPriorKnowledge    bool         `yaml:"h2c_prior_knowledge,omitempty"`
	PreserveHost         bool         `yaml:"preserve_host,omitempty"`
	Paths                []*pathDoc   `yaml:"paths,omitempty"`
	ALB                  *albDoc      `yaml:"alb,omitempty"`
}

type pathDoc struct {
	Path                    string            `yaml:"path,omitempty"`
	MatchType               string            `yaml:"match_type,omitempty"`
	Handler                 string            `yaml:"handler,omitempty"`
	Methods                 []string          `yaml:"methods,omitempty"`
	RequestHeaders          map[string]string `yaml:"request_headers,omitempty"`
	ResponseHeaders         map[string]string `yaml:"response_headers,omitempty"`
	CORS                    *corsDoc          `yaml:"cors,omitempty"`
	CollapsedForwardingName string            `yaml:"collapsed_forwarding,omitempty"`
	ReqRewriterName         string            `yaml:"req_rewriter_name,omitempty"`
	ResponseCode            int               `yaml:"response_code,omitempty"`
	ResponseBody            *string           `yaml:"response_body,omitempty"`
	MatchHeaders            []*conditionDoc   `yaml:"match_headers,omitempty"`
	MatchQueryParams        []*conditionDoc   `yaml:"match_query_params,omitempty"`
	MatchOrder              int               `yaml:"match_order,omitempty"`
	CacheKeyParams          []string          `yaml:"cache_key_params,omitempty"`
	CacheKeyHeaders         []string          `yaml:"cache_key_headers,omitempty"`
	CacheKeyFormFields      []string          `yaml:"cache_key_form_fields,omitempty"`
	NoMetrics               bool              `yaml:"no_metrics,omitempty"`
	HideResultHeader        bool              `yaml:"hide_result_header,omitempty"`
	Timeout                 string            `yaml:"timeout,omitempty"`
	AttemptTimeout          string            `yaml:"attempt_timeout,omitempty"`
	Retry                   *retryDoc         `yaml:"retry,omitempty"`
	Mirrors                 []*mirrorDoc      `yaml:"mirrors,omitempty"`
}

// retryDoc is the projection of a path's retry policy
type retryDoc struct {
	Attempts      int    `yaml:"attempts,omitempty"`
	Codes         []int  `yaml:"codes,omitempty"`
	Backoff       string `yaml:"backoff,omitempty"`
	BudgetPercent int    `yaml:"budget_percent,omitempty"`
}

// mirrorDoc is the projection of a path's request mirror
type mirrorDoc struct {
	BackendName string `yaml:"backend_name,omitempty"`
	Percent     int    `yaml:"percent,omitempty"`
}

// conditionDoc is one header or query parameter condition of a generated path
type conditionDoc struct {
	Name  string `yaml:"name,omitempty"`
	Value string `yaml:"value,omitempty"`
	Regex bool   `yaml:"regex,omitempty"`
}

// corsDoc is the CORS response-header policy projection
type corsDoc struct {
	Mode    string            `yaml:"mode,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
}

// rewriterDoc is the request rewriter projection
type rewriterDoc struct {
	Instructions [][]string `yaml:"instructions,omitempty"`
}

type albDoc struct {
	Mechanism string           `yaml:"mechanism,omitempty"`
	Pool      []*albPoolDoc    `yaml:"pool,omitempty"`
	Discovery *albDiscoveryDoc `yaml:"discovery,omitempty"`
}

// albDiscoveryDoc binds a generated ALB's pool to the generated discoverer:
// the Service's EndpointSlices, cloned from a generated template
type albDiscoveryDoc struct {
	DiscovererName  string    `yaml:"discoverer_name,omitempty"`
	TemplateBackend string    `yaml:"template_backend,omitempty"`
	HealthMode      string    `yaml:"health_mode,omitempty"`
	Query           *queryDoc `yaml:"query,omitempty"`
}

// queryDoc selects one Service port's endpoints
type queryDoc struct {
	Kind      string `yaml:"kind,omitempty"`
	Namespace string `yaml:"namespace,omitempty"`
	Service   string `yaml:"service,omitempty"`
	Port      string `yaml:"port,omitempty"`
	Scheme    string `yaml:"scheme,omitempty"`
}

// tlsDoc is the upstream TLS client projection a BackendTLSPolicy produces
type tlsDoc struct {
	ServerName              string `yaml:"server_name,omitempty"`
	CertificateAuthorityPEM string `yaml:"certificate_authority_pem,omitempty"`
	ExcludeSystemRoots      bool   `yaml:"exclude_system_roots,omitempty"`
}

type albPoolDoc struct {
	Name   string `yaml:"name,omitempty"`
	Weight int    `yaml:"weight,omitempty"`
}

func buildDocument(model *ir.IR, opts *kubecfg.Options, providers ProviderPaths,
) (*document, error) {
	doc := &document{}
	if model.IsEmpty() {
		return doc, nil
	}
	c := model.Canonical()
	doc.Listeners = compileListeners(c.Listeners)
	idx := index{
		groups:    make(map[string]ir.BackendGroup, len(c.Backends)),
		policies:  make(map[string]ir.Policy, len(c.Policies)),
		listeners: make(map[string]string, len(c.Listeners)),
		ports:     make(map[string]int, len(c.Listeners)),
		providers: providers,
	}
	for _, g := range c.Backends {
		idx.groups[g.Name] = g
	}
	for _, p := range c.Policies {
		idx.policies[p.Name] = p
	}
	for _, l := range c.Listeners {
		idx.listeners[l.Name] = CompiledListenerName(l)
		idx.ports[l.Name] = l.Port
	}
	// stream routes are relayed whole rather than planned by path, so they are compiled apart
	httpRoutes, streamRoutes := splitRoutes(c.Routes)
	planned := *c
	planned.Routes = httpRoutes
	if err := compileRoutes(doc, &planned, idx, opts); err != nil {
		return nil, err
	}
	if err := compileStreamRoutes(doc, streamRoutes, idx, opts); err != nil {
		return nil, err
	}
	bindUnusedListeners(doc)
	return doc, nil
}

// bindUnusedListeners gives every generated HTTP listener no backend reaches one of its own answering
// 404, so the port is bound and an HTTPS listener holds its certificates before any route attaches
func bindUnusedListeners(doc *document) {
	used := make(map[string]bool)
	for _, b := range doc.Backends {
		for _, l := range b.ListenerNames {
			used[l] = true
		}
	}
	for name, l := range doc.Listeners {
		if used[name] || l.Protocol != "" {
			continue
		}
		if doc.Backends == nil {
			doc.Backends = make(map[string]*backendDoc)
		}
		doc.Backends[PlaceholderName(name)] = placeholderBackend(name)
	}
}

// index is the by-name lookup the route compiler resolves references through: backend groups,
// policies, and IR listener names mapped onto the Trickster listeners they compiled to
type index struct {
	groups    map[string]ir.BackendGroup
	policies  map[string]ir.Policy
	listeners map[string]string
	// ports maps an IR listener name to the port it serves; zero for a
	// listener the operator configured, whose port the IR does not know
	ports map[string]int
	// providers supplies the paths a time series provider predefines
	providers ProviderPaths
}

func (i index) listenerPort(route ir.Route) int {
	// a redirect naming neither scheme nor port sends the client back to the listener's port,
	// which is only expressible when the route's listeners agree on it
	var port int
	for _, name := range route.Listeners {
		p, ok := i.ports[name]
		if !ok || p == 0 {
			return 0
		}
		if port != 0 && p != port {
			return 0
		}
		port = p
	}
	return port
}

func (i index) listenerNames(route ir.Route) []string {
	if len(route.Listeners) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(route.Listeners))
	out := make([]string, 0, len(route.Listeners))
	for _, name := range route.Listeners {
		compiled, ok := i.listeners[name]
		if !ok {
			// the route names a listener the IR does not define; the
			// translator reports it in status and it binds nothing here
			continue
		}
		if _, dup := seen[compiled]; dup {
			continue
		}
		seen[compiled] = struct{}{}
		out = append(out, compiled)
	}
	slices.Sort(out)
	return out
}

func compileListeners(listeners []ir.Listener) map[string]*listenerDoc {
	// two IR listeners on the same port are the Gateway API's same-port merge, and
	// collapse onto one entry.
	if len(listeners) == 0 {
		return nil
	}
	out := make(map[string]*listenerDoc, len(listeners))
	for _, l := range listeners {
		if l.External {
			// the operator configured this listener; the routes attach to
			// it by name and the controller has nothing to say about it
			continue
		}
		name := ListenerName(l.Port, l.Protocol)
		if _, ok := out[name]; ok {
			continue
		}
		if l.Protocol == ir.ProtocolHTTPS {
			// certificates arrive at runtime from Secrets, so the port must
			// be kept open with no certificate file behind it (0.C)
			out[name] = &listenerDoc{
				TLSListenPort: l.Port, TLSRuntimeCerts: true,
			}
			continue
		}
		if ir.IsStream(l.Protocol) {
			out[name] = &listenerDoc{Protocol: l.Protocol, Port: l.Port}
			continue
		}
		out[name] = &listenerDoc{Port: l.Port}
	}
	return out
}

// ListenerName is the generated name of the Trickster listener serving a port; listeners are
// per-port rather than per-Gateway because a port binds once
func ListenerName(port int, protocol string) string {
	return Prefix + "listener-" + protocol + "-" + strconv.Itoa(port)
}

// CompiledListenerName is the name the routing configuration knows an IR listener by: its own
// for one the operator configured, the generated one for a listener the controller mints
func CompiledListenerName(l ir.Listener) string {
	if l.External {
		return l.Name
	}
	return ListenerName(l.Port, l.Protocol)
}
