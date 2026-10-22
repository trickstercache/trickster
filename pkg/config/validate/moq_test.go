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
	moqo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	corso "github.com/trickstercache/trickster/v2/pkg/proxy/cors/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	rlopts "github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
)

func moqBackend(origin string, listeners ...string) *bo.Options {
	o := bo.New()
	o.Provider, o.OriginURL, o.ListenerNames = "moq", origin, listeners
	o.TLS = &to.Options{FullChainCertPath: "cert.pem", PrivateKeyPath: "key.pem", ServeTLS: true}
	return o
}

func moqConfig() *config.Config {
	c := config.NewConfig()
	c.Listeners["moq1"] = &listener.Options{Protocol: listener.ProtocolMoQ, ListenPort: 10443}
	c.Backends = bo.Lookup{"live": moqBackend("moqt://origin.example:443/live", "moq1")}
	return c
}

func TestMoQRuntimeCerts(t *testing.T) {
	// a moq listener whose certificates arrive at runtime, as the Kubernetes controller installs them, needs none
	// from its backends
	c := moqConfig()
	c.Backends["live"].TLS = nil
	c.Listeners["moq1"].TLSRuntimeCerts = true
	if err := Listeners(c); err != nil {
		t.Fatalf("a runtime-certificate moq listener: %v", err)
	}
	c = moqConfig()
	c.Listeners["udp"] = &listener.Options{Protocol: listener.ProtocolUDP, ListenPort: 10444, TLSRuntimeCerts: true}
	if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "tls_runtime_certs") {
		t.Errorf("runtime certificates on a protocol without TLS: %v", err)
	}
}

func TestMoQListenerDefaults(t *testing.T) {
	c := moqConfig()
	if err := Listeners(c); err != nil {
		t.Fatal(err)
	}
	if c.Listeners["moq1"].MoQ == nil || c.Backends["live"].MoQ == nil || !c.Listeners["moq1"].Active {
		t.Error("validation fills the moq blocks with their defaults and activates the listener")
	}
}

func TestMoQValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{"no certificate", func(c *config.Config) { c.Backends["live"].TLS = nil }, "needs a certificate"},
		{"tls port", func(c *config.Config) { c.Listeners["moq1"].TLSListenPort = 10444 }, "cannot configure a TLS port"},
		{
			"bad drafts", func(c *config.Config) { c.Listeners["moq1"].MoQ = &moqo.ListenerOptions{Drafts: []int{99}} },
			"not supported",
		},
		{
			"default draft not offered", func(c *config.Config) {
				c.Backends["live"].MoQ = moqo.New()
				c.Backends["live"].MoQ.WebTransport.DefaultDraft = 20
				c.Backends["live"].MoQ.WebTransport.Drafts = []int{21}
			}, "default_draft",
		},
		{
			"bad allowed origin", func(c *config.Config) {
				c.Backends["live"].MoQ = moqo.New()
				c.Backends["live"].MoQ.WebTransport.AllowedOrigins = []string{"https://player.example/watch"}
			}, "allowed_origins",
		},
		{
			"no webtransport sessions", func(c *config.Config) {
				c.Backends["live"].MoQ = moqo.New()
				c.Backends["live"].MoQ.WebTransport.MaxSessions = 0
			}, "max_sessions",
		},
		{"bad scheme", func(c *config.Config) { c.Backends["live"].OriginURL = "http://relay.example/" }, "moqt://"},
		{"same host twice", func(c *config.Config) {
			c.Backends["live"].Hosts = []string{"live.example.com"}
			other := moqBackend("moqt://other.example:443/", "moq1")
			other.Hosts = []string{"LIVE.example.com"}
			c.Backends["other"] = other
		}, "both serve host"},
		{"plain udp on the port", func(c *config.Config) {
			c.Listeners["relay"] = &listener.Options{Protocol: listener.ProtocolUDP, ListenPort: 10443}
			c.Backends["udp"] = &bo.Options{Provider: "rp", OriginURL: "http://127.0.0.1:9", ListenerNames: []string{"relay"}}
		}, "both use"},
		{"overlapping drafts on one port", func(c *config.Config) {
			c.Listeners["moq2"] = &listener.Options{Protocol: listener.ProtocolMoQ, ListenPort: 10443}
			c.Backends["second"] = moqBackend("moqt://second.example:443/", "moq2")
		}, "both serve moqt-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := moqConfig()
			tc.mutate(c)
			if err := Listeners(c); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestMoQRoutingAndSharing(t *testing.T) {
	c := moqConfig()
	c.Backends["live"].Hosts = []string{"live.example.com"}
	c.Backends["vod"] = moqBackend("moqt://vod.example:443/", "moq1")
	c.Backends["vod"].Paths = po.List{&po.Options{Path: "/vod"}}
	c.Backends["studio"] = moqBackend("", "moq1")
	// an HTTP/3 endpoint and a moq listener with other drafts share one UDP port, told apart by ALPN
	second := moqo.NewListener()
	second.Drafts = []int{20}
	c.Listeners["moq2"] = &listener.Options{Protocol: listener.ProtocolMoQ, ListenPort: 10443, MoQ: second}
	c.Listeners["moq1"].MoQ = moqo.NewListener()
	c.Listeners["moq1"].MoQ.Drafts = []int{21, 22}
	c.Backends["second"] = moqBackend("moqt://second.example:443/", "moq2")
	web := c.Listeners["default"]
	web.TLSListenPort, web.HTTP3 = 10443, &listener.HTTP3Options{Enabled: true}
	site := bo.New()
	site.Provider, site.OriginURL, site.ListenerNames = "rp", "http://127.0.0.1:9", []string{"default"}
	site.TLS = &to.Options{FullChainCertPath: "cert.pem", PrivateKeyPath: "key.pem", ServeTLS: true}
	c.Backends["site"] = site
	if err := Listeners(c); err != nil {
		t.Fatal(err)
	}
	if !web.HTTP3Enabled() {
		t.Error("the HTTP/3 endpoint stays enabled beside the moq listeners")
	}
}

func TestMoQOnHTTPListeners(t *testing.T) {
	c := moqConfig()
	c.Backends["live"].OriginURL = "https://relay.example/moq"
	c.Backends["live"].ListenerNames = []string{"default", "moq1"}
	c.Backends["live"].Paths = po.List{po.New(), &po.Options{Path: "/hello", HandlerName: "localresponse"}}
	web := c.Listeners["default"]
	web.TLSListenPort = 10443
	if err := Listeners(c); err != nil {
		t.Fatal(err)
	}
	if !hasWarning(c, "no HTTP/3 endpoint") {
		t.Errorf("a moq backend on an http listener without HTTP/3 is warned about: %v", c.LoaderWarnings)
	}
	paths := c.Backends["live"].Paths
	if paths[0].HandlerName != "moq" || len(paths[0].Methods) != 1 || paths[0].Methods[0] != "CONNECT" ||
		paths[1].HandlerName != "localresponse" {
		t.Errorf("a proxy path accepts WebTransport sessions, a local one keeps its handler: %+v %+v", paths[0], paths[1])
	}

	c = moqConfig()
	c.Backends["live"].ListenerNames = []string{"default"}
	web = c.Listeners["default"]
	web.TLSListenPort, web.HTTP3 = 10443, &listener.HTTP3Options{Enabled: true}
	if err := Listeners(c); err != nil {
		t.Fatal(err)
	}
	if hasWarning(c, "no HTTP/3 endpoint") {
		t.Error("an http listener with HTTP/3 carries WebTransport sessions")
	}
}

func hasWarning(c *config.Config, fragment string) bool {
	for _, w := range c.LoaderWarnings {
		if strings.Contains(w, fragment) {
			return true
		}
	}
	return false
}

func TestMoQOriginlessWarning(t *testing.T) {
	c := moqConfig()
	c.Backends["live"].OriginURL = ""
	if err := Listeners(c); err != nil {
		t.Fatal(err)
	}
	if !hasWarning(c, "serves nothing") {
		t.Error("an origin-less backend that accepts no publishers is warned about")
	}
	c = moqConfig()
	c.Backends["live"].OriginURL = ""
	c.Backends["live"].MoQ = moqo.New()
	c.Backends["live"].MoQ.Publish.Enabled = true
	if err := Listeners(c); err != nil {
		t.Fatal(err)
	}
	if hasWarning(c, "serves nothing") {
		t.Error("an ingest backend serves what is published to it")
	}
}

func moqPoolConfig() *config.Config {
	c := moqConfig()
	edge := c.Backends["live"]
	edge.OriginURL, edge.MoQ = "", moqo.New()
	edge.MoQ.Upstream.ALB = "origins"
	c.Backends["origin-a"] = moqBackend("moqt://a.example:443/live")
	c.Backends["origin-b"] = moqBackend("moqt://b.example:443/live")
	c.Backends["origins"] = &bo.Options{Provider: "alb", ALBOptions: &ao.Options{
		MechanismName: "hrw", Pool: ao.Members("origin-a", "origin-b"),
	}}
	return c
}

func TestMoQUpstreamALB(t *testing.T) {
	c := moqPoolConfig()
	if err := Listeners(c); err != nil {
		t.Fatalf("a moq backend reaching its origins through an alb: %v", err)
	}
	if hasWarning(c, "serves nothing") || hasWarning(c, "WebTransport") {
		t.Errorf("an alb upstream and its origins raise no warnings: %v", c.LoaderWarnings)
	}
	ejecting := moqPoolConfig()
	ejecting.Backends["origins"].ALBOptions.Stream = &ao.StreamOptions{PassiveHealth: &ao.PassiveHealthOptions{}}
	if err := Listeners(ejecting); err != nil {
		t.Errorf("failed connects may eject an origin: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{"undefined", func(c *config.Config) { c.Backends["live"].MoQ.Upstream.ALB = "nope" }, "undefined backend"},
		{"not an alb", func(c *config.Config) { c.Backends["live"].MoQ.Upstream.ALB = "origin-a" }, "not an alb"},
		{"fanout", func(c *config.Config) { c.Backends["origins"].ALBOptions.MechanismName = "fr" }, "picks one member"},
		{"least time", func(c *config.Config) { c.Backends["origins"].ALBOptions.MechanismName = "lt" }, "picks one member"},
		{"empty pool", func(c *config.Config) { c.Backends["origins"].ALBOptions.Pool = nil }, "has no pool"},
		{"originless member", func(c *config.Config) { c.Backends["origin-b"].OriginURL = "" }, "with an origin_url"},
		{"http member", func(c *config.Config) {
			c.Backends["origin-b"] = &bo.Options{Provider: "rp", OriginURL: "http://127.0.0.1:9"}
		}, "with an origin_url"},
		{"origin as well", func(c *config.Config) { c.Backends["live"].OriginURL = "moqt://x.example:443/" }, "exclusive"},
		{"stream retries", func(c *config.Config) {
			c.Backends["origins"].ALBOptions.Stream = &ao.StreamOptions{ConnectRetries: 2}
		}, "only passive_health"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := moqPoolConfig()
			tc.mutate(c)
			if err := Listeners(c); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestMoQAuthenticatorOnNativeListener(t *testing.T) {
	// a native session carries no HTTP credentials, so a backend's authenticator cannot judge it
	c := moqConfig()
	c.Backends["live"].AuthenticatorName = "basic"
	if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "authenticator_name") {
		t.Errorf("an authenticator on a backend a moq listener serves is refused: %v", err)
	}
	// over WebTransport alone, the CONNECT carries credentials the authenticator judges
	c = moqConfig()
	c.Backends["live"].AuthenticatorName = "basic"
	c.Backends["live"].ListenerNames = []string{"default"}
	if err := Listeners(c); err != nil && strings.Contains(err.Error(), "authenticator_name") {
		t.Errorf("an authenticator on a WebTransport backend is kept: %v", err)
	}
}

func TestMoQRateLimits(t *testing.T) {
	define := func(edit func(*rlopts.Options), keys ...string) *rlopts.Options {
		o := &rlopts.Options{Name: "per-ip", Limit: 5, Keys: keys}
		if edit != nil {
			edit(o)
		}
		if err := o.Validate(); err != nil {
			t.Fatal(err)
		}
		return o
	}
	onListener := func(o *rlopts.Options) error {
		c := moqConfig()
		c.RateLimiters = rlopts.Lookup{o.Name: o}
		c.Listeners["moq1"].RateLimiterName = o.Name
		return Listeners(c)
	}
	onBackend := func(o *rlopts.Options) error {
		c := moqConfig()
		c.RateLimiters = rlopts.Lookup{o.Name: o}
		c.Backends["live"].RateLimiterName, c.Backends["live"].RateLimiter = o.Name, o
		return Listeners(c)
	}
	// a moq listener counts sessions by client address, and a moq backend the requests of its sessions
	if err := onListener(define(nil, "client_ip")); err != nil {
		t.Errorf("a moq listener's session limiter: %v", err)
	}
	if err := onListener(define(func(o *rlopts.Options) { o.Unit = rlopts.UnitSessions })); err != nil {
		t.Errorf("a moq listener's limiter with one bucket: %v", err)
	}
	if err := onBackend(define(nil, "client_ip")); err != nil {
		t.Errorf("a native-only moq backend's request limiter: %v", err)
	}
	for name, tc := range map[string]struct {
		attach func(*rlopts.Options) error
		o      *rlopts.Options
		want   string
	}{
		"listener requests": {onListener, define(func(o *rlopts.Options) { o.Unit = rlopts.UnitRequests }),
			"not valid for this plane"},
		"listener sni":   {onListener, define(nil, "sni"), "cannot read"},
		"listener close": {onListener, define(func(o *rlopts.Options) { o.Action = rlopts.ActionClose }), "close"},
		"backend path":   {onBackend, define(nil, "path"), "cannot read"},
		"backend sessions": {onBackend, define(func(o *rlopts.Options) { o.Unit = rlopts.UnitSessions }),
			"not valid for this plane"},
	} {
		if err := tc.attach(tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestMoQListenerIgnoredSettings(t *testing.T) {
	warned := func(edit func(*listener.Options)) string {
		c := moqConfig()
		// a loaded listener starts from the defaults, which warn of nothing
		l := listener.New("moq1")
		l.Protocol, l.ListenPort = listener.ProtocolMoQ, 10443
		if edit != nil {
			edit(l)
		}
		c.Listeners["moq1"] = l
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		for _, w := range c.LoaderWarnings {
			if strings.Contains(w, "no effect on a moq listener") {
				return w
			}
		}
		return ""
	}
	if w := warned(nil); w != "" {
		t.Errorf("a listener left at its defaults is warned of: %s", w)
	}
	w := warned(func(l *listener.Options) {
		l.ProxyProtocol, l.TrustedProxies, l.ConnectionsLimit = true, []string{"10.0.0.0/8"}, 5
	})
	for _, field := range []string{"proxy_protocol", "trusted_proxies", "connections_limit"} {
		if !strings.Contains(w, field) {
			t.Errorf("%s is not warned of: %q", field, w)
		}
	}
}

func TestMoQBackendIgnoredSettings(t *testing.T) {
	c := moqConfig()
	b := c.Backends["live"]
	b.CORS, b.ReqRewriterName = &corso.Options{}, "rw"
	b.Paths = po.List{{Path: "/live", ResponseHeaders: map[string]string{"X-A": "1"},
		RequestHeaders: map[string]string{"X-B": "2"}}}
	if err := Listeners(c); err != nil {
		t.Fatal(err)
	}
	var sessionless, connectOnly string
	for _, w := range c.LoaderWarnings {
		switch {
		case strings.Contains(w, "have no effect on MoQ sessions"):
			sessionless = w
		case strings.Contains(w, "apply only to a WebTransport session's CONNECT"):
			connectOnly = w
		}
	}
	if !strings.Contains(sessionless, "cors") || !strings.Contains(sessionless, `path "/live" response_headers`) {
		t.Errorf("settings no session reads: %q", sessionless)
	}
	if !strings.Contains(connectOnly, "req_rewriter_name") || !strings.Contains(connectOnly, `path "/live" request_headers`) {
		t.Errorf("settings only a CONNECT reads: %q", connectOnly)
	}
}
