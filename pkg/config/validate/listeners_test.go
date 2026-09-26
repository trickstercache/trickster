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

package validate

import (
	"slices"
	"strings"
	"testing"

	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	configtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	l4o "github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"
	tlsopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

func mysqlBackend(listenerName string) *bo.Options {
	backend := bo.New()
	backend.Provider = providers.MySQL
	backend.ListenerName = listenerName
	backend.OriginURL = "mysql://user:password@example.com/database"
	backend.AuthenticatorName = "mysql-auth"
	backend.AuthOptions = &autho.Options{
		Users: configtypes.EnvStringMap{"client": "password"},
	}
	return backend
}

func TestListenersBackendMappings(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		c := config.NewConfig()
		c.Backends = bo.Lookup{"test": bo.New()}
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if !c.Backends["test"].UsesListener(listener.DefaultFrontendName) {
			t.Errorf("backend did not use default frontend")
		}
	})

	for _, reserved := range []string{mgmt.ListenerNameMgmt, mgmt.ListenerNameMetrics} {
		t.Run("reserved_"+reserved, func(t *testing.T) {
			c := config.NewConfig()
			backend := bo.New()
			backend.ListenerName = reserved
			c.Backends = bo.Lookup{"test": backend}
			if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "reserved listener") {
				t.Fatalf("expected reserved listener error, got %v", err)
			}
		})
	}

	t.Run("undefined", func(t *testing.T) {
		c := config.NewConfig()
		backend := bo.New()
		backend.ListenerName = "missing"
		c.Backends = bo.Lookup{"test": backend}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "undefined listener") {
			t.Fatalf("expected undefined listener error, got %v", err)
		}
	})
}

