/*
 * Copyright 2026 The Trickster Authors
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

package mysql

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	albregistry "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/registry"
	albtypes "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	checksum "github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
)

type nativeListenerAdapter struct{ engines map[string]Engine }

var _ native.Adapter = nativeListenerAdapter{}

// NativeListenerAdapter returns the MySQL implementation of the native
// listener extension point.
func NativeListenerAdapter() native.Adapter {
	return &nativeListenerAdapter{}
}

// NewNativeListenerAdapter serves MySQL and the supplied compatible engines.
func NewNativeListenerAdapter(engines ...Engine) native.Adapter {
	a := &nativeListenerAdapter{engines: make(map[string]Engine, len(engines))}
	for _, e := range engines {
		if e != nil && e.Name() != providers.MySQL {
			a.engines[e.Name()] = e
		}
	}
	return a
}

func (a nativeListenerAdapter) SupportsHTTP(provider string) bool {
	e := a.engines[provider]
	return e != nil && e.SupportsHTTP()
}

func (nativeListenerAdapter) Protocol() string { return listenerconfig.ProtocolMySQL }

func (a nativeListenerAdapter) ServesProvider(provider string) bool {
	return provider == providers.MySQL || a.engines[provider] != nil
}

func (a nativeListenerAdapter) Providers() []string {
	names := []string{providers.MySQL}
	for name := range a.engines {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (a nativeListenerAdapter) configFromOptions(o *bo.Options) (ProtocolConfig, error) {
	if o == nil {
		return ProtocolConfig{}, errors.New("nil MySQL backend options")
	}
	return ProtocolConfigForEngine(o, a.engines[strings.ToLower(o.Provider)])
}

func (nativeListenerAdapter) Configured(o *listenerconfig.Options) bool {
	return o != nil && o.MySQL != nil
}

func (nativeListenerAdapter) ValidateListener(o *listenerconfig.Options) error {
	if o == nil {
		return errors.New("nil MySQL listener options")
	}
	if o.MySQL == nil {
		o.MySQL = mo.NewListener()
	}
	return o.MySQL.Validate()
}

func (a nativeListenerAdapter) ValidateBackend(o *bo.Options) error {
	if o != nil && o.MySQL != nil {
		if err := o.MySQL.Validate(); err != nil {
			return err
		}
	}
	if o != nil && a.SupportsHTTP(strings.ToLower(o.Provider)) {
		if o.HasHTTPListener {
			u, err := url.Parse(o.OriginURL)
			if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return errors.New("an HTTP listener requires an http(s) origin_url; use a mysql listener for a MySQL-only backend")
			}
		}
		if !slices.Contains(o.NativeListenerProtocols, listenerconfig.ProtocolMySQL) {
			return nil
		}
	}
	_, err := a.configFromOptions(o)
	return err
}

func (a nativeListenerAdapter) ValidateUserRouter(c *config.Config, name string, backend *bo.Options) error {
	if backend == nil || backend.ALBOptions == nil || backend.ALBOptions.UserRouter == nil {
		return fmt.Errorf("mysql user router %q has no user-router configuration", name)
	}
	if backend.ALBOptions.MechanismName != names.MechanismUR {
		return fmt.Errorf("mysql user router %q requires mechanism %q", name, names.MechanismUR)
	}
	if _, err := DownstreamCredentialsFromOptions(backend); err != nil {
		return fmt.Errorf("mysql user router %q: %w", name, err)
	}
	o := backend.ALBOptions.UserRouter
	if o.DefaultBackend == "" && len(o.Users) == 0 {
		return fmt.Errorf("mysql user router %q has no routes", name)
	}
	validateTarget := func(target string) error {
		if target == "" {
			return fmt.Errorf("mysql user router %q has an empty terminal route", name)
		}
		terminal := c.Backends[target]
		if terminal == nil {
			return fmt.Errorf("mysql user router %q references missing backend %q", name, target)
		}
		if !a.ServesProvider(strings.ToLower(terminal.Provider)) {
			return fmt.Errorf("mysql user router %q target %q must be a direct mysql backend", name, target)
		}
		return nil
	}
	if o.DefaultBackend != "" {
		if err := validateTarget(o.DefaultBackend); err != nil {
			return err
		}
	}
	for username, mapping := range o.Users {
		if mapping == nil {
			return fmt.Errorf("mysql user router %q has an empty mapping for user %q", name, username)
		}
		if mapping.ToUser != "" || mapping.ToCredential != "" {
			return fmt.Errorf("mysql user router %q does not support to_user or to_credential", name)
		}
		if err := validateTarget(mapping.ToBackend); err != nil {
			return err
		}
	}
	return nil
}

func (a nativeListenerAdapter) ValidateBalancer(c *config.Config, name string, backend *bo.Options) error {
	if !a.isNativeBalancer(c, backend) {
		return fmt.Errorf("mysql load balancer %q requires a mechanism that balances sessions "+
			"over a pool of direct mysql backends", name)
	}
	if _, err := DownstreamCredentialsFromOptions(backend); err != nil {
		return fmt.Errorf("mysql load balancer %q: %w", name, err)
	}
	return nil
}

func (a nativeListenerAdapter) Describe(c *config.Config, listenerName string) (native.Descriptor, error) {
	protocolConfig, _, err := a.nativeProtocolConfig(c, listenerName)
	if err != nil {
		return native.Descriptor{}, err
	}
	if protocolConfig == nil {
		return native.Descriptor{}, errors.New("no mapped backend")
	}
	return native.Descriptor{RestartKey: protocolConfig.RestartKey}, nil
}

func (a nativeListenerAdapter) Build(request native.BuildRequest) (listener.ProtocolServer, error) {
	protocolConfig, routed, err := a.nativeProtocolConfig(request.Config, request.ListenerName)
	if err != nil {
		return nil, err
	}
	if protocolConfig == nil {
		return nil, errors.New("no mapped backend")
	}
	if backendOptions := request.Config.Backends[protocolConfig.BackendName]; backendOptions != nil {
		protocolConfig.Tracer = request.Tracers[backendOptions.TracingConfigName]
	}
	if client := request.BackendClients.Get(protocolConfig.BackendName); client != nil {
		protocolConfig.CacheProvider = client
	}
	protocolConfig.InboundTLS, err = request.Config.TLSCertConfigForListener(request.ListenerName)
	if err != nil {
		return nil, err
	}
	installVitessLogger()
	if !routed {
		return NewProtocolServer(*protocolConfig)
	}
	resolver, targets := a.nativeRouteRuntime(request)
	if resolver == nil || len(targets) == 0 {
		return nil, errors.New("no usable native route targets")
	}
	return NewRoutedProtocolServer(*protocolConfig, resolver, targets)
}

func (a nativeListenerAdapter) RouteResolver(request native.BuildRequest) backends.RouteResolver {
	resolver, _ := a.nativeRouteRuntime(request)
	return resolver
}

// nativeProtocolConfig returns the configuration of the single backend mapped
// to a MySQL listener; common listener validation guarantees uniqueness.
func (a nativeListenerAdapter) nativeProtocolConfig(c *config.Config, listenerName string) (*ProtocolConfig, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	for backendName, o := range c.Backends {
		if !o.UsesListener(listenerName) {
			continue
		}
		if a.isNativeRouter(c, o) {
			users, err := DownstreamCredentialsFromOptions(o)
			if err != nil {
				return nil, false, err
			}
			protocolConfig := ProtocolConfig{
				BackendName: backendName, RequireSecureTransport: o.RequireTLS,
				DownstreamUsers: users,
			}
			if listenerOptions := c.Listeners[listenerName]; listenerOptions != nil {
				protocolConfig.ApplyListenerOptions(listenerOptions.MySQL)
			}
			protocolConfig.RestartKey = backendName + ":" + a.routedRestartKey(c, o, users)
			return &protocolConfig, true, nil
		}
		protocolConfig, err := a.configFromOptions(o)
		if listenerOptions := c.Listeners[listenerName]; listenerOptions != nil {
			protocolConfig.ApplyListenerOptions(listenerOptions.MySQL)
		}
		protocolConfig.RestartKey = backendName + ":" + protocolConfig.RestartKey
		return &protocolConfig, false, err
	}
	return nil, false, nil
}

// isNativeRouter reports whether o is an ALB that commits each MySQL session to a backend:
// by the user it authenticated as, or by a strategy that balances sessions over a pool
func (a nativeListenerAdapter) isNativeRouter(c *config.Config, o *bo.Options) bool {
	return a.isNativeUserRouter(o) || a.isNativeBalancer(c, o)
}

func (a nativeListenerAdapter) isNativeUserRouter(o *bo.Options) bool {
	return o != nil && o.Provider == providers.ALB && o.ALBOptions != nil &&
		o.ALBOptions.MechanismName == names.MechanismUR && o.ALBOptions.UserRouter != nil &&
		a.ServesProvider(strings.ToLower(o.ALBOptions.UserRouter.TargetProvider))
}

func (a nativeListenerAdapter) isNativeBalancer(c *config.Config, o *bo.Options) bool {
	if c == nil || o == nil || o.Provider != providers.ALB || o.ALBOptions == nil ||
		o.ALBOptions.UserRouter != nil || len(o.ALBOptions.Pool) == 0 ||
		o.ALBOptions.MechanismName == names.MechanismUR ||
		!albregistry.Supports(o.ALBOptions.MechanismName, albtypes.PlaneNative) {
		return false
	}
	for _, m := range o.ALBOptions.Pool {
		if member := c.Backends[m.Name]; member == nil || !a.ServesProvider(strings.ToLower(member.Provider)) {
			return false
		}
	}
	return true
}

type routeResolverProvider interface {
	RouteResolver() backends.RouteResolver
}

type nativeRouteProvider interface {
	MySQLRouteConfig() (ProtocolConfig, error)
}

func (a nativeListenerAdapter) nativeRouteRuntime(request native.BuildRequest) (backends.RouteResolver, map[string]ProtocolConfig) {
	routerName := backendForListener(request.Config, request.ListenerName)
	routerOptions := request.Config.Backends[routerName]
	if !a.isNativeRouter(request.Config, routerOptions) {
		return nil, nil
	}
	client := request.BackendClients.Get(routerName)
	provider, ok := client.(routeResolverProvider)
	if !ok || provider.RouteResolver() == nil {
		return nil, nil
	}
	names := routeTargetNames(routerOptions)
	targets := make(map[string]ProtocolConfig, len(names))
	for _, name := range names {
		targetClient := request.BackendClients.Get(name)
		target, ok := targetClient.(nativeRouteProvider)
		if !ok {
			return nil, nil
		}
		protocolConfig, err := target.MySQLRouteConfig()
		if err != nil {
			return nil, nil
		}
		protocolConfig.ApplyListenerOptions(request.Listener.MySQL)
		if backendOptions := request.Config.Backends[name]; backendOptions != nil {
			protocolConfig.Tracer = request.Tracers[backendOptions.TracingConfigName]
		}
		protocolConfig.CacheProvider = targetClient
		targets[name] = protocolConfig
	}
	return provider.RouteResolver(), targets
}

func backendForListener(c *config.Config, listenerName string) string {
	if c == nil {
		return ""
	}
	for name, o := range c.Backends {
		if o.UsesListener(listenerName) {
			return name
		}
	}
	return ""
}

func routeTargetNames(o *bo.Options) []string {
	if o == nil || o.ALBOptions == nil {
		return nil
	}
	if o.ALBOptions.UserRouter == nil {
		// a load balancer's sessions may be committed to any member of its pool
		result := o.ALBOptions.Pool.Names()
		slices.Sort(result)
		return slices.Compact(result)
	}
	seen := make(map[string]struct{})
	if name := o.ALBOptions.UserRouter.DefaultBackend; name != "" {
		seen[name] = struct{}{}
	}
	for _, mapping := range o.ALBOptions.UserRouter.Users {
		if mapping != nil && mapping.ToBackend != "" {
			seen[mapping.ToBackend] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

func (a nativeListenerAdapter) routedRestartKey(c *config.Config, router *bo.Options, users map[string]string) string {
	var identity strings.Builder
	appendRestartIdentityField(&identity, userRouterRestartIdentity(router.ALBOptions.UserRouter))
	appendRestartIdentityField(&identity, strconv.FormatBool(router.RequireTLS))
	appendRestartIdentityField(&identity, tlsRestartIdentity(router))
	appendRestartIdentityField(&identity, credentialRestartIdentity(users))
	for _, name := range routeTargetNames(router) {
		if target := c.Backends[name]; target != nil {
			if protocolConfig, err := a.configFromOptions(target); err == nil {
				appendRestartIdentityField(&identity, name)
				appendRestartIdentityField(&identity, protocolConfig.RestartKey)
			}
		}
	}
	return checksum.Checksum(identity.String())
}

func userRouterRestartIdentity(o *uropt.Options) string {
	if o == nil {
		return ""
	}
	var identity strings.Builder
	appendRestartIdentityField(&identity, o.DefaultBackend)
	appendRestartIdentityField(&identity, strconv.Itoa(o.NoRouteStatusCode))
	appendRestartIdentityField(&identity, o.TargetProvider)
	usernames := make([]string, 0, len(o.Users))
	for username := range o.Users {
		usernames = append(usernames, username)
	}
	slices.Sort(usernames)
	for _, username := range usernames {
		appendRestartIdentityField(&identity, username)
		mapping := o.Users[username]
		if mapping == nil {
			appendRestartIdentityField(&identity, "")
			continue
		}
		appendRestartIdentityField(&identity, mapping.ToBackend)
		appendRestartIdentityField(&identity, mapping.ToUser)
		appendRestartIdentityField(&identity, string(mapping.ToCredential))
	}
	return identity.String()
}
