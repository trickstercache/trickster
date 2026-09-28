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

	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"
)

func TestGreptimeDBMultipleNativeProtocols(t *testing.T) {
	for _, names := range [][]string{
		{"default"}, {"mysql"}, {"postgres"}, {"mysql", "postgres"}, {"default", "mysql", "postgres"},
	} {
		t.Run(strings.Join(names, "+"), func(t *testing.T) {
			c := config.NewConfig()
			c.Listeners["mysql"] = &listener.Options{Protocol: listener.ProtocolMySQL, ListenPort: 8490}
			c.Listeners["postgres"] = &listener.Options{Protocol: listener.ProtocolPostgres, ListenPort: 8491}
			o := bo.New()
			o.Provider, o.OriginURL, o.ListenerNames = "greptimedb", "http://db.example:4000", names
			o.MySQL = mo.New()
			o.MySQL.UpstreamURL = "mysql://reader:dev-password@db.example/public"
			o.Postgres = po.New()
			o.Postgres.UpstreamURL = "postgres://reader:dev-password@db.example/public"
			o.AuthenticatorName = "native-clients"
			o.AuthOptions = &autho.Options{Users: types.EnvStringMap{"client": "dev-password"}}
			c.Backends = bo.Lookup{"greptime": o}
			if err := Listeners(c); err != nil {
				t.Fatal(err)
			}
			for _, protocol := range []string{listener.ProtocolMySQL, listener.ProtocolPostgres} {
				if slices.Contains(o.NativeListenerProtocols, protocol) != slices.Contains(names, protocol) {
					t.Fatalf("protocols %v for listeners %v", o.NativeListenerProtocols, names)
				}
			}
			o.ListenerNames = []string{"default"}
			o.MySQL.UpstreamURL = "invalid-unused-native-url"
			if err := Listeners(c); err != nil {
				t.Fatal(err)
			}
			if len(o.NativeListenerProtocols) != 0 {
				t.Fatal("stale native mapping after revalidation")
			}
		})
	}
}

func TestGreptimeDBNativeBalancerProtocols(t *testing.T) {
	for _, protocol := range []string{listener.ProtocolMySQL, listener.ProtocolPostgres} {
		c := replicaConfig("rr")
		c.Listeners["mysql1"].Protocol = protocol
		for _, name := range []string{"replica-a", "replica-b"} {
			o := c.Backends[name]
			o.Provider, o.OriginURL = "greptimedb", "http://db.example:4000"
			o.MySQL = mo.New()
			o.MySQL.UpstreamURL = "mysql://reader:dev-password@db.example/public"
			o.Postgres = po.New()
			o.Postgres.UpstreamURL = "postgres://reader:dev-password@db.example/public"
		}
		if protocol == listener.ProtocolPostgres {
			if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "postgres session balancing is not supported") {
				t.Fatalf("unsupported postgres session balancing: %v", err)
			}
			continue
		}
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"replica-a", "replica-b"} {
			o := c.Backends[name]
			if len(o.ListenerNames) != 0 || !slices.Equal(o.NativeListenerProtocols, []string{protocol}) {
				t.Fatalf("wrong terminal mappings: %+v", o.NativeListenerProtocols)
			}
		}
		if protocol == listener.ProtocolMySQL {
			c.Backends["replica-a"].MySQL.UpstreamURL = "invalid"
		} else {
			c.Backends["replica-a"].Postgres.UpstreamURL = "invalid"
		}
		if err := Listeners(c); err == nil {
			t.Fatal("native target escaped protocol validation")
		}
	}
}

func TestGreptimeDBListenerMapping(t *testing.T) {
	for _, names := range [][]string{{"default", "greptime"}, {"greptime"}, {"default"}, nil} {
		c := config.NewConfig()
		c.Listeners["greptime"] = &listener.Options{Protocol: listener.ProtocolPostgres, ListenPort: 8489}
		o := bo.New()
		o.Provider, o.OriginURL = "greptimedb", "http://db.example:4000"
		o.ListenerNames = names
		c.Backends = bo.Lookup{"greptime": o}
		if err := Listeners(c); err != nil {
			t.Fatalf("listeners %v: %v", names, err)
		}
		wantHTTP := len(names) == 0 || names[0] == "default"
		if o.HasHTTPListener != wantHTTP {
			t.Fatalf("listeners %v: HTTP=%t", names, o.HasHTTPListener)
		}
		// Revalidation must clear the synthesized flag when HTTP is removed.
		o.ListenerNames = []string{"greptime"}
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if o.HasHTTPListener {
			t.Fatal("stale HTTP listener after revalidation")
		}
	}
}

func TestGreptimeDBRejectsInvalidMappings(t *testing.T) {
	for _, tt := range []struct {
		name, origin string
		listeners    []string
		want         string
	}{
		{"native URL without listener", "postgres://user:password@db.example/public", nil, "HTTP listener requires"},
		{"native URL on HTTP listener", "postgres://user:password@db.example/public", []string{"default", "greptime"}, "HTTP listener requires"},
		{"bad scheme", "mysql://db.example/public", []string{"greptime"}, "unsupported postgres origin scheme"},
		{"missing host", "http:///v1/sql", []string{"greptime"}, "has no host"},
		{"missing listener", "http://db.example:4000", []string{"missing"}, "undefined listener"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := bo.New()
			o.Provider, o.OriginURL, o.ListenerNames = "greptimedb", tt.origin, tt.listeners
			c := config.NewConfig()
			c.Listeners["greptime"] = &listener.Options{Protocol: listener.ProtocolPostgres, ListenPort: 8489}
			c.Backends = bo.Lookup{"greptime": o}
			if err := Listeners(c); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

func TestGreptimeDBDeveloperAuthentication(t *testing.T) {
	c, err := config.Load([]string{"-config", "../../../docs/developer/environment/trickster-config/trickster.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	o := c.Backends["greptimedb1"]
	if o == nil || o.Postgres == nil || o.Postgres.UpstreamURL == "" {
		t.Fatal("developer backend requires a separate pgwire upstream URL")
	}
	auth := c.Authenticators[o.AuthenticatorName]
	if auth == nil || !auth.ProxyPreserve {
		t.Fatal("GreptimeDB HTTP origin requires preserved client credentials")
	}
}
