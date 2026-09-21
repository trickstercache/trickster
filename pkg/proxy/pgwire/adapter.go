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
package pgwire

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	checksum "github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	pgo "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"
)

var errNoMappedBackend = errors.New("no mapped backend")

type nativeListenerAdapter struct {
	engines Engines
}

var _ native.Adapter = (*nativeListenerAdapter)(nil)

// NewNativeListenerAdapter returns the postgres protocol's one adapter, which
// serves every provider that has an engine in the registry.
func NewNativeListenerAdapter(engines Engines) native.Adapter {
	// a pointer, because the struct holds a map and adapters are compared as interfaces
	return &nativeListenerAdapter{engines: engines}
}

func (nativeListenerAdapter) SupportsHTTP() bool { return false }

func (nativeListenerAdapter) Protocol() string { return listenerconfig.ProtocolPostgres }

func (a nativeListenerAdapter) ServesProvider(provider string) bool {
	return a.engines.Get(provider) != nil
}

func (a nativeListenerAdapter) Providers() []string { return a.engines.Names() }

func (nativeListenerAdapter) Configured(o *listenerconfig.Options) bool {
	return o != nil && o.Postgres != nil
}

func (nativeListenerAdapter) ValidateListener(o *listenerconfig.Options) error {
	if o == nil {
		return errors.New("nil postgres listener options")
	}
	if o.Postgres == nil {
		o.Postgres = pgo.NewListener()
	}
	return o.Postgres.Validate()
}

func (a nativeListenerAdapter) ValidateBackend(o *bo.Options) error {
	if o != nil && o.Postgres != nil {
		if err := o.Postgres.Validate(); err != nil {
			return err
		}
	}
	_, err := a.configFromOptions(o)
	return err
}

func (a nativeListenerAdapter) ValidateUserRouter(c *config.Config, name string, backend *bo.Options) error {
	if backend == nil || backend.ALBOptions == nil || backend.ALBOptions.UserRouter == nil {
		return fmt.Errorf("postgres user router %q has no user-router configuration", name)
	}
	if backend.ALBOptions.MechanismName != names.MechanismUR {
		return fmt.Errorf("postgres user router %q requires mechanism %q", name, names.MechanismUR)
	}
	if users, err := downstreamUsers(backend); err != nil {
		return fmt.Errorf("postgres user router %q: %w", name, err)
	} else if users == nil {
		return fmt.Errorf("postgres user router %q requires an authenticator with users", name)
	}
	o := backend.ALBOptions.UserRouter
	if o.DefaultBackend == "" && len(o.Users) == 0 {
		return fmt.Errorf("postgres user router %q has no routes", name)
	}
	for username, mapping := range o.Users {
		if mapping == nil {
			return fmt.Errorf("postgres user router %q has an empty mapping for user %q", name, username)
		}
		if mapping.ToUser != "" || mapping.ToCredential != "" {
			return fmt.Errorf("postgres user router %q does not support to_user or to_credential", name)
		}
		if mapping.ToBackend == "" {
			return fmt.Errorf("postgres user router %q has an empty terminal route", name)
		}
	}
	for _, target := range routeTargetNames(backend) {
		terminal := c.Backends[target]
		if terminal == nil {
			return fmt.Errorf("postgres user router %q references missing backend %q", name, target)
		}
		if !a.ServesProvider(strings.ToLower(terminal.Provider)) {
			return fmt.Errorf("postgres user router %q target %q must be a direct postgres backend", name, target)
		}
		if targetConfig, err := a.configFromOptions(terminal); err != nil {
			return fmt.Errorf("postgres user router %q target %q: %w", name, target, err)
		} else if targetConfig.Upstream.User == "" {
			return fmt.Errorf("postgres user router %q target %q: origin URL must include a username", name, target)
		}
	}
	return nil
}

func (a nativeListenerAdapter) Describe(c *config.Config, listenerName string) (native.Descriptor, error) {
	protocolConfig, _, err := a.listenerConfig(c, listenerName)
	if err != nil {
		return native.Descriptor{}, err
	}
	return native.Descriptor{RestartKey: protocolConfig.RestartKey}, nil
}

func (a nativeListenerAdapter) Build(request native.BuildRequest) (listener.ProtocolServer, error) {
	protocolConfig, routed, err := a.listenerConfig(request.Config, request.ListenerName)
	if err != nil {
		return nil, err
	}
	protocolConfig.InboundTLS, err = request.Config.TLSCertConfigForListener(request.ListenerName)
	if err != nil {
		return nil, err
	}
	if !routed {
		if client := request.BackendClients.Get(protocolConfig.BackendName); client != nil {
			protocolConfig.CacheProvider = client
		}
		return NewServer(*protocolConfig)
	}
	resolver, targets := a.routeRuntime(request)
	if resolver == nil || len(targets) == 0 {
		return nil, errors.New("no usable postgres route targets")
	}
	return NewRoutedServer(*protocolConfig, resolver, targets)
}

