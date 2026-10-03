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

package validate

import (
	stderrors "errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	albregistry "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/registry"
	albtypes "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	"github.com/trickstercache/trickster/v2/pkg/backends/rule"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	logmanager "github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"
	tr "github.com/trickstercache/trickster/v2/pkg/observability/tracing/registry"
	ar "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/registry"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

func Validate(c *config.Config) error {
	if c == nil {
		return errors.ErrInvalidOptions
	}
	if c.MgmtConfig != nil {
		if err := c.MgmtConfig.Validate(); err != nil {
			return err
		}
	}
	if c.Logging != nil {
		if _, err := c.Logging.Validate(); err != nil {
			return err
		}
	}
	if c.AccessLog != nil {
		if _, err := c.AccessLog.Validate(); err != nil {
			return fmt.Errorf("access_log: %w", err)
		}
	}
	if c.Metrics != nil {
		if _, err := c.Metrics.Validate(); err != nil {
			return err
		}
	}
	if err := Tracers(c); err != nil {
		return err
	}
	if err := Rewriters(c); err != nil {
		return err
	}
	if err := Rules(c); err != nil {
		return err
	}
	if err := Authenticators(c); err != nil {
		return err
	}
	if err := IPACLs(c); err != nil {
		return err
	}
	if err := Caches(c); err != nil {
		return err
	}
	if err := NegativeCaches(c); err != nil {
		return err
	}
	if err := Discoverers(c); err != nil {
		return err
	}
	if c.Kubernetes != nil {
		if err := c.Kubernetes.Validate(); err != nil {
			return fmt.Errorf("kubernetes: %w", err)
		}
		if err := kubernetesReferences(c); err != nil {
			return err
		}
	}
	if err := Backends(c); err != nil {
		return err
	}
	if err := LoggingFiles(c); err != nil {
		return err
	}
	if err := Listeners(c); err != nil {
		return err
	}
	return ACME(c)
}

// LoggingFiles validates shared rotation settings across all configured logs.
func LoggingFiles(c *config.Config) error {
	if c == nil {
		return nil
	}
	return logmanager.ValidateOptions(c.LogManagerOptions()...)
}

func Rewriters(c *config.Config) error {
	if c == nil || len(c.RequestRewriters) == 0 {
		return nil
	}
	return c.RequestRewriters.Validate()
}

func Tracers(c *config.Config) error {
	if c == nil || len(c.TracingOptions) == 0 {
		return nil
	}
	return c.TracingOptions.Validate()
}

func Rules(c *config.Config) error {
	if c == nil || len(c.Rules) == 0 {
		return nil
	}
	return c.Rules.Validate()
}

func Caches(c *config.Config) error {
	if c == nil || len(c.Caches) == 0 {
		return nil
	}
	return c.Caches.Validate()
}

// kubernetesReferences checks every object the kubernetes section names
// against the configuration that defines it, the same way a backend's names
// are checked.
//
// The controller generates backends carrying these names, so an undefined
// one would otherwise fail every reload the controller asks for rather than
// this one, and the operator would see it as the controller having stopped
// working rather than as their own typo. A name a route supplies by
// annotation is checked in the translator instead, where it can be rejected
// as that one object's mistake.
func kubernetesReferences(c *config.Config) error {
	if !c.Kubernetes.IsEnabled() {
		return nil
	}
	for _, name := range c.Kubernetes.Listeners() {
		if _, ok := c.Listeners[name]; !ok {
			return newKubernetesRefError("ingress", "listener", name)
		}
	}
	d := c.Kubernetes.Defaults
	if d == nil {
		return nil
	}
	if d.CacheName != "" {
		if _, ok := c.Caches[d.CacheName]; !ok {
			return newKubernetesRefError("defaults", "cache", d.CacheName)
		}
	}
	if d.NegativeCacheName != "" {
		if _, ok := c.NegativeCacheConfigs[d.NegativeCacheName]; !ok {
			return newKubernetesRefError("defaults", "negative cache",
				d.NegativeCacheName)
		}
	}
	if d.TracingName != "" {
		if _, ok := c.TracingOptions[d.TracingName]; !ok {
			return newKubernetesRefError("defaults", "tracing config",
				d.TracingName)
		}
	}
	if d.ReqRewriterName != "" {
		if _, ok := c.RequestRewriters[d.ReqRewriterName]; !ok {
			return newKubernetesRefError("defaults", "request rewriter",
				d.ReqRewriterName)
		}
	}
	if d.AuthenticatorName != "" {
		if _, ok := c.Authenticators[d.AuthenticatorName]; !ok {
			return newKubernetesRefError("defaults", "authenticator",
				d.AuthenticatorName)
		}
	}
	if d.IPACLName != "" {
		def := c.IPACLs[d.IPACLName]
		if def == nil {
			return newKubernetesRefError("defaults", "ip acl", d.IPACLName)
		}
		// compiled by IPACLs above. A peer or drop list exists, so it is not an
		// undefined name; a generated backend still cannot use it.
		if !kubernetesIPACLEligible(def.Compiled) {
			return fmt.Errorf("kubernetes 'defaults' references ineligible ip acl %q: "+
				"generated backends require source client_ip and action reject",
				d.IPACLName)
		}
	}
	return nil
}

// kubernetesIPACLEligible reports whether a compiled list may be named by a
// generated backend. ClientIP and Reject are the zero values, which are also
// the defaults. A nil list is not eligible.
func kubernetesIPACLEligible(list *ipacl.List) bool {
	return list != nil && list.Source() == ipacl.ClientIP && list.Action() == ipacl.Reject
}

func newKubernetesRefError(block, kind, name string) error {
	return fmt.Errorf("kubernetes '%s' references undefined %s %q",
		block, kind, name)
}

// Discoverers validates the top-level discovery section
func Discoverers(c *config.Config) error {
	if c == nil || len(c.Discovery) == 0 {
		return nil
	}
	return c.Discovery.Validate()
}

func NegativeCaches(c *config.Config) error {
	if c == nil || len(c.NegativeCacheConfigs) == 0 {
		return nil
	}
	nc, err := c.NegativeCacheConfigs.ValidateAndCompile()
	if err != nil {
		return err
	}
	c.CompiledNegativeCaches = nc
	return nil
}

func Authenticators(c *config.Config) error {
	if c == nil || len(c.Authenticators) == 0 {
		return nil
	}
	return c.Authenticators.Validate(ar.IsRegistered)
}

func Backends(c *config.Config) error {
	if c == nil {
		return errors.ErrNoValidBackends
	}
	if len(c.Backends) == 0 {
		return errors.ErrNoValidBackends
	}
	if err := c.Backends.ValidateConfigMappings(c.Caches, c.CompiledNegativeCaches,
		c.Rules, c.RequestRewriters, c.Authenticators, c.TracingOptions, c.IPACLs); err != nil {
		return err
	}
	if err := c.Backends.ValidateDiscovery(c.Discovery); err != nil {
		return err
	}
	serveTLS, err := c.Backends.ValidateTLSConfigs()
	if err != nil {
		return err
	}
	if serveTLS && c.Frontend != nil {
		c.Frontend.ServeTLS = true
	}
	if err := c.Backends.Validate(); err != nil {
		return err
	}
	warnStepAlignments(c)
	return nil
}

func warnStepAlignments(c *config.Config) {
	nativeListeners := providerregistry.NativeListeners()
	for _, name := range slices.Sorted(maps.Keys(c.Backends)) {
		o := c.Backends[name]
		if o == nil || o.StepAlignment == 0 {
			continue
		}
		if o.ProxyOnly && o.StepAlignment == timeseries.StepAlignmentOff {
			addWarning(c, fmt.Sprintf("backend %q sets step_alignment: off, which has no effect "+
				"with proxy_only: true, since nothing is cached", name))
		}
		if o.Provider == providers.ALB && servesNativeListener(c, o, nativeListeners) {
			addWarning(c, fmt.Sprintf("alb %q sets step_alignment, which it applies to its http "+
				"requests only; the members it sends native protocol sessions to use their own", name))
		}
	}
}

// Listeners validates inbound listener definitions and backend mappings.
func Listeners(c *config.Config) error {
	if c == nil || len(c.Listeners) == 0 {
		return stderrors.New("no listeners configured")
	}

	mapped := make(map[string]int, len(c.Listeners))
	tlsMapped := make(map[string]bool, len(c.Listeners))
	mappedProviders := make(map[string]map[string]string, len(c.Listeners))
	nativeListeners := providerregistry.NativeListeners()
	nativeTargets := nativeUserRouterTargets(c, nativeListeners)
	nativeProtocols := nativeBackendProtocols(c, nativeListeners)
	// the load balancers that serve a stream or native listener; every other one serves requests
	streamALBs := sets.NewStringSet()
	for backendName, backend := range c.Backends {
		if backend == nil || backend.IsTemplate {
			// templates are never routed, so they map to no listener
			continue
		}
		backend.NormalizeListenerNames()
		backend.HasHTTPListener = false
		backend.NativeListenerProtocols = nativeProtocols[backendName]
		listenerNames := backend.ListenerNames
		if len(listenerNames) == 0 && !nativeTargets[backendName] {
			listenerNames = []string{listener.DefaultFrontendName}
		}
		for _, name := range listenerNames {
			if lo := c.Listeners[name]; lo != nil && (lo.Protocol == "" || strings.EqualFold(lo.Protocol, listener.ProtocolHTTP)) {
				backend.HasHTTPListener = true
			}
		}
		for _, adapter := range nativeListeners.ForProvider(strings.ToLower(backend.Provider)) {
			if err := adapter.ValidateBackend(backend); err != nil {
				return fmt.Errorf("%s backend %q: %w", adapter.Protocol(), backendName, err)
			}
		}
		if nativeTargets[backendName] && len(backend.ListenerNames) == 0 {
			continue
		}
		if backend.Provider == providers.ALB && backend.ALBOptions != nil &&
			backend.ALBOptions.UserRouter != nil {
			targetProvider := strings.ToLower(backend.ALBOptions.UserRouter.TargetProvider)
			adapters := nativeListeners.ForProvider(targetProvider)
			for _, adapter := range adapters {
				if len(adapters) > 1 && !slices.Contains(backend.NativeListenerProtocols, adapter.Protocol()) {
					continue
				}
				if err := adapter.ValidateUserRouter(c, backendName, backend); err != nil {
					return err
				}
			}
		}
		if len(backend.ListenerNames) == 0 {
			backend.ListenerNames = []string{listener.DefaultFrontendName}
		}
		for _, name := range backend.ListenerNames {
			if name == "" {
				return fmt.Errorf("backend %q has an empty listener name", backendName)
			}
			if name == mgmt.ListenerNameMgmt || name == mgmt.ListenerNameMetrics {
				return fmt.Errorf("backend %q cannot use reserved listener %q", backendName, name)
			}
			lo := c.Listeners[name]
			if lo == nil {
				return fmt.Errorf("backend %q references undefined listener %q", backendName, name)
			}
			mapped[name]++
			if mappedProviders[name] == nil {
				mappedProviders[name] = make(map[string]string)
			}
			mappedProviders[name][backendName] = strings.ToLower(backend.Provider)
			if backend.TLS != nil && backend.TLS.ServeTLS {
				tlsMapped[name] = true
			}
		}
	}

	ports := make(map[string]string)
	for name, options := range c.Listeners {
		if name == "" || options == nil {
			return stderrors.New("invalid empty listener configuration")
		}
		options.Protocol = strings.ToLower(options.Protocol)
		if options.Protocol == "" {
			options.Protocol = listener.ProtocolHTTP
		}
		if options.Protocol != listener.ProtocolHTTP && options.TLSListenPort > 0 {
			return fmt.Errorf("listener %q cannot configure a TLS port for protocol %q", name, options.Protocol)
		}
		if options.Protocol != listener.ProtocolHTTP && options.TLSRuntimeCerts {
			return fmt.Errorf("listener %q cannot use tls_runtime_certs with protocol %q", name, options.Protocol)
		}
		if options.Protocol != listener.ProtocolHTTP && !options.IsStream() && mapped[name] > 1 {
			return fmt.Errorf("listener %q with protocol %q can map to only one backend", name, options.Protocol)
		}
		if options.Stream != nil && !options.IsStream() {
			return fmt.Errorf("listener %q configures stream options for protocol %q", name, options.Protocol)
		}
		if options.IsStream() {
			if err := streamListener(c, name, options, mappedProviders[name], streamALBs); err != nil {
				return err
			}
		}
		nativeAdapter := nativeListeners.Get(options.Protocol)
		if options.Protocol != listener.ProtocolHTTP && nativeAdapter == nil && !options.IsStream() {
			return fmt.Errorf("listener %q uses unsupported protocol %q", name, options.Protocol)
		}
		if nativeAdapter != nil {
			if err := nativeAdapter.ValidateListener(options); err != nil {
				return fmt.Errorf("listener %q: %w", name, err)
			}
		}
		mismatchedProtocol := nativeListeners.ConfiguredProtocol(options, options.Protocol)
		if mismatchedProtocol != "" {
			return fmt.Errorf("listener %q configures %s limits for protocol %q",
				name, mismatchedProtocol, options.Protocol)
		}
		// Native listeners accept either a backend of the same protocol or a
		// User Router ALB whose validated terminal provider matches it.
		for backendName, provider := range mappedProviders[name] {
			backend := c.Backends[backendName]
			targetProvider := provider
			if provider == providers.ALB && backend != nil && backend.ALBOptions != nil &&
				backend.ALBOptions.UserRouter != nil {
				targetProvider = strings.ToLower(backend.ALBOptions.UserRouter.TargetProvider)
			} else if provider == providers.ALB && options.Protocol != listener.ProtocolHTTP && nativeAdapter != nil {
				// a selection strategy balances the listener's sessions over its pool
				if err := sessionBalancer(c, name, options.Protocol, backendName, backend, nativeAdapter); err != nil {
					return err
				}
				streamALBs.Set(backendName)
				// its pool members were each checked against the adapter's providers
				continue
			}
			if options.Protocol != listener.ProtocolHTTP && nativeAdapter != nil &&
				!nativeAdapter.ServesProvider(targetProvider) {
				return fmt.Errorf("listener %q with protocol %q cannot map to backend %q with provider %q",
					name, options.Protocol, backendName, provider)
			}
			if options.Protocol == listener.ProtocolHTTP {
				adapters := nativeListeners.ForProvider(targetProvider)
				allowed := len(adapters) == 0
				var protocols []string
				for _, adapter := range adapters {
					allowed = allowed || adapter.SupportsHTTP(targetProvider)
					protocols = append(protocols, adapter.Protocol())
				}
				if !allowed {
					return fmt.Errorf("backend %q with provider %q requires a listener with protocol %q", backendName, provider, strings.Join(protocols, " or "))
				}
			}
		}
		if options.ListenPort < 0 || options.TLSListenPort < 0 {
			return fmt.Errorf("listener %q has an invalid listen port", name)
		}
		if _, err := clientip.ParseTrusted(options.TrustedProxies); err != nil {
			return fmt.Errorf("listener %q: %w", name, err)
		}
		if err := bindListenerIPACL(c, name, options); err != nil {
			return err
		}
		if err := options.PathNormalization.Validate(); err != nil {
			return fmt.Errorf("listener %q: path_normalization: %w", name, err)
		}
		if err := options.ValidateHTTPLimits(); err != nil {
			return fmt.Errorf("listener %q: %w", name, err)
		}

		builtIn := name == listener.DefaultFrontendName ||
			name == mgmt.ListenerNameMgmt || name == mgmt.ListenerNameMetrics
		options.Active = name == mgmt.ListenerNameMgmt || name == mgmt.ListenerNameMetrics || mapped[name] > 0
		if !builtIn && mapped[name] == 0 {
			addWarning(c, fmt.Sprintf("listener %q is unused and will not be started", name))
		}

		runtimeCerts := options.TLSRuntimeCerts || acmeOnDemandListener(c, name)
		if options.TLSListenPort > 0 && !tlsMapped[name] && !runtimeCerts {
			addWarning(c, fmt.Sprintf(
				"listener %q TLS port is disabled because no mapped backend provides a TLS certificate", name))
			options.TLSListenPort = 0
			options.ServeTLS = false
		} else {
			options.ServeTLS = options.TLSListenPort > 0 && (tlsMapped[name] || runtimeCerts)
		}
		if options.Active && options.ListenPort == 0 && options.TLSListenPort == 0 {
			addWarning(c, fmt.Sprintf("listener %q has no enabled ports and will not be started", name))
		}

		if options.HTTP3 != nil && options.HTTP3.Enabled && !options.HTTP3Enabled() {
			addWarning(c, fmt.Sprintf(
				"listener %q HTTP/3 is disabled because it requires an http listener with a TLS port", name))
			options.HTTP3.Enabled = false
		}

		// a port is reserved in its transport's space: a udp listener and an HTTP/3 endpoint
		// bind UDP, everything else TCP, so a tcp and a udp listener may share a port number
		if options.Active && options.ListenPort > 0 {
			transport := transportTCP
			if options.Protocol == listener.ProtocolUDP {
				transport = transportUDP
			}
			if err := reserveListenerPort(ports, name, transport, options.ListenAddress,
				options.ListenPort); err != nil {
				return err
			}
		}
		if options.Active && options.TLSListenPort > 0 {
			if err := reserveListenerPort(ports, name, transportTCP, options.TLSListenAddress,
				options.TLSListenPort); err != nil {
				return err
			}
		}
		if h3Address, h3Port, _ := options.HTTP3Endpoint(); options.Active && h3Port > 0 {
			if err := reserveListenerPort(ports, name, transportUDP, h3Address, h3Port); err != nil {
				return err
			}
		}
	}
	if err := validateIPACLPlacements(c); err != nil {
		return err
	}
	return requestALBs(c, streamALBs)
}

// sessionBalancer validates an ALB that a native protocol listener maps to without a user
// router: its strategy must serve sessions, over a pool of the listener's own kind of backend
func sessionBalancer(c *config.Config, listenerName, protocol, backendName string, backend *bo.Options,
	adapter native.Adapter,
) error {
	if backend == nil || backend.ALBOptions == nil ||
		!albregistry.Supports(backend.ALBOptions.MechanismName, albtypes.PlaneNative) {
		return fmt.Errorf("listener %q with protocol %q requires alb backend %q to use a mechanism that "+
			"routes or balances sessions: %s", listenerName, protocol, backendName,
			strings.Join(albregistry.Supporting(albtypes.PlaneNative), ", "))
	}
	o := backend.ALBOptions
	for _, m := range o.Pool {
		if member := c.Backends[m.Name]; member == nil ||
			!adapter.ServesProvider(strings.ToLower(member.Provider)) {
			return fmt.Errorf("listener %q with protocol %q requires every pool member of alb backend %q "+
				"to be a %s backend: %q is not", listenerName, protocol, backendName,
				strings.Join(adapter.Providers(), " or "), m.Name)
		}
	}
	if o.Discovery != nil {
		return fmt.Errorf("alb backend %q: 'discovery' is not supported on a %s listener", backendName, protocol)
	}
	if o.Stream != nil {
		return fmt.Errorf("alb backend %q: 'stream' options apply only to an alb that serves a "+
			"tcp, tls or udp listener", backendName)
	}
	if !o.HRW.KeySource.OnNative() {
		return fmt.Errorf("listener %q with protocol %q cannot read alb backend %q's hrw.key %q: "+
			"use client_ip or user", listenerName, protocol, backendName, o.HRW.Key)
	}
	if err := stickyOnFlows(listenerName, protocol, backendName, o, func() bool {
		return o.Sticky.Table.KeySource.OnNative()
	}); err != nil {
		return err
	}
	return adapter.ValidateBalancer(c, backendName, backend)
}

// streamProviders are the providers a stream listener may relay to: one with an origin to dial,
// or a pool of them
var streamProviders = sets.New([]string{
	providers.ReverseProxyShort, providers.ReverseProxy, providers.Proxy, providers.ALB,
})

// requestALBs holds every load balancer that serves an http listener to what a request can
// offer: no server name or session to key on and no connect to time. One that also serves a
// stream or native listener was held to that listener's rules as well, so its settings must
// suit every listener it serves. streamALBs names those that serve a stream or native listener.
func requestALBs(c *config.Config, streamALBs sets.Set[string]) error {
	members := c.Backends.PoolMembers()
	for _, backendName := range slices.Sorted(maps.Keys(c.Backends)) {
		backend := c.Backends[backendName]
		if backend == nil || backend.Provider != providers.ALB || backend.ALBOptions == nil {
			continue
		}
		o := backend.ALBOptions
		// a mechanism that commits a flow to several members at once is the relay's to carry out
		streamOnly := albregistry.Supports(o.MechanismName, albtypes.PlaneStream) &&
			!albregistry.Supports(o.MechanismName, albtypes.PlaneHTTP)
		if streamOnly && members.Contains(backendName) {
			return fmt.Errorf("alb backend %q: mechanism %q cannot be a member of another alb's pool",
				backendName, o.MechanismName)
		}
		if o.Stream != nil && !streamALBs.Contains(backendName) {
			return fmt.Errorf("alb backend %q: 'stream' options apply only to an alb that serves a "+
				"tcp, tls or udp listener", backendName)
		}
		httpListener := servesHTTPListener(c, backend)
		if httpListener == "" {
			if issuesTokens(o) {
				return fmt.Errorf("alb backend %q: sticky.mode %q issues tokens, which only an http "+
					"listener carries, and it serves none", backendName, o.Sticky.Mode)
			}
			continue
		}
		if streamOnly {
			return fmt.Errorf("alb backend %q: mechanism %q requires a %s listener, and cannot serve "+
				"http listener %q", backendName, o.MechanismName,
				strings.Join(albregistry.StreamProtocols(o.MechanismName), " or "), httpListener)
		}
		if !o.HRW.KeySource.OnHTTP() {
			return fmt.Errorf("alb backend %q: hrw.key %q cannot be read from a request, which http "+
				"listener %q serves", backendName, o.HRW.Key, httpListener)
		}
		if _, err := o.LTSignalFor(listener.ProtocolHTTP); err != nil {
			return fmt.Errorf("alb backend %q: %w", backendName, err)
		}
		if err := stickyOnRequests(backendName, httpListener, o); err != nil {
			return err
		}
		// only an http listener is sent tokens, and there the mode defaults to cookie
		if w := o.Sticky.KeyWarning(backendName); w != "" {
			addWarning(c, w)
		}
	}
	return nil
}

// servesHTTPListener returns the name of an http listener the backend is mapped to, or "" when
// it has none. A backend that names no listener serves the default one, which is http.
func servesHTTPListener(c *config.Config, backend *bo.Options) string {
	if names := httpListenerNames(c, backend); len(names) > 0 {
		return names[0]
	}
	return ""
}

// httpListenerNames returns the names of the http listeners the backend is mapped to, in order
func httpListenerNames(c *config.Config, backend *bo.Options) []string {
	if len(backend.ListenerNames) == 0 {
		return []string{listener.DefaultFrontendName}
	}
	var out []string
	for _, name := range backend.ListenerNames {
		lo := c.Listeners[name]
		if lo == nil || lo.Protocol == "" || lo.Protocol == listener.ProtocolHTTP {
			out = append(out, name)
		}
	}
	return out
}

func streamListener(c *config.Config, name string, options *listener.Options,
	mapped map[string]string, streamALBs sets.Set[string],
) error {
	if err := options.Stream.Validate(); err != nil {
		return fmt.Errorf("listener %q: %w", name, err)
	}
	if len(mapped) == 0 {
		return nil
	}
	// a tls listener demultiplexes by server name, so its backends are told apart by their
	// hosts; a tcp or udp listener reads nothing and relays everything to its one backend. A
	// pool member is reached through its pool and is neither.
	members := c.Backends.PoolMembers()
	table := l4.NewTable()
	var served int
	for _, backendName := range slices.Sorted(maps.Keys(mapped)) {
		provider := mapped[backendName]
		backend := c.Backends[backendName]
		if !streamProviders.Contains(provider) {
			return fmt.Errorf("listener %q with protocol %q cannot map to backend %q with provider %q",
				name, options.Protocol, backendName, provider)
		}
		if provider == providers.ALB && (backend.ALBOptions == nil ||
			!albregistry.Supports(backend.ALBOptions.MechanismName, albtypes.PlaneStream)) {
			return fmt.Errorf("listener %q with protocol %q requires alb backend %q to use a mechanism that "+
				"serves a %s listener: %s", name, options.Protocol, backendName, options.Protocol,
				strings.Join(albregistry.ServingProtocol(options.Protocol), ", "))
		}
		if provider == providers.ALB {
			streamALBs.Set(backendName)
			o := backend.ALBOptions
			if !albregistry.ServesProtocol(o.MechanismName, options.Protocol) {
				return fmt.Errorf("listener %q with protocol %q cannot serve alb backend %q: mechanism %q requires a %s listener",
					name, options.Protocol, backendName, o.MechanismName,
					strings.Join(albregistry.StreamProtocols(o.MechanismName), " or "))
			}
			sl := flowkey.StreamListener{
				TLS:           options.Protocol == listener.ProtocolTLS,
				ProxyProtocol: options.ProxyProtocol && options.Protocol != listener.ProtocolUDP,
			}
			if !o.HRW.KeySource.OnStream(sl) {
				return fmt.Errorf("listener %q with protocol %q cannot read alb backend %q's hrw.key %q: use "+
					"client_ip, sni on a tls listener, or proxy_tlv:<type> on a tcp or tls listener with proxy_protocol",
					name, options.Protocol, backendName, o.HRW.Key)
			}
			if err := stickyOnFlows(name, options.Protocol, backendName, o, func() bool {
				return o.Sticky.Table.KeySource.OnStream(sl)
			}); err != nil {
				return err
			}
			if _, err := o.LTSignalFor(options.Protocol); err != nil {
				return fmt.Errorf("listener %q: alb backend %q: %w", name, backendName, err)
			}
		}
		if members.Contains(backendName) {
			continue
		}
		served++
		if options.Protocol != listener.ProtocolTLS {
			if served > 1 {
				return fmt.Errorf("listener %q with protocol %q can map to only one backend",
					name, options.Protocol)
			}
			continue
		}
		hosts := backend.Hosts
		if len(hosts) == 0 {
			hosts = []string{""}
		}
		for _, h := range hosts {
			if err := table.Add(h, l4.Static(backendName)); err != nil {
				return fmt.Errorf("listener %q: backend %q: %w", name, backendName, err)
			}
		}
	}
	return nil
}

// nativeUserRouterTargets returns the backends that a native protocol listener reaches through
// an ALB, which therefore need no listener of their own
func nativeUserRouterTargets(c *config.Config, nativeListeners native.Registry) map[string]bool {
	targets := make(map[string]bool)
	if c == nil {
		return targets
	}
	for _, backend := range c.Backends {
		if backend == nil || backend.Provider != providers.ALB || backend.ALBOptions == nil {
			continue
		}
		if backend.ALBOptions.UserRouter == nil {
			if servesNativeListener(c, backend, nativeListeners) {
				for _, m := range backend.ALBOptions.Pool {
					targets[m.Name] = true
				}
			}
			continue
		}
		if len(nativeListeners.ForProvider(strings.ToLower(backend.ALBOptions.UserRouter.TargetProvider))) == 0 {
			continue
		}
		if name := backend.ALBOptions.UserRouter.DefaultBackend; name != "" {
			targets[name] = true
		}
		for _, mapping := range backend.ALBOptions.UserRouter.Users {
			if mapping != nil && mapping.ToBackend != "" {
				targets[mapping.ToBackend] = true
			}
		}
	}
	return targets
}

// servesNativeListener reports whether any listener the backend names speaks a native protocol
func servesNativeListener(c *config.Config, backend *bo.Options, nativeListeners native.Registry) bool {
	backend.NormalizeListenerNames()
	for _, name := range backend.ListenerNames {
		if lo := c.Listeners[name]; lo != nil && !strings.EqualFold(lo.Protocol, listener.ProtocolHTTP) &&
			nativeListeners.Get(strings.ToLower(lo.Protocol)) != nil {
			return true
		}
	}
	return false
}

// IPACLs validates and compiles every named access list. Files are read here,
// so a missing or unreadable file fails configuration loading.
func IPACLs(c *config.Config) error {
	if c == nil || len(c.IPACLs) == 0 {
		return nil
	}
	warnings, err := c.IPACLs.Validate()
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		addWarning(c, warning)
	}
	return nil
}

