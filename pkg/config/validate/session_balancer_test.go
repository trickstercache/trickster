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
	"strings"
	"testing"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	configtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
)

// replicaConfig maps a mysql listener to a load balancer over two mysql backends that have
// no listener of their own
func replicaConfig(mechanism string) *config.Config {
	c := config.NewConfig()
	c.Listeners["mysql1"] = listener.New("mysql1")
	c.Listeners["mysql1"].Protocol = listener.ProtocolMySQL
	c.Listeners["mysql1"].ListenPort = 8486
	lb := bo.New()
	lb.Provider = providers.ALB
	lb.ListenerName = "mysql1"
	lb.ALBOptions = &ao.Options{MechanismName: mechanism, Pool: ao.Members("replica-a", "replica-b")}
	lb.AuthenticatorName = "mysql-listener-clients"
	lb.AuthOptions = &autho.Options{Users: configtypes.EnvStringMap{"client": "password"}}
	c.Backends = bo.Lookup{"replicas": lb, "replica-a": mysqlBackend(""), "replica-b": mysqlBackend("")}
	return c
}

func TestNativeListenerBalancesSessions(t *testing.T) {
	for _, mechanism := range []string{"rr", "round_robin", "p2c", "lc", "hrw"} {
		c := replicaConfig(mechanism)
		if err := Listeners(c); err != nil {
			t.Fatalf("%s: %v", mechanism, err)
		}
		if !c.Listeners["mysql1"].Active {
			t.Errorf("%s: the listener is not active", mechanism)
		}
		if names := c.Backends["replica-a"].ListenerNames; len(names) != 0 {
			t.Errorf("%s: a replica reached through the pool was given listeners %v", mechanism, names)
		}
	}
	keyed := func(key string, kind ao.KeyKind) func(*config.Config) {
		return func(c *config.Config) {
			c.Backends["replicas"].ALBOptions.HRW = ao.HRWOptions{Key: key, KeySource: ao.KeySource{Kind: kind}}
		}
	}
	for name, test := range map[string]struct {
		mechanism string
		adjust    func(*config.Config)
		want      string
	}{
		"user key":  {"hrw", keyed("user", ao.KeyUser), ""},
		"host key":  {"hrw", keyed("host", ao.KeyHost), "use client_ip or user"},
		"fanout":    {"fr", nil, "routes or balances sessions: hrw, lc, p2c, rr, ur"},
		"no timing": {"lt", nil, "routes or balances sessions"},
		"foreign member": {"rr", func(c *config.Config) {
			c.Backends["replica-b"].Provider = providers.ReverseProxyShort
			c.Backends["replica-b"].OriginURL = "http://example.com"
		}, "to be a mysql backend: \"replica-b\" is not"},
		"missing member": {"rr", func(c *config.Config) { delete(c.Backends, "replica-b") }, "\"replica-b\" is not"},
		"discovery": {"rr", func(c *config.Config) {
			c.Backends["replicas"].ALBOptions.Discovery = &ao.DiscoveryOptions{}
		}, "'discovery' is not supported on a mysql listener"},
		"stream block": {"rr", func(c *config.Config) {
			c.Backends["replicas"].ALBOptions.Stream = &ao.StreamOptions{}
		}, "'stream' options apply only"},
		"no listener users": {"rr", func(c *config.Config) {
			c.Backends["replicas"].AuthenticatorName, c.Backends["replicas"].AuthOptions = "", nil
		}, "requires an authenticator_name"},
	} {
		c := replicaConfig(test.mechanism)
		if test.adjust != nil {
			test.adjust(c)
		}
		err := Listeners(c)
		switch {
		case test.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
			t.Errorf("%s: error = %v, want %q", name, err, test.want)
		}
	}
}

