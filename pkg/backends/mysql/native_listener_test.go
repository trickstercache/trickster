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
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	configtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
)

func TestNativeListenerAdapterContract(t *testing.T) {
	adapter := NativeListenerAdapter()
	if adapter.Protocol() != listenerconfig.ProtocolMySQL {
		t.Fatalf("Protocol() = %q", adapter.Protocol())
	}
	if adapter.Configured(nil) || adapter.Configured(listenerconfig.New("mysql")) {
		t.Fatal("Configured() accepted a listener without MySQL options")
	}
	configured := listenerconfig.New("mysql")
	configured.MySQL = mo.NewListener()
	if !adapter.Configured(configured) {
		t.Fatal("Configured() rejected MySQL listener options")
	}
	if err := adapter.ValidateListener(nil); err == nil {
		t.Fatal("ValidateListener(nil) succeeded")
	}
	defaults := listenerconfig.New("mysql")
	if err := adapter.ValidateListener(defaults); err != nil || defaults.MySQL == nil {
		t.Fatalf("ValidateListener(defaults) = %v, options = %+v", err, defaults.MySQL)
	}
	configured.MySQL.MaxQuerySizeBytes = 0
	if err := adapter.ValidateListener(configured); err == nil {
		t.Fatal("ValidateListener() accepted an invalid query limit")
	}

	valid := validBackendOptions()
	valid.MySQL = mo.New()
	if err := adapter.ValidateBackend(valid); err != nil {
		t.Fatalf("ValidateBackend(valid) = %v", err)
	}
	if err := adapter.ValidateBackend(nil); err == nil {
		t.Fatal("ValidateBackend(nil) succeeded")
	}
}