func TestListenersWarningsAndProtocolValidation(t *testing.T) {
	t.Run("unused", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["unused"] = listener.New("unused")
		c.Listeners["unused"].ListenPort = 9000
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if c.Listeners["unused"].Active {
			t.Errorf("unused listener should not be active")
		}
		if !warningsContain(c.LoaderWarnings, `listener "unused" is unused`) {
			t.Errorf("missing unused listener warning: %v", c.LoaderWarnings)
		}
		// duplicate warnings should be ignored
		before := len(c.LoaderWarnings)
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if len(c.LoaderWarnings) != before {
			t.Fatalf("duplicate unused warning was appended: %v", c.LoaderWarnings)
		}
	})

	t.Run("tls_without_certificate", func(t *testing.T) {
		c := config.NewConfig()
		c.Backends = bo.Lookup{"test": bo.New()}
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if c.Listeners[listener.DefaultFrontendName].TLSListenPort != 0 {
			t.Errorf("TLS port should be disabled without a mapped certificate")
		}
		if !warningsContain(c.LoaderWarnings, "TLS port is disabled") {
			t.Errorf("missing TLS disable warning: %v", c.LoaderWarnings)
		}
	})

	t.Run("unsupported_protocol", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["custom"] = listener.New("custom")
		c.Listeners["custom"].Protocol = "unsupported"
		backend := bo.New()
		backend.ListenerName = "custom"
		c.Backends = bo.Lookup{"test": backend}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "unsupported protocol") {
			t.Fatalf("expected unsupported protocol error, got %v", err)
		}
	})

	t.Run("non_http_tls", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["custom"] = listener.New("custom")
		c.Listeners["custom"].Protocol = "unsupported"
		c.Listeners["custom"].TLSListenPort = 9443
		backend := bo.New()
		backend.ListenerName = "custom"
		c.Backends = bo.Lookup{"test": backend}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "cannot configure a TLS port") {
			t.Fatalf("expected non-HTTP TLS error, got %v", err)
		}
	})

	t.Run("non_http_multiple_backends", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["custom"] = listener.New("custom")
		c.Listeners["custom"].Protocol = "unsupported"
		first, second := bo.New(), bo.New()
		first.ListenerName, second.ListenerName = "custom", "custom"
		c.Backends = bo.Lookup{"first": first, "second": second}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "only one backend") {
			t.Fatalf("expected single-backend protocol error, got %v", err)
		}
	})

	t.Run("mysql_listener_single_backend", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["mysql1"] = listener.New("mysql1")
		c.Listeners["mysql1"].Protocol = listener.ProtocolMySQL
		c.Listeners["mysql1"].ListenPort = 8486
		backend := mysqlBackend("mysql1")
		c.Backends = bo.Lookup{"mysql1": backend}
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if !c.Listeners["mysql1"].Active {
			t.Fatal("mysql listener with one mapped backend should be active")
		}
	})

	t.Run("mysql_listener_multiple_backends", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["mysql1"] = listener.New("mysql1")
		c.Listeners["mysql1"].Protocol = listener.ProtocolMySQL
		c.Listeners["mysql1"].ListenPort = 8486
		first, second := mysqlBackend("mysql1"), mysqlBackend("mysql1")
		c.Backends = bo.Lookup{"first": first, "second": second}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "only one backend") {
			t.Fatalf("expected single-backend protocol error, got %v", err)
		}
	})

	t.Run("mysql_listener_wrong_provider", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["mysql1"] = listener.New("mysql1")
		c.Listeners["mysql1"].Protocol = listener.ProtocolMySQL
		c.Listeners["mysql1"].ListenPort = 8486
		backend := bo.New()
		backend.Provider = providers.Prometheus
		backend.ListenerName = "mysql1"
		c.Backends = bo.Lookup{"prom1": backend}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "cannot map to backend") {
			t.Fatalf("expected provider/protocol mismatch error, got %v", err)
		}
	})

	t.Run("mysql_listener_accepts_user_router_alb", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["mysql1"] = listener.New("mysql1")
		c.Listeners["mysql1"].Protocol = listener.ProtocolMySQL
		c.Listeners["mysql1"].ListenPort = 8486
		backend := bo.New()
		backend.Provider = providers.ALB
		backend.ListenerName = "mysql1"
		backend.ALBOptions = ao.New()
		backend.ALBOptions.MechanismName = "ur"
		backend.ALBOptions.UserRouter = &uropt.Options{
			TargetProvider: providers.MySQL,
			Users: uropt.UserMappingOptionsByUser{
				"client": {ToBackend: "mysql-a"},
			},
		}
		backend.AuthenticatorName = "mysql-listener-clients"
		backend.AuthOptions = &autho.Options{
			Users: configtypes.EnvStringMap{"client": "password"},
		}
		target := mysqlBackend("")
		c.Backends = bo.Lookup{"mysql-users": backend, "mysql-a": target}
		if err := Listeners(c); err != nil {
			t.Fatalf("expected MySQL User Router ALB to be accepted, got %v", err)
		}
		if !c.Listeners["mysql1"].Active {
			t.Fatal("MySQL User Router listener should be active")
		}
	})

	for _, tc := range []struct {
		name      string
		configure func(*bo.Options, *uropt.Options, bo.Lookup)
		want      string
	}{
		{
			name: "mysql_user_router_rejects_credential_remapping",
			configure: func(_ *bo.Options, o *uropt.Options, _ bo.Lookup) {
				o.Users["client"].ToUser = "origin-user"
			},
			want: "does not support to_user or to_credential",
		},
		{
			name: "mysql_user_router_rejects_nested_target",
			configure: func(_ *bo.Options, o *uropt.Options, lookup bo.Lookup) {
				nested := bo.New()
				nested.Provider = providers.ALB
				lookup["nested"] = nested
				o.Users["client"].ToBackend = "nested"
			},
			want: "must be a direct mysql backend",
		},
		{
			name: "mysql_user_router_requires_listener_authenticator",
			configure: func(backend *bo.Options, _ *uropt.Options, _ bo.Lookup) {
				backend.AuthenticatorName = ""
				backend.AuthOptions = nil
			},
			want: "requires an authenticator_name",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.NewConfig()
			c.Listeners["mysql1"] = listener.New("mysql1")
			c.Listeners["mysql1"].Protocol = listener.ProtocolMySQL
			c.Listeners["mysql1"].ListenPort = 8486
			routerBackend := bo.New()
			routerBackend.Provider = providers.ALB
			routerBackend.ListenerName = "mysql1"
			routerBackend.AuthenticatorName = "mysql-listener-clients"
			routerBackend.AuthOptions = &autho.Options{
				Users: configtypes.EnvStringMap{"client": "password"},
			}
			routerBackend.ALBOptions = ao.New()
			routerBackend.ALBOptions.MechanismName = "ur"
			routerOptions := &uropt.Options{
				TargetProvider: providers.MySQL,
				Users: uropt.UserMappingOptionsByUser{
					"client": {ToBackend: "mysql-a"},
				},
			}
			routerBackend.ALBOptions.UserRouter = routerOptions
			c.Backends = bo.Lookup{"mysql-users": routerBackend, "mysql-a": mysqlBackend("")}
			tc.configure(routerBackend, routerOptions, c.Backends)
			if err := Listeners(c); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Listeners() error = %v, want %q", err, tc.want)
			}
		})
	}

	t.Run("mysql_backend_on_http_listener", func(t *testing.T) {
		c := config.NewConfig()
		backend := mysqlBackend("")
		c.Backends = bo.Lookup{"mysql1": backend}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "requires a listener") {
			t.Fatalf("expected mysql-on-http error, got %v", err)
		}
	})

	t.Run("mysql_listener_tls_port", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["mysql1"] = listener.New("mysql1")
		c.Listeners["mysql1"].Protocol = listener.ProtocolMySQL
		c.Listeners["mysql1"].ListenPort = 8486
		c.Listeners["mysql1"].TLSListenPort = 9443
		backend := mysqlBackend("mysql1")
		c.Backends = bo.Lookup{"mysql1": backend}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "cannot configure a TLS port") {
			t.Fatalf("expected non-HTTP TLS error, got %v", err)
		}
	})
}

func TestListenersEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("nil_config", func(t *testing.T) {
		if err := Listeners(nil); err == nil || !strings.Contains(err.Error(), "no listeners") {
			t.Fatalf("Listeners(nil) = %v", err)
		}
	})

	t.Run("empty_listeners", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners = nil
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "no listeners") {
			t.Fatalf("Listeners(empty) = %v", err)
		}
	})

	t.Run("nil_listener_entry", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["broken"] = nil
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "invalid empty listener") {
			t.Fatalf("expected empty listener error, got %v", err)
		}
	})

	t.Run("invalid_port", func(t *testing.T) {
		c := config.NewConfig()
		c.Backends = bo.Lookup{"test": bo.New()}
		c.Listeners[listener.DefaultFrontendName].ListenPort = -5
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "invalid listen port") {
			t.Fatalf("expected invalid port error, got %v", err)
		}
	})

	t.Run("invalid_trusted_proxy", func(t *testing.T) {
		c := config.NewConfig()
		c.Backends = bo.Lookup{"test": bo.New()}
		c.Listeners[listener.DefaultFrontendName].TrustedProxies = []string{"10.0.0.0/8", "nope"}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "invalid trusted proxy") {
			t.Fatalf("expected invalid trusted proxy error, got %v", err)
		}
		c.Listeners[listener.DefaultFrontendName].TrustedProxies = []string{"10.0.0.0/8", "::1"}
		if err := Listeners(c); err != nil {
			t.Fatalf("expected valid trusted proxies, got %v", err)
		}
	})

	t.Run("port_conflict", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["custom"] = listener.New("custom")
		c.Listeners["custom"].ListenPort = c.Listeners[listener.DefaultFrontendName].ListenPort
		c.Listeners["custom"].ListenAddress = c.Listeners[listener.DefaultFrontendName].ListenAddress
		first, second := bo.New(), bo.New()
		first.ListenerName = listener.DefaultFrontendName
		second.ListenerName = "custom"
		c.Backends = bo.Lookup{"first": first, "second": second}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "both use") {
			t.Fatalf("expected port conflict error, got %v", err)
		}
	})

	t.Run("no_enabled_ports", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners[listener.DefaultFrontendName].ListenPort = 0
		c.Listeners[listener.DefaultFrontendName].TLSListenPort = 0
		c.Backends = bo.Lookup{"test": bo.New()}
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if !warningsContain(c.LoaderWarnings, "has no enabled ports") {
			t.Fatalf("missing no-ports warning: %v", c.LoaderWarnings)
		}
	})

	t.Run("skips_nil_backend", func(t *testing.T) {
		c := config.NewConfig()
		c.Backends = bo.Lookup{"gone": nil, "test": bo.New()}
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("tls_with_certificate", func(t *testing.T) {
		c := config.NewConfig()
		backend := bo.New()
		backend.TLS = &tlsopts.Options{ServeTLS: true}
		c.Backends = bo.Lookup{"test": backend}
		c.Listeners[listener.DefaultFrontendName].TLSListenPort = 9443
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if !c.Listeners[listener.DefaultFrontendName].ServeTLS {
			t.Fatal("listener should serve TLS when a mapped backend provides a cert")
		}
		if c.Listeners[listener.DefaultFrontendName].TLSListenPort != 9443 {
			t.Fatal("TLS port should remain enabled with a mapped certificate")
		}
	})
}

func warningsContain(warnings []string, substring string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substring) {
			return true
		}
	}
	return false
}

func TestClickHouseListenerBindings(t *testing.T) {
	for _, protocol := range []string{"http", "native"} {
		for _, names := range [][]string{{"default"}, {"ch"}, {"ch", "default"}} {
			t.Run(protocol+strings.Join(names, "/"), func(t *testing.T) {
				c := config.NewConfig()
				c.Listeners["ch"] = &listener.Options{Protocol: listener.ProtocolClickHouse, ListenPort: 9000}
				o := bo.New()
				o.Provider = providers.ClickHouse
				o.Protocol = protocol
				o.OriginURL = "http://localhost:9000"
				o.ListenerNames = names
				c.Backends = bo.Lookup{"click": o}
				if err := Listeners(c); err != nil {
					t.Fatal(err)
				}
				for _, name := range names {
					if !c.Listeners[name].Active {
						t.Fatalf("listener %s inactive", name)
					}
				}
				cloned := o.Clone()
				cloned.ListenerNames[0] = "other"
				if o.ListenerNames[0] == "other" {
					t.Fatal("listener names were not cloned")
				}
			})
		}
	}
	for _, names := range [][]string{{"missing"}, {"metrics"}, {""}} {
		c := config.NewConfig()
		c.Listeners["ch"] = &listener.Options{Protocol: listener.ProtocolClickHouse, ListenPort: 9000}
		o := bo.New()
		o.Provider = providers.ClickHouse
		o.ListenerNames = names
		c.Backends = bo.Lookup{"click": o}
		if err := Listeners(c); err == nil {
			t.Fatalf("accepted listener names %v", names)
		}
	}
}

