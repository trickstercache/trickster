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

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	rlopts "github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
)

func limiter(limit int64, keys ...string) *rlopts.Options {
	o := &rlopts.Options{Limit: limit, Keys: keys}
	if err := o.Validate(); err != nil {
		panic(err)
	}
	return o
}

func httpBackend(name, provider, origin string) *bo.Options {
	o := bo.New()
	o.Name = name
	o.Provider = provider
	o.OriginURL = origin
	return o
}

func httpConfig(name string, opts *rlopts.Options) *config.Config {
	c := config.NewConfig()
	c.Backends = bo.Lookup{"api": httpBackend("api", providers.Prometheus, "http://prom:9090")}
	if opts != nil {
		opts.Name = name
		c.RateLimiters = rlopts.Lookup{name: opts}
	}
	return c
}

func TestRateLimitPlacementErrors(t *testing.T) {
	cases := []struct {
		name string
		edit func(*config.Config)
		want string
	}{
		{"undefined listener", func(c *config.Config) {
			c.Listeners[listener.DefaultFrontendName].RateLimiterName = "missing"
		}, "undefined rate limiter"},
		{"none as listener name", func(c *config.Config) {
			c.RateLimiters = rlopts.Lookup{}
			c.Listeners[listener.DefaultFrontendName].RateLimiterName = "none"
		}, "undefined rate limiter"},
		{"stream backend", func(c *config.Config) {
			c.Listeners["relay"] = &listener.Options{Protocol: listener.ProtocolTCP, ListenPort: 9000, Active: true}
			c.RateLimiters = rlopts.Lookup{"edge": limiter(1, "client_ip")}
			c.Backends["db"] = httpBackend("db", providers.ReverseProxyShort, "http://db:9")
			c.Backends["db"].ListenerNames = []string{"relay"}
			c.Backends["db"].RateLimiterName = "edge"
		}, "does not pass an HTTP route"},
		{"close on http", func(c *config.Config) {
			o := limiter(1, "client_ip")
			o.Action = rlopts.ActionClose
			_ = o.Validate()
			c.RateLimiters = rlopts.Lookup{"edge": o}
			c.Backends["api"].RateLimiterName = "edge"
		}, "action close"},
		{"unit on http", func(c *config.Config) {
			o := limiter(1, "client_ip")
			o.Unit = rlopts.UnitConnections
			_ = o.Validate()
			c.RateLimiters = rlopts.Lookup{"edge": o}
			c.Listeners[listener.DefaultFrontendName].RateLimiterName = "edge"
		}, "unit"},
		{"two planes", func(c *config.Config) {
			o := limiter(1, "client_ip")
			o.Unit = rlopts.UnitRequests
			_ = o.Validate()
			c.RateLimiters = rlopts.Lookup{"edge": o}
			c.Listeners[listener.DefaultFrontendName].RateLimiterName = "edge"
			c.Listeners["relay"] = &listener.Options{
				Protocol: listener.ProtocolTCP, ListenPort: 9000, Active: true, RateLimiterName: "edge",
			}
			c.Backends["db"] = httpBackend("db", providers.ReverseProxyShort, "http://db:9")
			c.Backends["db"].ListenerNames = []string{"relay"}
		}, "more than one plane"},
		{"unreadable key", func(c *config.Config) {
			c.RateLimiters = rlopts.Lookup{"edge": limiter(1, "sni")}
			c.Listeners[listener.DefaultFrontendName].RateLimiterName = "edge"
		}, "cannot read"},
		{"path none", func(c *config.Config) {
			c.RateLimiters = rlopts.Lookup{"edge": limiter(1, "client_ip")}
			c.Backends["api"].RateLimiterName = "edge"
			c.Backends["api"].Paths = po.List{{Path: "/healthz", RateLimiterName: "none"}}
		}, ""},
		{"undefined backend", func(c *config.Config) {
			c.Backends["api"].RateLimiterName = "missing"
		}, "invalid rate_limiter_name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := httpConfig("edge", nil)
			tc.edit(c)
			err := Validate(c)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if c.Backends["api"].Paths[0].RateLimiter != nil {
					t.Fatal("none resolved to a limiter")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNativeListenerRejectsRateLimit(t *testing.T) {
	c := httpConfig("edge", limiter(1, "client_ip"))
	err := bindListenerRateLimit(c, "sql", &listener.Options{
		Protocol: listener.ProtocolMySQL, RateLimiterName: "edge",
	})
	if err == nil || !strings.Contains(err.Error(), "native listeners") {
		t.Fatal(err)
	}
}

func TestRateLimitProxyProtocolWarning(t *testing.T) {
	c := httpConfig("edge", nil)
	c.Listeners["relay"] = &listener.Options{
		Protocol: listener.ProtocolTCP, ListenPort: 9000, Active: true,
		ProxyProtocol: true, RateLimiterName: "edge",
	}
	c.RateLimiters = rlopts.Lookup{"edge": limiter(1, "client_ip")}
	c.Backends["db"] = httpBackend("db", providers.ReverseProxyShort, "http://db:9")
	c.Backends["db"].ListenerNames = []string{"relay"}
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, warning := range c.LoaderWarnings {
		if strings.Contains(warning, "trusted_proxies") && strings.Contains(warning, "client_ip") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings %v", c.LoaderWarnings)
	}
}