func TestNativeListenerAdapterValidatesUserRouter(t *testing.T) {
	adapter := nativeListenerAdapter{}
	validConfig := routedRestartTestConfig()
	validRouter := validConfig.Backends["mysql-users"]
	if err := adapter.ValidateUserRouter(validConfig, "mysql-users", validRouter); err != nil {
		t.Fatalf("ValidateUserRouter(valid) = %v", err)
	}

	tests := []struct {
		name   string
		change func(*config.Config) *bo.Options
		want   string
	}{
		{
			name: "missing options",
			change: func(*config.Config) *bo.Options {
				return nil
			},
			want: "no user-router configuration",
		},
		{
			name: "wrong mechanism",
			change: func(c *config.Config) *bo.Options {
				router := c.Backends["mysql-users"]
				router.ALBOptions.MechanismName = "rr"
				return router
			},
			want: "requires mechanism",
		},
		{
			name: "invalid listener credentials",
			change: func(c *config.Config) *bo.Options {
				router := c.Backends["mysql-users"]
				router.AuthenticatorName = ""
				return router
			},
			want: "requires an authenticator_name",
		},
		{
			name: "no routes",
			change: func(c *config.Config) *bo.Options {
				router := c.Backends["mysql-users"]
				router.ALBOptions.UserRouter.DefaultBackend = ""
				router.ALBOptions.UserRouter.Users = nil
				return router
			},
			want: "has no routes",
		},
		{
			name: "missing default target",
			change: func(c *config.Config) *bo.Options {
				router := c.Backends["mysql-users"]
				router.ALBOptions.UserRouter.DefaultBackend = "missing"
				return router
			},
			want: "references missing backend",
		},
		{
			name: "empty mapped target",
			change: func(c *config.Config) *bo.Options {
				router := c.Backends["mysql-users"]
				router.ALBOptions.UserRouter.DefaultBackend = ""
				router.ALBOptions.UserRouter.Users["alice"].ToBackend = ""
				return router
			},
			want: "empty terminal route",
		},
		{
			name: "non mysql target",
			change: func(c *config.Config) *bo.Options {
				router := c.Backends["mysql-users"]
				c.Backends["mysql-a"].Provider = providers.Proxy
				return router
			},
			want: "must be a direct mysql backend",
		},
		{
			name: "nil mapping",
			change: func(c *config.Config) *bo.Options {
				router := c.Backends["mysql-users"]
				router.ALBOptions.UserRouter.Users["alice"] = nil
				return router
			},
			want: "empty mapping",
		},
		{
			name: "credential remapping",
			change: func(c *config.Config) *bo.Options {
				router := c.Backends["mysql-users"]
				router.ALBOptions.UserRouter.Users["alice"].ToUser = "origin-user"
				return router
			},
			want: "does not support to_user or to_credential",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configured := validConfig.Clone()
			router := test.change(configured)
			err := adapter.ValidateUserRouter(configured, "mysql-users", router)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateUserRouter() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestNativeListenerAdapterDescribeAndBuild(t *testing.T) {
	adapter := nativeListenerAdapter{}
	if _, err := adapter.Describe(nil, "missing"); err == nil {
		t.Fatal("Describe(nil) succeeded")
	}

	direct := directNativeTestConfig()
	descriptor, err := adapter.Describe(direct, "mysql-direct")
	if err != nil || descriptor.RestartKey == "" {
		t.Fatalf("Describe(direct) = %+v, %v", descriptor, err)
	}
	server, err := adapter.Build(nativeTestBuildRequest(direct, "mysql-direct", nil))
	if err != nil {
		t.Fatalf("Build(direct) = %v", err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	invalid := direct.Clone()
	invalid.Backends["mysql-direct"].OriginURL = "://bad"
	if _, err := adapter.Describe(invalid, "mysql-direct"); err == nil {
		t.Fatal("Describe(invalid) succeeded")
	}
	if _, err := adapter.Build(nativeTestBuildRequest(invalid, "mysql-direct", nil)); err == nil {
		t.Fatal("Build(invalid) succeeded")
	}
	if _, err := adapter.Build(native.BuildRequest{}); err == nil {
		t.Fatal("Build(empty) succeeded")
	}
}

func TestNativeRouteRuntimeAndRoutedBuild(t *testing.T) {
	adapter := nativeListenerAdapter{}
	configured := routedRestartTestConfig()
	request := nativeTestBuildRequest(configured, "mysql-users", nil)
	if resolver := adapter.RouteResolver(request); resolver != nil {
		t.Fatal("RouteResolver() found a resolver without runtime clients")
	}
	if _, err := adapter.Build(request); err == nil {
		t.Fatal("Build(routed without clients) succeeded")
	}

	resolver := staticRouteResolver{}
	routerClient := &nativeRuntimeBackend{resolver: resolver}
	targetClient := &nativeRuntimeBackend{protocolConfig: ProtocolConfig{
		BackendName: "mysql-a",
	}}
	request.BackendClients = backends.Backends{
		"mysql-users": routerClient,
		"mysql-a":     targetClient,
	}
	if got := adapter.RouteResolver(request); got == nil {
		t.Fatal("RouteResolver() did not expose the runtime resolver")
	}
	server, err := adapter.Build(request)
	if err != nil {
		t.Fatalf("Build(routed) = %v", err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	targetClient.protocolErr = errors.New("target config failed")
	if resolver, targets := nativeRouteRuntime(request); resolver != nil || targets != nil {
		t.Fatalf("nativeRouteRuntime(target error) = %v, %v", resolver, targets)
	}
	targetClient.protocolErr = nil
	request.BackendClients["mysql-a"] = &nonNativeBackend{}
	if resolver, targets := nativeRouteRuntime(request); resolver != nil || targets != nil {
		t.Fatalf("nativeRouteRuntime(empty target) = %v, %v", resolver, targets)
	}
}

func TestNativeListenerHelperEdgeCases(t *testing.T) {
	adapter := nativeListenerAdapter{}
	if configured, routed, err := nativeProtocolConfig(config.NewConfig(), "missing"); configured != nil || routed || err != nil {
		t.Fatalf("nativeProtocolConfig(missing) = %+v, %t, %v", configured, routed, err)
	}
	routedConfig := routedRestartTestConfig()
	routedConfig.Backends["mysql-users"].AuthenticatorName = ""
	if _, _, err := nativeProtocolConfig(routedConfig, "mysql-users"); err == nil {
		t.Fatal("nativeProtocolConfig() accepted invalid routed credentials")
	}

	direct := directNativeTestConfig()
	request := nativeTestBuildRequest(direct, "mysql-direct", nil)
	if resolver, targets := nativeRouteRuntime(request); resolver != nil || targets != nil {
		t.Fatalf("nativeRouteRuntime(direct) = %v, %v", resolver, targets)
	}
	if got := backendForListener(nil, "mysql"); got != "" {
		t.Fatalf("backendForListener(nil) = %q", got)
	}
	if got := backendForListener(direct, "missing"); got != "" {
		t.Fatalf("backendForListener(missing) = %q", got)
	}
	if got := routeTargetNames(nil); got != nil {
		t.Fatalf("routeTargetNames(nil) = %v", got)
	}
	if got := userRouterRestartIdentity(nil); got != "" {
		t.Fatalf("userRouterRestartIdentity(nil) = %q", got)
	}
	options := &uropt.Options{Users: uropt.UserMappingOptionsByUser{"nil": nil}}
	if got := userRouterRestartIdentity(options); got == "" {
		t.Fatal("nil route mapping produced an empty restart identity")
	}

	invalidTLS := direct.Clone()
	missingTLSDir := t.TempDir()
	invalidTLS.Backends["mysql-direct"].TLS.ServeTLS = true
	invalidTLS.Backends["mysql-direct"].TLS.FullChainCertPath = filepath.Join(missingTLSDir, "cert.pem")
	invalidTLS.Backends["mysql-direct"].TLS.PrivateKeyPath = filepath.Join(missingTLSDir, "key.pem")
	if _, err := adapter.Build(nativeTestBuildRequest(invalidTLS, "mysql-direct", nil)); err == nil {
		t.Fatal("Build() accepted unreadable TLS files")
	}
}

type staticRouteResolver struct{}

func (staticRouteResolver) ResolveRoute(backends.RouteInput) (backends.RouteDecision, bool) {
	return backends.RouteDecision{}, false
}

type nativeRuntimeBackend struct {
	backends.Backend
	resolver       backends.RouteResolver
	protocolConfig ProtocolConfig
	protocolErr    error
}

func (b *nativeRuntimeBackend) RouteResolver() backends.RouteResolver {
	return b.resolver
}

func (b *nativeRuntimeBackend) MySQLRouteConfig() (ProtocolConfig, error) {
	return b.protocolConfig, b.protocolErr
}

type nonNativeBackend struct {
	backends.Backend
}

func nativeTestBuildRequest(c *config.Config, listenerName string,
	clients backends.Backends,
) native.BuildRequest {
	return native.BuildRequest{
		Config: c, ListenerName: listenerName, Listener: c.Listeners[listenerName],
		BackendClients: clients,
	}
}

func directNativeTestConfig() *config.Config {
	c := config.NewConfig()
	c.Listeners["mysql-direct"] = listenerconfig.New("mysql-direct")
	c.Listeners["mysql-direct"].Protocol = listenerconfig.ProtocolMySQL
	backend := validBackendOptions()
	backend.Name = "mysql-direct"
	backend.Provider = providers.MySQL
	backend.ListenerNames = []string{"mysql-direct"}
	c.Backends = bo.Lookup{"mysql-direct": backend}
	return c
}

func TestRoutedRestartKeyStableAcrossConfigClone(t *testing.T) {
	configured := routedRestartTestConfig()
	first, routed, err := nativeProtocolConfig(configured, "mysql-users")
	if err != nil {
		t.Fatal(err)
	}
	if !routed || first == nil || first.RestartKey == "" {
		t.Fatalf("nativeProtocolConfig() = %+v, %t, want routed config", first, routed)
	}

	cloned := configured.Clone()
	second, routed, err := nativeProtocolConfig(cloned, "mysql-users")
	if err != nil {
		t.Fatal(err)
	}
	if !routed || second == nil {
		t.Fatal("cloned config did not produce a routed protocol config")
	}
	if first.RestartKey != second.RestartKey {
		t.Fatalf("equivalent routed configs produced different restart keys: %q != %q",
			first.RestartKey, second.RestartKey)
	}
}

func TestRoutedRestartKeyIncludesRuntimeConfiguration(t *testing.T) {
	configured := routedRestartTestConfig()
	baseline, _, err := nativeProtocolConfig(configured, "mysql-users")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		change func(*config.Config)
	}{
		{
			name: "downstream credentials",
			change: func(c *config.Config) {
				c.Backends["mysql-users"].AuthOptions.Users["alice"] = "rotated-password"
			},
		},
		{
			name: "route mapping",
			change: func(c *config.Config) {
				c.Backends["mysql-users"].ALBOptions.UserRouter.Users["alice"].ToBackend = "mysql-b"
			},
		},
		{
			name: "target protocol settings",
			change: func(c *config.Config) {
				c.Backends["mysql-a"].Timeout++
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := configured.Clone()
			test.change(changed)
			got, _, configErr := nativeProtocolConfig(changed, "mysql-users")
			if configErr != nil {
				t.Fatal(configErr)
			}
			if got.RestartKey == baseline.RestartKey {
				t.Fatalf("%s did not alter routed restart key", test.name)
			}
		})
	}
}

func routedRestartTestConfig() *config.Config {
	c := config.NewConfig()
	c.Listeners["mysql-users"] = listenerconfig.New("mysql-users")
	c.Listeners["mysql-users"].Protocol = listenerconfig.ProtocolMySQL

	router := bo.New()
	router.Name = "mysql-users"
	router.Provider = providers.ALB
	router.ListenerNames = []string{"mysql-users"}
	router.ALBOptions = ao.New()
	router.ALBOptions.MechanismName = names.MechanismUR
	router.ALBOptions.UserRouter = &uropt.Options{
		DefaultBackend: "mysql-a",
		TargetProvider: providers.MySQL,
		Users: uropt.UserMappingOptionsByUser{
			"alice": {ToBackend: "mysql-a"},
		},
	}
	router.AuthenticatorName = "mysql-listener-clients"
	router.AuthOptions = &autho.Options{
		Users: configtypes.EnvStringMap{"alice": "alice-password"},
	}

	newTarget := func(name string) *bo.Options {
		target := bo.New()
		target.Name = name
		target.Provider = providers.MySQL
		target.OriginURL = "mysql://origin:password@127.0.0.1/database"
		target.AuthenticatorName = "mysql-clients"
		target.AuthOptions = &autho.Options{
			Users: configtypes.EnvStringMap{"alice": "alice-password"},
		}
		return target
	}
	c.Backends = bo.Lookup{
		"mysql-users": router,
		"mysql-a":     newTarget("mysql-a"),
		"mysql-b":     newTarget("mysql-b"),
	}
	return c
}

func TestNativeListenerBindingsDoNotChangeRestartIdentity(t *testing.T) {
	c := directNativeTestConfig()
	a := nativeListenerAdapter{}
	first, err := a.Describe(c, "mysql-direct")
	if err != nil {
		t.Fatal(err)
	}
	o := c.Backends["mysql-direct"]
	o.ListenerNames = append(o.ListenerNames, "mysql-other")
	c.Listeners["mysql-other"] = c.Listeners["mysql-direct"].Clone()
	for _, name := range o.ListenerNames {
		got, err := a.Describe(c, name)
		if err != nil || got.RestartKey != first.RestartKey {
			t.Fatalf("binding %s changed identity: %v", name, err)
		}
	}
	delete(c.Backends, "mysql-direct")
	c.Backends["replacement"] = o
	got, err := a.Describe(c, "mysql-direct")
	if err != nil || got.RestartKey == first.RestartKey {
		t.Fatal("backend replacement did not change listener identity")
	}
}
