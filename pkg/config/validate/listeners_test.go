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
	tlsopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
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