// bindListenerIPACL resolves a listener's ip_acl_name. peer is valid here.
// A native listener cannot take a client_ip list while proxy_protocol is on,
// because the socket peer and the address in the header are different.
// A client_ip list with proxy_protocol and no trusted_proxies is a warning:
// every peer's header is believed.
func bindListenerIPACL(c *config.Config, name string, options *listener.Options) error {
	if options == nil || options.IPACLName == "" {
		return nil
	}
	def := c.IPACLs[options.IPACLName]
	if def == nil || def.Compiled == nil {
		return fmt.Errorf("listener %q references undefined ip acl %q", name, options.IPACLName)
	}
	options.IPACL = def.Compiled
	if options.IPACL.Source() != ipacl.ClientIP {
		return nil
	}
	if providerregistry.NativeListeners().Get(options.Protocol) != nil && options.ProxyProtocol {
		return fmt.Errorf("listener %q with protocol %q cannot use ip acl %q with source client_ip "+
			"while proxy_protocol is enabled", name, options.Protocol, options.IPACLName)
	}
	if options.ProxyProtocol && len(options.TrustedProxies) == 0 {
		addWarning(c, fmt.Sprintf("listener %q uses ip acl %q with source client_ip, proxy_protocol, "+
			"and no trusted_proxies: every peer's header is believed", name, options.IPACLName))
	}
	return nil
}