func TestListenerNamesAreAdditiveAndProviderIndependent(t *testing.T) {
	for _, test := range []struct {
		name, provider, legacy string
		names, want            []string
		invalid                bool
	}{
		{"default", providers.Prometheus, "", nil, []string{"default"}, false},
		{"legacy", providers.Prometheus, "http2", nil, []string{"http2"}, false},
		{"additive", providers.Prometheus, "default", []string{"http2", "http2"}, []string{"default", "http2"}, false},
		{"overlap", providers.ReverseProxyCacheShort, "http2", []string{"http2", "default"}, []string{"default", "http2"}, false},
		{"mysql", providers.MySQL, "mysql1", []string{"mysql2", "mysql1"}, []string{"mysql1", "mysql2"}, false},
		{"clickhouse", providers.ClickHouse, "http2", []string{"ch", "default"}, []string{"ch", "default", "http2"}, false},
		{"prom cannot speak mysql", providers.Prometheus, "", []string{"default", "mysql1"}, nil, true},
		{"mysql cannot speak http", providers.MySQL, "default", []string{"mysql1"}, nil, true},
		{"clickhouse cannot speak mysql", providers.ClickHouse, "", []string{"ch", "mysql1"}, nil, true},
		{"invalid legacy is not ignored", providers.Prometheus, "missing", []string{"default"}, nil, true},
		{"empty name", providers.Prometheus, "", []string{""}, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := config.NewConfig()
			for i, name := range []string{"http2", "mysql1", "mysql2", "ch"} {
				lo := listener.New(name)
				lo.ListenPort = 9000 + i
				switch name {
				case "mysql1", "mysql2":
					lo.Protocol = listener.ProtocolMySQL
				case "ch":
					lo.Protocol = listener.ProtocolClickHouse
				}
				c.Listeners[name] = lo
			}
			o := bo.New()
			if test.provider == providers.MySQL {
				o = mysqlBackend("")
			}
			o.Provider = test.provider
			o.ListenerName = test.legacy
			o.ListenerNames = test.names
			c.Backends = bo.Lookup{"test": o}
			err := Listeners(c)
			if test.invalid {
				if err == nil {
					t.Fatal("accepted unsupported binding")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(o.ListenerNames, test.want) || o.ListenerName != test.legacy {
				t.Fatalf("bindings %v, legacy %q", o.ListenerNames, o.ListenerName)
			}
			if err := Listeners(c); err != nil || !slices.Equal(o.ListenerNames, test.want) {
				t.Fatalf("normalization is not idempotent: %v %v", o.ListenerNames, err)
			}
			for _, name := range test.want {
				if !c.Listeners[name].Active {
					t.Fatalf("inactive binding %s", name)
				}
			}
			o.ListenerName = "not-a-runtime-binding"
			if o.UsesListener(o.ListenerName) {
				t.Fatal("runtime reads legacy field")
			}
		})
	}
}

func TestListenersRuntimeCerts(t *testing.T) {
	c := config.NewConfig()
	c.Backends["default"].ListenerName = listener.DefaultFrontendName
	lo := c.Listeners[listener.DefaultFrontendName]
	lo.TLSListenPort = 8483
	lo.TLSRuntimeCerts = true
	if err := Listeners(c); err != nil {
		t.Fatal(err)
	}
	if !lo.ServeTLS || lo.TLSListenPort != 8483 {
		t.Errorf("runtime-cert listener serveTLS=%v port=%d; want TLS kept without a file cert",
			lo.ServeTLS, lo.TLSListenPort)
	}
	if warningsContain(c.LoaderWarnings, "TLS port is disabled") {
		t.Error("runtime-cert listener must not warn that its TLS port is disabled")
	}

	c = config.NewConfig()
	c.Backends["default"].ListenerName = listener.DefaultFrontendName
	c.Listeners["native"] = &listener.Options{Protocol: listener.ProtocolMySQL, TLSRuntimeCerts: true}
	if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "tls_runtime_certs") {
		t.Fatalf("error = %v; want a tls_runtime_certs protocol error", err)
	}
}

func streamBackend(listenerName, provider string, hosts ...string) *bo.Options {
	backend := bo.New()
	backend.Provider = provider
	backend.ListenerName = listenerName
	backend.OriginURL = "tcp://origin.example.com:9000"
	backend.Hosts = hosts
	return backend
}

