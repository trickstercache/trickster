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
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
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

type nativeListenerAdapter struct{}

var _ native.Adapter = nativeListenerAdapter{}

// NativeListenerAdapter returns the MySQL implementation of the native
// listener extension point.
func NativeListenerAdapter() native.Adapter {
	return nativeListenerAdapter{}
}

func (nativeListenerAdapter) SupportsHTTP() bool { return false }

func (nativeListenerAdapter) Protocol() string { return listenerconfig.ProtocolMySQL }

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

func (nativeListenerAdapter) ValidateBackend(o *bo.Options) error {
	if o != nil && o.MySQL != nil {
		if err := o.MySQL.Validate(); err != nil {
			return err
		}
	}
	_, err := ProtocolConfigFromOptions(o)
	return err
}

func (nativeListenerAdapter) ValidateUserRouter(c *config.Config, name string, backend *bo.Options) error {
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
		if terminal.Provider != providers.MySQL {
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

func (nativeListenerAdapter) Describe(c *config.Config, listenerName string) (native.Descriptor, error) {
	protocolConfig, _, err := nativeProtocolConfig(c, listenerName)
	if err != nil {
		return native.Descriptor{}, err
	}
	if protocolConfig == nil {
		return native.Descriptor{}, errors.New("no mapped backend")
	}
	return native.Descriptor{RestartKey: protocolConfig.RestartKey}, nil
}

func (nativeListenerAdapter) Build(request native.BuildRequest) (listener.ProtocolServer, error) {
	protocolConfig, routed, err := nativeProtocolConfig(request.Config, request.ListenerName)
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
	resolver, targets := nativeRouteRuntime(request)
	if resolver == nil || len(targets) == 0 {
		return nil, errors.New("no usable native route targets")
	}
	return NewRoutedProtocolServer(*protocolConfig, resolver, targets)
}

func (nativeListenerAdapter) RouteResolver(request native.BuildRequest) backends.RouteResolver {
	resolver, _ := nativeRouteRuntime(request)
	return resolver
}

// nativeProtocolConfig returns the configuration of the single backend mapped
// to a MySQL listener; common listener validation guarantees uniqueness.
func nativeProtocolConfig(c *config.Config, listenerName string) (*ProtocolConfig, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	for backendName, o := range c.Backends {
		if !o.UsesListener(listenerName) {
			continue
		}
		if isNativeUserRouter(o) {
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
			protocolConfig.RestartKey = backendName + ":" + routedRestartKey(c, o, users)
			return &protocolConfig, true, nil
		}
		protocolConfig, err := ProtocolConfigFromOptions(o)
		if listenerOptions := c.Listeners[listenerName]; listenerOptions != nil {
			protocolConfig.ApplyListenerOptions(listenerOptions.MySQL)
		}
		protocolConfig.RestartKey = backendName + ":" + protocolConfig.RestartKey
		return &protocolConfig, false, err
	}
	return nil, false, nil
}

func isNativeUserRouter(o *bo.Options) bool {
	return o != nil && o.Provider == providers.ALB && o.ALBOptions != nil &&
		o.ALBOptions.MechanismName == names.MechanismUR && o.ALBOptions.UserRouter != nil &&
		o.ALBOptions.UserRouter.TargetProvider == providers.MySQL
}

type routeResolverProvider interface {
	RouteResolver() backends.RouteResolver
}

type nativeRouteProvider interface {
	MySQLRouteConfig() (ProtocolConfig, error)
}

func nativeRouteRuntime(request native.BuildRequest) (backends.RouteResolver, map[string]ProtocolConfig) {
	routerName := backendForListener(request.Config, request.ListenerName)
	routerOptions := request.Config.Backends[routerName]
	if !isNativeUserRouter(routerOptions) {
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
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

func routedRestartKey(c *config.Config, router *bo.Options, users map[string]string) string {
	var identity strings.Builder
	appendRestartIdentityField(&identity, userRouterRestartIdentity(router.ALBOptions.UserRouter))
	appendRestartIdentityField(&identity, strconv.FormatBool(router.RequireTLS))
	appendRestartIdentityField(&identity, tlsRestartIdentity(router))
	appendRestartIdentityField(&identity, credentialRestartIdentity(users))
	for _, name := range routeTargetNames(router) {
		if target := c.Backends[name]; target != nil {
			if protocolConfig, err := ProtocolConfigFromOptions(target); err == nil {
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