func validateIPACLPlacements(c *config.Config) error {
	httpBackends := aclBackendReachability(c, true)
	streamMembers := aclBackendReachability(c, false)
	nativeListeners := providerregistry.NativeListeners()
	for _, backendName := range slices.Sorted(maps.Keys(c.Backends)) {
		backend := c.Backends[backendName]
		if backend == nil {
			continue
		}
		streamMember := streamMembers.Contains(backendName)
		if backend.IPACL != nil && streamMember {
			return streamMemberACLError(backendName, "", backend.IPACLName)
		}
		httpServed := httpBackends.Contains(backendName)
		if backend.IPACL != nil && backend.IPACL.Action() == ipacl.Drop && httpServed {
			return fmt.Errorf("backend %q uses ip acl %q with action drop, which is not valid "+
				"when the backend is served over http", backendName, backend.IPACLName)
		}
		for _, path := range backend.Paths {
			if path != nil && path.IPACL != nil && streamMember {
				return streamMemberACLError(backendName, path.Path, path.IPACLName)
			}
			if path == nil || path.IPACL == nil || path.IPACL.Action() != ipacl.Drop || !httpServed {
				continue
			}
			return fmt.Errorf("backend %q path %q uses ip acl %q with action drop, which is not valid "+
				"when the backend is served over http", backendName, path.Path, path.IPACLName)
		}
		if backend.IPACL != nil && servesNativeListener(c, backend, nativeListeners) {
			addWarning(c, fmt.Sprintf("backend %q has ip acl %q and is served by a native listener: "+
				"native sessions are judged by the listener acl only, and a ClickHouse native bridge "+
				"request to this backend has no client address and is denied",
				backendName, backend.IPACLName))
		}
	}
	return nil
}