func TestListenersStreamProtocols(t *testing.T) {
	newConfig := func(protocol string, backends bo.Lookup) *config.Config {
		c := config.NewConfig()
		c.Listeners["relay"] = listener.New("relay")
		c.Listeners["relay"].Protocol = protocol
		c.Listeners["relay"].ListenPort = 9000
		c.Backends = backends
		return c
	}
	behindProxy := func(c *config.Config) *config.Config {
		c.Listeners["relay"].ProxyProtocol = true
		return c
	}
	albBackend := func(mechanism string) *bo.Options {
		b := bo.New()
		b.Provider = providers.ALB
		b.ListenerName = "relay"
		b.ALBOptions = &ao.Options{MechanismName: mechanism, Pool: ao.PoolMemberList{{Name: "m1"}}}
		return b
	}
	albWith := func(mechanism string, set func(*ao.Options)) *bo.Options {
		b := albBackend(mechanism)
		set(b.ALBOptions)
		return b
	}
	keyed := func(kind ao.KeyKind, spelled string) func(*ao.Options) {
		return func(o *ao.Options) {
			o.HRW = ao.HRWOptions{Key: spelled, KeySource: ao.KeySource{Kind: kind, Name: "X"}}
		}
	}
	signal := func(s string) func(*ao.Options) { return func(o *ao.Options) { o.LT.Signal = s } }
	member := bo.New()
	member.OriginURL = "tcp://member.example.com:9000"
	cases := []struct {
		name string
		conf *config.Config
		want string
	}{
		{"tcp_single_rp", newConfig(listener.ProtocolTCP, bo.Lookup{"db": streamBackend("relay", providers.ReverseProxyShort)}), ""},
		{"udp_single_proxy", newConfig(listener.ProtocolUDP, bo.Lookup{"dns": streamBackend("relay", providers.Proxy)}), ""},
		{"tcp_alb_rr", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albBackend("rr"), "m1": member}), ""},
		{"tls_sni_hosts", newConfig(listener.ProtocolTLS, bo.Lookup{
			"a": streamBackend("relay", providers.ReverseProxy, "a.example.com", "*.a.example.com"),
			"b": streamBackend("relay", providers.ReverseProxyShort, "b.example.com"),
			"c": streamBackend("relay", providers.ReverseProxyShort),
		}), ""},
		{"tcp_two_backends", newConfig(listener.ProtocolTCP, bo.Lookup{
			"a": streamBackend("relay", providers.ReverseProxyShort), "b": streamBackend("relay", providers.ReverseProxyShort),
		}), "can map to only one backend"},
		{"tls_two_catch_alls", newConfig(listener.ProtocolTLS, bo.Lookup{
			"a": streamBackend("relay", providers.ReverseProxyShort), "b": streamBackend("relay", providers.ReverseProxyShort),
		}), "already has a catch-all"},
		{"tls_duplicate_host", newConfig(listener.ProtocolTLS, bo.Lookup{
			"a": streamBackend("relay", providers.ReverseProxyShort, "x.example.com"),
			"b": streamBackend("relay", providers.ReverseProxyShort, "X.example.com."),
		}), "already routed"},
		{"wrong_provider", newConfig(listener.ProtocolTCP, bo.Lookup{"p": streamBackend("relay", providers.Prometheus)}), "cannot map to backend"},
		{"tcp_alb_round_robin", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albBackend("round_robin"), "m1": member}), ""},
		{"udp_alb_rr", newConfig(listener.ProtocolUDP, bo.Lookup{"pool": albBackend("rr"), "m1": member}), ""},
		// the mechanisms a stream listener may use come from the registry, and the error names them
		{"alb_fanout", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albBackend("fr"), "m1": member}),
			"a mechanism that serves a tcp listener: hrw, lc, lt, p2c, race, rr"},
		{"udp_alb_fanout", newConfig(listener.ProtocolUDP, bo.Lookup{"pool": albBackend("fr"), "m1": member}),
			"a mechanism that serves a udp listener: hrw, lc, lt, mirror, p2c, rr"},
		// a mechanism that commits a flow to several members serves only the protocols it can
		{"tcp_alb_race", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albBackend("race"), "m1": member}), ""},
		{"tls_alb_connect_race", newConfig(listener.ProtocolTLS, bo.Lookup{"pool": albBackend("connect_race"), "m1": member}), ""},
		{"udp_alb_mirror", newConfig(listener.ProtocolUDP, bo.Lookup{"pool": albBackend("mirror"), "m1": member}), ""},
		{"udp_alb_race", newConfig(listener.ProtocolUDP, bo.Lookup{"pool": albBackend("race"), "m1": member}),
			"mechanism \"race\" requires a tcp or tls listener"},
		{"tcp_alb_mirror", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albBackend("udp_mirror"), "m1": member}),
			"mechanism \"udp_mirror\" requires a udp listener"},
		{"alb_router", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albBackend("ur"), "m1": member}), "requires alb backend"},
		{"tcp_alb_p2c", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albBackend("p2c"), "m1": member}), ""},
		{"udp_alb_lc", newConfig(listener.ProtocolUDP, bo.Lookup{"pool": albBackend("lc"), "m1": member}), ""},
		{"tls_alb_lt", newConfig(listener.ProtocolTLS, bo.Lookup{"pool": albBackend("lt"), "m1": member}), ""},
		{"tcp_alb_hrw", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albBackend("hrw"), "m1": member}), ""},
		// a key must be something the listener can read: the client address on any of them,
		// the server name on tls alone, and nothing of a request
		{"tls_hrw_sni", newConfig(listener.ProtocolTLS, bo.Lookup{"pool": albWith("hrw", keyed(ao.KeySNI, "sni")), "m1": member}), ""},
		{"tcp_hrw_sni", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albWith("hrw", keyed(ao.KeySNI, "sni")), "m1": member}),
			"cannot read alb backend \"pool\"'s hrw.key \"sni\""},
		{"udp_hrw_sni", newConfig(listener.ProtocolUDP, bo.Lookup{"pool": albWith("hrw", keyed(ao.KeySNI, "sni")), "m1": member}), "cannot read"},
		{"tcp_hrw_header", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albWith("hrw", keyed(ao.KeyHeader, "header:X")), "m1": member}),
			"use client_ip, sni on a tls listener, or proxy_tlv:<type>"},
		// a PROXY protocol TLV is there to read only where the header is accepted, which udp never does
		{"tcp_hrw_tlv", behindProxy(newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albWith("hrw", keyed(ao.KeyProxyTLV, "proxy_tlv:0xEA")), "m1": member})), ""},
		{"tls_hrw_tlv", behindProxy(newConfig(listener.ProtocolTLS, bo.Lookup{"pool": albWith("hrw", keyed(ao.KeyProxyTLV, "proxy_tlv:0xEA")), "m1": member})), ""},
		{"tcp_hrw_tlv_no_proxy_protocol", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albWith("hrw", keyed(ao.KeyProxyTLV, "proxy_tlv:0xEA")), "m1": member}),
			"cannot read alb backend \"pool\"'s hrw.key \"proxy_tlv:0xEA\""},
		{"udp_hrw_tlv", behindProxy(newConfig(listener.ProtocolUDP, bo.Lookup{"pool": albWith("hrw", keyed(ao.KeyProxyTLV, "proxy_tlv:0xEA")), "m1": member})),
			"cannot read"},
		// and a latency signal must be one the protocol has
		{"tcp_lt_first_byte", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albWith("lt", signal("first_byte")), "m1": member}), ""},
		{"udp_lt_first_reply", newConfig(listener.ProtocolUDP, bo.Lookup{"pool": albWith("lt", signal("first_reply")), "m1": member}), ""},
		{"udp_lt_connect", newConfig(listener.ProtocolUDP, bo.Lookup{"pool": albWith("lt", signal("connect")), "m1": member}),
			"\"connect\" on a udp listener (use first_reply)"},
		{"tcp_lt_first_write", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albWith("lt", signal("first_write")), "m1": member}),
			"use connect or first_byte"},
		{"tcp_alb_stream_block", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albWith("p2c", func(o *ao.Options) {
			o.Stream = &ao.StreamOptions{ConnectRetries: 2, PassiveHealth: &ao.PassiveHealthOptions{Failures: 3}}
		}), "m1": member}), ""},
		{"alb_unknown_mechanism", newConfig(listener.ProtocolTCP, bo.Lookup{"pool": albBackend("nope"), "m1": member}), "requires alb backend"},
		{"unsupported_still_refused", newConfig("sctp", bo.Lookup{"p": streamBackend("relay", providers.ReverseProxyShort)}), "unsupported protocol"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Listeners(tc.conf)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if tc.want == "" && !tc.conf.Listeners["relay"].Active {
				t.Error("a mapped stream listener must be active")
			}
		})
	}

	t.Run("stream_block_on_http_listener", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["web"] = listener.New("web")
		c.Listeners["web"].ListenPort = 9000
		c.Listeners["web"].Stream = l4o.New()
		c.Backends = bo.Lookup{"b": streamBackend("web", providers.ReverseProxyShort)}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "stream options") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("negative_stream_timeout", func(t *testing.T) {
		c := newConfig(listener.ProtocolTCP, bo.Lookup{"db": streamBackend("relay", providers.ReverseProxyShort)})
		c.Listeners["relay"].Stream = &l4o.Options{IdleTimeout: -1}
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "zero or positive") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("stream_tls_port_refused", func(t *testing.T) {
		c := newConfig(listener.ProtocolTLS, bo.Lookup{"db": streamBackend("relay", providers.ReverseProxyShort)})
		c.Listeners["relay"].TLSListenPort = 9443
		if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "cannot configure a TLS port") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestListenersReservePortsByTransport(t *testing.T) {
	// a tcp and a udp listener may share a port number, since they bind in different port
	// spaces; two in one space may not, and an HTTP/3 endpoint binds UDP
	newConfig := func() *config.Config {
		c := config.NewConfig()
		for _, name := range []string{"tcp", "udp"} {
			c.Listeners[name] = listener.New(name)
			c.Listeners[name].Protocol = name
			c.Listeners[name].ListenPort = 5353
		}
		c.Backends = bo.Lookup{
			"a": streamBackend("tcp", providers.ReverseProxyShort),
			"b": streamBackend("udp", providers.ReverseProxyShort),
		}
		return c
	}
	if err := Listeners(newConfig()); err != nil {
		t.Fatalf("tcp and udp on one port number: %v", err)
	}
	c := newConfig()
	c.Listeners["udp"].Protocol = listener.ProtocolTLS
	if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "both use tcp :5353") {
		t.Fatalf("tcp and tls on one port: %v", err)
	}
	c = newConfig()
	c.Listeners["udp"].Protocol = listener.ProtocolHTTP
	c.Listeners["udp"].ListenPort = 0
	c.Listeners["udp"].TLSListenPort = 5443
	c.Listeners["udp"].TLSRuntimeCerts = true
	c.Listeners["udp"].HTTP3 = &listener.HTTP3Options{Enabled: true, ListenPort: 5353}
	c.Listeners["tcp"].Protocol = listener.ProtocolUDP
	if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "both use udp :5353") {
		t.Fatalf("HTTP/3 and udp on one port: %v", err)
	}
}