// the same pool on an http listener is an ordinary load balancer, held to a request's rules
func TestSessionKeysAreForNativeListeners(t *testing.T) {
	c := config.NewConfig()
	lb := bo.New()
	lb.Provider = providers.ALB
	lb.ALBOptions = &ao.Options{
		MechanismName: "hrw", Pool: ao.Members("origin"),
		HRW: ao.HRWOptions{Key: "user", KeySource: ao.KeySource{Kind: ao.KeyUser}},
	}
	origin := bo.New()
	origin.Provider = providers.ReverseProxyShort
	origin.OriginURL = "http://example.com"
	c.Backends = bo.Lookup{"lb": lb, "origin": origin}
	if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "hrw.key \"user\"") {
		t.Fatalf("error = %v", err)
	}
}

// a load balancer mapped to listeners of two kinds must suit both
func TestALBsOnTwoPlanesAreHeldToBoth(t *testing.T) {
	web := func(c *config.Config) {
		c.Listeners["web"] = listener.New("web")
		c.Listeners["web"].ListenPort = 18480
	}
	t.Run("http and mysql", func(t *testing.T) {
		for key, want := range map[string]string{
			"client_ip": "",
			"user":      "hrw.key \"user\" cannot be read from a request, which http listener \"web\" serves",
		} {
			c := replicaConfig("hrw")
			web(c)
			lb := c.Backends["replicas"]
			lb.ListenerName, lb.ListenerNames = "", []string{"mysql1", "web"}
			ks, err := ao.ParseKeySource(key)
			if err != nil {
				t.Fatal(err)
			}
			lb.ALBOptions.HRW = ao.HRWOptions{Key: key, KeySource: ks}
			err = Listeners(c)
			if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
				t.Errorf("%s: error = %v, want %q", key, err, want)
			}
		}
	})
	t.Run("http and tls", func(t *testing.T) {
		for name, test := range map[string]struct {
			mechanism string
			set       func(*ao.Options)
			want      string
		}{
			"client_ip": {"hrw", func(*ao.Options) {}, ""},
			"sni": {"hrw", func(o *ao.Options) { o.HRW = ao.HRWOptions{Key: "sni", KeySource: ao.KeySource{Kind: ao.KeySNI}} },
				"hrw.key \"sni\" cannot be read from a request"},
			"host": {"hrw", func(o *ao.Options) { o.HRW = ao.HRWOptions{Key: "host", KeySource: ao.KeySource{Kind: ao.KeyHost}} },
				"cannot read alb backend \"lb\"'s hrw.key \"host\""},
			"default signal": {"lt", func(*ao.Options) {}, ""},
			"connect signal": {"lt", func(o *ao.Options) { o.LT.Signal = ao.LTSignalConnect }, "on a http listener"},
			"write signal":   {"lt", func(o *ao.Options) { o.LT.Signal = ao.LTSignalFirstWrite }, "on a tls listener"},
			"stream block":   {"rr", func(o *ao.Options) { o.Stream = &ao.StreamOptions{ConnectRetries: 1} }, ""},
			"race":           {"race", func(*ao.Options) {}, "cannot serve http listener \"web\""},
		} {
			c := config.NewConfig()
			web(c)
			c.Listeners["relay"] = listener.New("relay")
			c.Listeners["relay"].Protocol = listener.ProtocolTLS
			c.Listeners["relay"].ListenPort = 9443
			lb := bo.New()
			lb.Provider = providers.ALB
			lb.ListenerNames = []string{"relay", "web"}
			lb.ALBOptions = &ao.Options{MechanismName: test.mechanism, Pool: ao.Members("m1")}
			test.set(lb.ALBOptions)
			member := bo.New()
			member.Provider = providers.ReverseProxyShort
			member.OriginURL = "tcp://member.example.com:9000"
			c.Backends = bo.Lookup{"lb": lb, "m1": member}
			err := Listeners(c)
			if (test.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), test.want)) {
				t.Errorf("%s: error = %v, want %q", name, err, test.want)
			}
		}
	})
}