func streamMemberACLError(backendName, path, aclName string) error {
	where := fmt.Sprintf("backend %q", backendName)
	if path != "" {
		where += fmt.Sprintf(" path %q", path)
	}
	return fmt.Errorf("%s uses ip acl %q but is a stream pool member or template: "+
		"stream member access lists are not supported; attach the list to the listener or front alb", where, aclName)
}

func aclBackendReachability(c *config.Config, wantHTTP bool) sets.Set[string] {
	// Follow dispatch edges once per backend. HTTP returns every reachable backend;
	// streams return only pool members and templates, which bypass admission.
	reached := sets.NewStringSet()
	members := sets.NewStringSet()
	queue := make([]string, 0, len(c.Backends))
	add := func(name string) {
		if name != "" && c.Backends[name] != nil && !reached.Contains(name) {
			reached.Set(name)
			queue = append(queue, name)
		}
	}
	for name, backend := range c.Backends {
		if backend == nil || backend.IsTemplate {
			continue
		}
		for _, listenerName := range backend.ListenerNames {
			lo := c.Listeners[listenerName]
			if lo != nil && (wantHTTP && (lo.Protocol == "" || lo.Protocol == listener.ProtocolHTTP) ||
				!wantHTTP && lo.IsStream()) {
				add(name)
				break
			}
		}
	}
	for i := 0; i < len(queue); i++ {
		backend := c.Backends[queue[i]]
		if o := backend.ALBOptions; o != nil {
			for _, member := range o.Pool {
				members.Set(member.Name)
				add(member.Name)
			}
			if o.Discovery != nil {
				members.Set(o.Discovery.TemplateBackend)
				add(o.Discovery.TemplateBackend)
			}
			if wantHTTP && o.UserRouter != nil {
				add(o.UserRouter.DefaultBackend)
				for _, mapping := range o.UserRouter.Users {
					if mapping != nil {
						add(mapping.ToBackend)
					}
				}
			}
		}
		if !wantHTTP {
			continue
		}
		if o := c.Rules[backend.RuleName]; o != nil {
			add(o.NextRoute)
			for _, ruleCase := range o.CaseOptions {
				if ruleCase != nil {
					add(ruleCase.NextRoute)
				}
			}
		}
		for _, path := range backend.Paths {
			if path == nil {
				continue
			}
			for _, mirror := range path.Mirrors {
				if mirror != nil {
					add(mirror.BackendName)
				}
			}
		}
	}
	if wantHTTP {
		return reached
	}
	return members
}