// a load balancer that serves no stream listener is held to what a request can offer
func TestRequestALBsRefuseStreamOnlySettings(t *testing.T) {
	alb := func(set func(*ao.Options), listeners ...string) *config.Config {
		c := config.NewConfig()
		c.Listeners["relay"] = listener.New("relay")
		c.Listeners["relay"].Protocol = listener.ProtocolTLS
		c.Listeners["web"] = listener.New("web")
		b := bo.New()
		b.Provider = providers.ALB
		b.ListenerNames = listeners
		b.ALBOptions = &ao.Options{MechanismName: "hrw"}
		set(b.ALBOptions)
		notALB := bo.New()
		c.Backends = bo.Lookup{"lb": b, "origin": notALB, "unset": nil}
		return c
	}
	for name, test := range map[string]struct {
		set  func(*ao.Options)
		want string
	}{
		"plain":        {func(*ao.Options) {}, ""},
		"header key":   {func(o *ao.Options) { o.HRW.KeySource = ao.KeySource{Kind: ao.KeyHeader, Name: "X"} }, ""},
		"first_write":  {func(o *ao.Options) { o.LT.Signal = ao.LTSignalFirstWrite }, ""},
		"stream block": {func(o *ao.Options) { o.Stream = &ao.StreamOptions{} }, "'stream' options apply only"},
		"sni key": {func(o *ao.Options) { o.HRW = ao.HRWOptions{Key: "sni", KeySource: ao.KeySource{Kind: ao.KeySNI}} },
			"cannot be read from a request"},
		"tlv key": {func(o *ao.Options) {
			o.HRW = ao.HRWOptions{Key: "proxy_tlv:5", KeySource: ao.KeySource{Kind: ao.KeyProxyTLV, TLV: 5}}
		},
			"cannot be read from a request"},
		"connect signal": {func(o *ao.Options) { o.LT.Signal = ao.LTSignalConnect }, "on a http listener (use first_write)"},
	} {
		err := requestALBs(alb(test.set), sets.NewStringSet())
		switch {
		case test.want == "" && err != nil:
			t.Errorf("%s: unexpected error: %v", name, err)
		case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
			t.Errorf("%s: error = %v, want %q", name, err, test.want)
		}
		// the same load balancer on a stream listener alone is somebody else's to judge
		if err := requestALBs(alb(test.set, "relay"), sets.New([]string{"lb"})); err != nil {
			t.Errorf("%s: a stream load balancer was held to request rules: %v", name, err)
		}
		// one that serves requests as well is held to both, but for a stream block, which the
		// stream listener it also serves has a use for
		err = requestALBs(alb(test.set, "relay", "web"), sets.New([]string{"lb"}))
		switch {
		case (test.want == "" || name == "stream block") && err != nil:
			t.Errorf("%s: on both planes: unexpected error: %v", name, err)
		case test.want != "" && name != "stream block" && (err == nil || !strings.Contains(err.Error(), test.want)):
			t.Errorf("%s: on both planes: error = %v, want %q", name, err, test.want)
		}
	}
}

