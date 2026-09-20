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
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
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
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
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
	return Listeners(c)
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
	return nil
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
		c.Rules, c.RequestRewriters, c.Authenticators, c.TracingOptions); err != nil {
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
	return c.Backends.Validate()
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
	// the load balancers that serve a stream or native listener; every other one serves requests
	streamALBs := sets.NewStringSet()
	for backendName, backend := range c.Backends {
		if backend == nil || backend.IsTemplate {
			// templates are never routed, so they map to no listener
			continue
		}
		backend.NormalizeListenerNames()
		if adapter := nativeListeners.GetByProvider(strings.ToLower(backend.Provider)); adapter != nil {
			if err := adapter.ValidateBackend(backend); err != nil {
				return fmt.Errorf("%s backend %q: %w", adapter.Protocol(), backendName, err)
			}
			if nativeTargets[backendName] && len(backend.ListenerNames) == 0 {
				continue
			}
		}
		if backend.Provider == providers.ALB && backend.ALBOptions != nil &&
			backend.ALBOptions.UserRouter != nil {
			targetProvider := strings.ToLower(backend.ALBOptions.UserRouter.TargetProvider)
			if adapter := nativeListeners.GetByProvider(targetProvider); adapter != nil {
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
				targetProvider = nativeAdapter.BackendProvider()
			}
			if options.Protocol != listener.ProtocolHTTP && nativeAdapter != nil &&
				targetProvider != nativeAdapter.BackendProvider() {
				return fmt.Errorf("listener %q with protocol %q cannot map to backend %q with provider %q",
					name, options.Protocol, backendName, provider)
			}
			if adapter := nativeListeners.GetByProvider(targetProvider); options.Protocol == listener.ProtocolHTTP && adapter != nil && !adapter.SupportsHTTP() {
				return fmt.Errorf("backend %q with provider %q requires a listener with protocol %q",
					backendName, provider, adapter.Protocol())
			}
		}
		if options.ListenPort < 0 || options.TLSListenPort < 0 {
			return fmt.Errorf("listener %q has an invalid listen port", name)
		}
		if _, err := clientip.ParseTrusted(options.TrustedProxies); err != nil {
			return fmt.Errorf("listener %q: %w", name, err)
		}

		builtIn := name == listener.DefaultFrontendName ||
			name == mgmt.ListenerNameMgmt || name == mgmt.ListenerNameMetrics
		options.Active = name == mgmt.ListenerNameMgmt || name == mgmt.ListenerNameMetrics || mapped[name] > 0
		if !builtIn && mapped[name] == 0 {
			addWarning(c, fmt.Sprintf("listener %q is unused and will not be started", name))
		}

		if options.TLSListenPort > 0 && !tlsMapped[name] && !options.TLSRuntimeCerts {
			addWarning(c, fmt.Sprintf(
				"listener %q TLS port is disabled because no mapped backend provides a TLS certificate", name))
			options.TLSListenPort = 0
			options.ServeTLS = false
		} else {
			options.ServeTLS = options.TLSListenPort > 0 && (tlsMapped[name] || options.TLSRuntimeCerts)
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
			strings.ToLower(member.Provider) != adapter.BackendProvider() {
			return fmt.Errorf("listener %q with protocol %q requires every pool member of alb backend %q "+
				"to be a %s backend: %q is not", listenerName, protocol, backendName, adapter.BackendProvider(), m.Name)
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
	}
	return nil
}

// servesHTTPListener returns the name of an http listener the backend is mapped to, or "" when
// it has none. A backend that names no listener serves the default one, which is http.
func servesHTTPListener(c *config.Config, backend *bo.Options) string {
	if len(backend.ListenerNames) == 0 {
		return listener.DefaultFrontendName
	}
	for _, name := range backend.ListenerNames {
		lo := c.Listeners[name]
		if lo == nil || lo.Protocol == "" || lo.Protocol == listener.ProtocolHTTP {
			return name
		}
	}
	return ""
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
			if !o.HRW.KeySource.OnStream(ao.StreamListener{
				TLS:           options.Protocol == listener.ProtocolTLS,
				ProxyProtocol: options.ProxyProtocol && options.Protocol != listener.ProtocolUDP,
			}) {
				return fmt.Errorf("listener %q with protocol %q cannot read alb backend %q's hrw.key %q: use "+
					"client_ip, sni on a tls listener, or proxy_tlv:<type> on a tcp or tls listener with proxy_protocol",
					name, options.Protocol, backendName, o.HRW.Key)
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
		if nativeListeners.GetByProvider(strings.ToLower(backend.ALBOptions.UserRouter.TargetProvider)) == nil {
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
	return alb.ValidateClients(clients)
}