func addWarning(c *config.Config, warning string) {
	if slices.Contains(c.LoaderWarnings, warning) {
		return
	}
	c.LoaderWarnings = append(c.LoaderWarnings, warning)
}

// the port spaces a listener endpoint binds in
const (
	transportTCP = "tcp"
	transportUDP = "udp"
)

func reserveListenerPort(ports map[string]string, listenerName, transport, address string,
	port int,
) error {
	key := fmt.Sprintf("%s %s:%d", transport, address, port)
	if existing, ok := ports[key]; ok {
		return fmt.Errorf("listeners %q and %q both use %s", existing, listenerName, key)
	}
	ports[key] = listenerName
	return nil
}

func RoutesRulesAndPools(c *config.Config, clients backends.Backends) error {
	caches := make(cache.Lookup)
	for k := range c.Caches {
		caches[k] = nil
	}
	r := lm.NewRouter()
	mr := lm.NewRouter()
	mr.SetMatchingScheme(0) // metrics router is exact-match only
	listenerRouters := make(map[string]router.Router)
	for name, options := range c.Listeners {
		if options != nil && options.Active &&
			name != mgmt.ListenerNameMgmt && name != mgmt.ListenerNameMetrics {
			listenerRouters[name] = lm.NewRouter()
		}
	}
	if len(listenerRouters) == 0 {
		listenerRouters[listener.DefaultFrontendName] = r
	}
	tracers, err := tr.RegisterAll(c, true)
	if err != nil {
		return err
	}
	err = routing.RegisterProxyRoutesForListeners(c, clients, listenerRouters, mr, caches, tracers, true)
	if err != nil {
		return err
	}
	// these validations can't be performed until the router tree is constructed
	err = rule.ValidateOptions(clients, c.CompiledRewriters)
	if err != nil {
		return err
	}
	// which backends a listener reaches depends on the paths their clients register
	if err = stickyCookies(c, listenerVisible(c, clients)); err != nil {
		return err
	}
	if err = alb.ValidateClients(clients); err != nil {
		return err
	}
	for _, w := range alb.StepAlignmentWarnings(clients) {
		addWarning(c, w)
	}
	return nil
}