// a mechanism that commits a flow to several members needs a stream listener of its own
func TestSpreadMechanismsNeedAStreamListener(t *testing.T) {
	alb := func(mechanism string, pool ...string) *bo.Options {
		b := bo.New()
		b.Provider = providers.ALB
		b.ALBOptions = &ao.Options{MechanismName: mechanism, Pool: ao.Members(pool...)}
		return b
	}
	c := config.NewConfig()
	c.Listeners["relay"] = listener.New("relay")
	c.Listeners["relay"].Protocol = listener.ProtocolTCP
	c.Backends = bo.Lookup{"racer": alb("race", "origin"), "origin": bo.New()}
	if err := requestALBs(c, sets.NewStringSet()); err == nil ||
		!strings.Contains(err.Error(), "mechanism \"race\" requires a tcp or tls listener") {
		t.Errorf("a race on a request listener: %v", err)
	}
	c.Backends["racer"].ListenerNames = []string{"relay"}
	if err := requestALBs(c, sets.New([]string{"racer"})); err != nil {
		t.Errorf("a race on a stream listener: %v", err)
	}
	c.Backends["racer"].ListenerNames = []string{"relay", "default"}
	if err := requestALBs(c, sets.New([]string{"racer"})); err == nil ||
		!strings.Contains(err.Error(), "cannot serve http listener \"default\"") {
		t.Errorf("a race on a stream and a request listener: %v", err)
	}
	c.Backends["racer"].ListenerNames = []string{"relay"}
	// and cannot be reached through another load balancer, which has one member to hand it
	c.Backends["outer"] = alb("rr", "racer")
	if err := requestALBs(c, sets.New([]string{"racer", "outer"})); err == nil ||
		!strings.Contains(err.Error(), "cannot be a member of another alb's pool") {
		t.Errorf("a race as a pool member: %v", err)
	}
}