func (a nativeListenerAdapter) RouteResolver(request native.BuildRequest) backends.RouteResolver {
	resolver, _ := a.routeRuntime(request)
	return resolver
}

type routeResolverProvider interface {
	RouteResolver() backends.RouteResolver
}

func (a nativeListenerAdapter) isUserRouter(o *bo.Options) bool {
	return o != nil && o.Provider == providers.ALB && o.ALBOptions != nil &&
		o.ALBOptions.MechanismName == names.MechanismUR && o.ALBOptions.UserRouter != nil &&
		a.ServesProvider(strings.ToLower(o.ALBOptions.UserRouter.TargetProvider))
}

func (a nativeListenerAdapter) routeRuntime(request native.BuildRequest) (backends.RouteResolver, map[string]Config) {
	if request.Config == nil {
		return nil, nil
	}
	var router *bo.Options
	var routerName string
	for name, o := range request.Config.Backends {
		if o.UsesListener(request.ListenerName) {
			router, routerName = o, name
			break
		}
	}
	if !a.isUserRouter(router) {
		return nil, nil
	}
	provider, ok := request.BackendClients.Get(routerName).(routeResolverProvider)
	if !ok || provider.RouteResolver() == nil {
		return nil, nil
	}
	targets := make(map[string]Config)
	for _, name := range routeTargetNames(router) {
		target, err := a.configFromOptions(request.Config.Backends[name])
		if err != nil {
			return nil, nil
		}
		target.BackendName = name
		if request.Listener != nil {
			target.ApplyListenerOptions(request.Listener.Postgres)
		}
		if client := request.BackendClients.Get(name); client != nil {
			target.CacheProvider = client
		}
		targets[name] = target
	}
	return provider.RouteResolver(), targets
}

func routeTargetNames(o *bo.Options) []string {
	if o == nil || o.ALBOptions == nil || o.ALBOptions.UserRouter == nil {
		return nil
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
	return slices.Sorted(maps.Keys(seen))
}

func (a nativeListenerAdapter) routerConfig(c *config.Config, name string, o *bo.Options) (*Config, error) {
	users, err := downstreamUsers(o)
	if err != nil {
		return nil, err
	}
	routerConfig := Config{BackendName: name, Provider: providers.ALB, Users: users, RequireSecureTransport: o.RequireTLS}
	// every route, credential and target setting restarts the listener, since sessions are pinned to a target
	var identity strings.Builder
	router := o.ALBOptions.UserRouter
	appendRestartIdentityField(&identity, router.DefaultBackend)
	for _, username := range slices.Sorted(maps.Keys(router.Users)) {
		appendRestartIdentityField(&identity, username)
		if mapping := router.Users[username]; mapping != nil {
			appendRestartIdentityField(&identity, mapping.ToBackend)
		}
	}
	appendRestartIdentityField(&identity, strconv.FormatBool(o.RequireTLS))
	appendRestartIdentityField(&identity, tlsRestartIdentity(o))
	appendRestartIdentityField(&identity, credentialRestartIdentity(users))
	for _, target := range routeTargetNames(o) {
		if targetConfig, err := a.configFromOptions(c.Backends[target]); err == nil {
			appendRestartIdentityField(&identity, target)
			appendRestartIdentityField(&identity, targetConfig.RestartKey)
		}
	}
	routerConfig.RestartKey = checksum.Checksum(identity.String())
	return &routerConfig, nil
}

func (a nativeListenerAdapter) configFromOptions(o *bo.Options) (Config, error) {
	if o == nil {
		return Config{}, errors.New("nil postgres backend options")
	}
	return ConfigFromOptions(o, a.engines.Get(strings.ToLower(o.Provider)))
}

func (a nativeListenerAdapter) listenerConfig(c *config.Config, listenerName string) (*Config, bool, error) {
	// returns the configuration of the single backend mapped to a postgres listener, and whether
	// it is a user router; common listener validation guarantees uniqueness.
	if c == nil {
		return nil, false, errNoMappedBackend
	}
	for backendName, o := range c.Backends {
		if !o.UsesListener(listenerName) {
			continue
		}
		var (
			protocolConfig *Config
			err            error
		)
		routed := a.isUserRouter(o)
		if routed {
			protocolConfig, err = a.routerConfig(c, backendName, o)
		} else {
			var direct Config
			direct, err = a.configFromOptions(o)
			protocolConfig = &direct
		}
		if err != nil {
			return nil, false, err
		}
		protocolConfig.BackendName = backendName
		if listenerOptions := c.Listeners[listenerName]; listenerOptions != nil {
			protocolConfig.ApplyListenerOptions(listenerOptions.Postgres)
		} else if routed {
			protocolConfig.ApplyListenerOptions(nil)
		}
		protocolConfig.RestartKey = backendName + ":" + protocolConfig.RestartKey
		return protocolConfig, routed, nil
	}
	return nil, false, errNoMappedBackend
}
