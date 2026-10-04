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

	moqo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
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
			"webtransport origin", func(c *config.Config) { c.Backends["live"].OriginURL = "https://relay.example/moq" },
			"not yet supported",
		},
		{"bad scheme", func(c *config.Config) { c.Backends["live"].OriginURL = "http://relay.example/" }, "moqt://"},
		{"same host twice", func(c *config.Config) {
			c.Backends["live"].Hosts = []string{"live.example.com"}
			other := moqBackend("moqt://other.example:443/", "moq1")
			other.Hosts = []string{"LIVE.example.com"}
			c.Backends["other"] = other
		}, "both serve host"},
		{
			"http listener", func(c *config.Config) { c.Backends["live"].ListenerNames = []string{"default"} },
			"requires a listener with protocol",
		},
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