const pgTestListener = "pg1"

func postgresListenerConfig(backend *bo.Options) *config.Config {
	c := config.NewConfig()
	c.Listeners[pgTestListener] = listener.New(pgTestListener)
	c.Listeners[pgTestListener].Protocol = listener.ProtocolPostgres
	c.Listeners[pgTestListener].ListenPort = 8488
	backend.ListenerName = pgTestListener
	c.Backends = bo.Lookup{"backend1": backend}
	return c
}

func TestPostgresListenerServesEveryPostgresProvider(t *testing.T) {
	for _, provider := range []string{providers.Postgres, providers.TimescaleDB} {
		backend := bo.New()
		backend.Provider = provider
		backend.OriginURL = "postgres://user:password@example.com/database"
		c := postgresListenerConfig(backend)
		if err := Listeners(c); err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if !c.Listeners[pgTestListener].Active || c.Listeners[pgTestListener].Postgres == nil {
			t.Fatalf("%s: the listener should be active with default postgres limits", provider)
		}
	}

	if err := Listeners(postgresListenerConfig(mysqlBackend(pgTestListener))); err == nil ||
		!strings.Contains(err.Error(), "cannot map to backend") {
		t.Fatalf("expected a provider/protocol mismatch, got %v", err)
	}

	invalid := bo.New()
	invalid.Provider = providers.TimescaleDB
	invalid.OriginURL = "http://example.com"
	if err := Listeners(postgresListenerConfig(invalid)); err == nil ||
		!strings.Contains(err.Error(), "unsupported postgres origin scheme") {
		t.Fatalf("expected the origin scheme to be rejected, got %v", err)
	}

	onHTTP := bo.New()
	onHTTP.Provider = providers.TimescaleDB
	onHTTP.OriginURL = "postgres://user:password@example.com/database"
	c := config.NewConfig()
	c.Backends = bo.Lookup{"backend1": onHTTP}
	if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "requires a listener with protocol") {
		t.Fatalf("expected a postgres backend on an HTTP listener to be rejected, got %v", err)
	}

	router := bo.New()
	router.Provider = providers.ALB
	router.ALBOptions = ao.New()
	router.ALBOptions.MechanismName = "ur"
	router.ALBOptions.UserRouter = &uropt.Options{TargetProvider: providers.Postgres, DefaultBackend: "backend2"}
	if err := Listeners(postgresListenerConfig(router)); err == nil ||
		!strings.Contains(err.Error(), "requires an authenticator") {
		t.Fatalf("expected a user router without listener-facing users to be rejected, got %v", err)
	}
	router.AuthenticatorName = "pg-clients"
	router.AuthOptions = &autho.Options{Users: configtypes.EnvStringMap{"client": "password"}}
	routed := postgresListenerConfig(router)
	if err := Listeners(routed); err == nil || !strings.Contains(err.Error(), "references missing backend") {
		t.Fatalf("expected the missing target to be rejected, got %v", err)
	}
	// a target reached only through the router needs no listener of its own
	target := bo.New()
	target.Provider = providers.TimescaleDB
	target.OriginURL = "postgres://user:password@example.com/database"
	routed.Backends["backend2"] = target
	if err := Listeners(routed); err != nil {
		t.Fatalf("expected a user router over a postgres target to validate: %v", err)
	}
	if len(target.ListenerNames) != 0 {
		t.Fatalf("the target must not be mapped to the HTTP listener: %v", target.ListenerNames)
	}
}
