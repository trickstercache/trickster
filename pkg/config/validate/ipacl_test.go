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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	uro "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func mustACLs(t *testing.T, lists ipacl.Lookup) ipacl.Lookup {
	t.Helper()
	if _, err := lists.Validate(); err != nil {
		t.Fatal(err)
	}
	return lists
}

func countWarnings(warnings []string, substring string) int {
	n := 0
	for _, warning := range warnings {
		if strings.Contains(warning, substring) {
			n++
		}
	}
	return n
}

func TestIPACLWarningsDeduped(t *testing.T) {
	c := config.NewConfig()
	c.IPACLs = ipacl.Lookup{"closed": {}}
	if err := IPACLs(c); err != nil {
		t.Fatal(err)
	}
	if err := IPACLs(c); err != nil {
		t.Fatal(err)
	}
	if got := countWarnings(c.LoaderWarnings, "no entries"); got != 1 {
		t.Fatalf("no-entries warnings = %d in %v", got, c.LoaderWarnings)
	}
}

func TestIPACLMissingFileFailsValidation(t *testing.T) {
	c := config.NewConfig()
	c.IPACLs = ipacl.Lookup{"office": {
		AllowFile: filepath.Join(t.TempDir(), "missing.lst"),
	}}
	err := IPACLs(c)
	if !errors.Is(err, ipacl.ErrInvalidFile) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("IPACLs(missing file) = %v", err)
	}
}

func TestListenerIPACLReferences(t *testing.T) {
	office := mustACLs(t, ipacl.Lookup{"office": {Allow: []string{"10.0.0.0/8"}}})
	peer := mustACLs(t, ipacl.Lookup{"edge": {Allow: []string{"10.0.0.0/8"}, Source: "peer"}})

	t.Run("valid", func(t *testing.T) {
		c := config.NewConfig()
		c.IPACLs = office
		c.Listeners[listener.DefaultFrontendName].IPACLName = "office"
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if c.Listeners[listener.DefaultFrontendName].IPACL != office["office"].Compiled {
			t.Fatal("listener list was not resolved")
		}
	})

	t.Run("peer", func(t *testing.T) {
		c := config.NewConfig()
		c.IPACLs = peer
		c.Listeners[listener.DefaultFrontendName].IPACLName = "edge"
		c.Listeners[listener.DefaultFrontendName].ProxyProtocol = true
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if warningsContain(c.LoaderWarnings, "no trusted_proxies") {
			t.Fatalf("peer list warned about trusted proxies: %v", c.LoaderWarnings)
		}
	})

	for _, name := range []string{"missing", "none"} {
		t.Run(name, func(t *testing.T) {
			c := config.NewConfig()
			c.IPACLs = office
			c.Listeners[listener.DefaultFrontendName].IPACLName = name
			err := Listeners(c)
			if err == nil || !strings.Contains(err.Error(), "undefined ip acl") {
				t.Fatalf("Listeners(%q) = %v", name, err)
			}
		})
	}
}

func TestListenerIPACLProxyProtocol(t *testing.T) {
	client := mustACLs(t, ipacl.Lookup{"office": {Allow: []string{"10.0.0.0/8"}}})

	t.Run("native client_ip with proxy protocol", func(t *testing.T) {
		c := config.NewConfig()
		c.IPACLs = client
		c.Listeners["ch"] = &listener.Options{
			Protocol:      listener.ProtocolClickHouse,
			ListenPort:    9000,
			ProxyProtocol: true,
			IPACLName:     "office",
		}
		backend := bo.New()
		backend.Provider = providers.ClickHouse
		backend.OriginURL = "http://localhost:9000"
		backend.ListenerNames = []string{"ch"}
		c.Backends = bo.Lookup{"click": backend}
		err := Listeners(c)
		if err == nil || !strings.Contains(err.Error(), "proxy_protocol") {
			t.Fatalf("Listeners = %v; want proxy_protocol refused", err)
		}
	})

	t.Run("native client_ip without proxy protocol", func(t *testing.T) {
		c := config.NewConfig()
		c.IPACLs = client
		c.Listeners["ch"] = &listener.Options{
			Protocol:   listener.ProtocolClickHouse,
			ListenPort: 9000,
			IPACLName:  "office",
		}
		backend := bo.New()
		backend.Provider = providers.ClickHouse
		backend.OriginURL = "http://localhost:9000"
		backend.ListenerNames = []string{"ch"}
		c.Backends = bo.Lookup{"click": backend}
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if c.Listeners["ch"].IPACL != client["office"].Compiled {
			t.Fatal("native listener list was not resolved")
		}
	})

	t.Run("client_ip proxy protocol without trusted proxies", func(t *testing.T) {
		c := config.NewConfig()
		c.IPACLs = client
		c.Listeners[listener.DefaultFrontendName].IPACLName = "office"
		c.Listeners[listener.DefaultFrontendName].ProxyProtocol = true
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if got := countWarnings(c.LoaderWarnings, "no trusted_proxies"); got != 1 {
			t.Fatalf("trusted-proxy warnings = %d in %v", got, c.LoaderWarnings)
		}
	})
}

func TestIPACLDrop(t *testing.T) {
	wall := mustACLs(t, ipacl.Lookup{
		"wall": {Allow: []string{"10.0.0.0/8"}, Action: "drop"},
	})["wall"].Compiled

	httpBackend := func() *bo.Options {
		backend := bo.New()
		backend.Provider = providers.Prometheus
		backend.OriginURL = "http://example"
		backend.IPACLName = "wall"
		backend.IPACL = wall
		return backend
	}

	t.Run("http backend", func(t *testing.T) {
		c := config.NewConfig()
		c.Backends = bo.Lookup{"api": httpBackend()}
		err := Listeners(c)
		if err == nil || !strings.Contains(err.Error(), "action drop") {
			t.Fatalf("Listeners = %v", err)
		}
	})

	t.Run("http path", func(t *testing.T) {
		c := config.NewConfig()
		backend := httpBackend()
		backend.IPACLName = ""
		backend.IPACL = nil
		backend.Paths = po.List{{Path: "/admin/", IPACLName: "wall", IPACL: wall}}
		c.Backends = bo.Lookup{"api": backend}
		err := Listeners(c)
		if err == nil || !strings.Contains(err.Error(), `path "/admin/"`) {
			t.Fatalf("Listeners = %v", err)
		}
	})

	t.Run("stream backend and path", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["relay"] = listener.New("relay")
		c.Listeners["relay"].Protocol = listener.ProtocolTCP
		c.Listeners["relay"].ListenPort = 9000
		backend := streamBackend("relay", providers.ReverseProxy)
		backend.IPACLName = "wall"
		backend.IPACL = wall
		backend.Paths = po.List{{Path: "/admin/", IPACLName: "wall", IPACL: wall}}
		c.Backends = bo.Lookup{"api": backend}
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mixed http and stream", func(t *testing.T) {
		c := config.NewConfig()
		c.Listeners["relay"] = listener.New("relay")
		c.Listeners["relay"].Protocol = listener.ProtocolTCP
		c.Listeners["relay"].ListenPort = 9001
		backend := bo.New()
		backend.Provider = providers.ReverseProxy
		backend.OriginURL = "http://example"
		backend.ListenerNames = []string{listener.DefaultFrontendName, "relay"}
		backend.IPACLName = "wall"
		backend.IPACL = wall
		c.Backends = bo.Lookup{"api": backend}
		err := Listeners(c)
		if err == nil || !strings.Contains(err.Error(), "action drop") {
			t.Fatalf("Listeners = %v", err)
		}
	})
}

func TestNativeBackendIPACLWarning(t *testing.T) {
	office := mustACLs(t, ipacl.Lookup{
		"office": {Allow: []string{"10.0.0.0/8"}},
	})["office"].Compiled

	clickhouse := func(names []string) *bo.Options {
		backend := bo.New()
		backend.Provider = providers.ClickHouse
		backend.OriginURL = "http://localhost:9000"
		backend.ListenerNames = names
		backend.IPACLName = "office"
		backend.IPACL = office
		return backend
	}

	withClickHouse := func(backend *bo.Options) *config.Config {
		c := config.NewConfig()
		c.Listeners["ch"] = &listener.Options{Protocol: listener.ProtocolClickHouse, ListenPort: 9000}
		c.Backends = bo.Lookup{"click": backend}
		return c
	}

	t.Run("native", func(t *testing.T) {
		c := withClickHouse(clickhouse([]string{"ch"}))
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if got := countWarnings(c.LoaderWarnings, "native listener"); got != 1 {
			t.Fatalf("native warnings = %d in %v", got, c.LoaderWarnings)
		}
	})

	t.Run("http only", func(t *testing.T) {
		c := withClickHouse(clickhouse(nil))
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if warningsContain(c.LoaderWarnings, "native listener") {
			t.Fatalf("http-only clickhouse warned: %v", c.LoaderWarnings)
		}
	})

	t.Run("mixed", func(t *testing.T) {
		c := withClickHouse(clickhouse([]string{"ch", listener.DefaultFrontendName}))
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if got := countWarnings(c.LoaderWarnings, "native listener"); got != 1 {
			t.Fatalf("native warnings = %d in %v", got, c.LoaderWarnings)
		}
	})
}

func TestIPACLDropIndirectHTTP(t *testing.T) {
	for _, attachment := range []string{"backend", "path"} {
		for _, via := range []string{"pool", "template", "rule default", "rule case", "mirror", "user default", "user mapping"} {
			t.Run(attachment+"/"+via, func(t *testing.T) {
				c := config.NewConfig()
				list := mustACLs(t, ipacl.Lookup{"wall": {Action: "drop"}})["wall"].Compiled
				front := &bo.Options{ListenerNames: []string{listener.DefaultFrontendName}}
				target := &bo.Options{}
				if attachment == "backend" {
					target.IPACLName, target.IPACL = "wall", list
				} else {
					target.Paths = po.List{nil, {Path: "/", IPACLName: "wall", IPACL: list}}
				}
				c.Backends = bo.Lookup{"front": front, "target": target, "unused": nil}
				switch via {
				case "pool":
					front.ALBOptions = &ao.Options{Pool: ao.PoolMemberList{{Name: "nested"}}}
					c.Backends["nested"] = &bo.Options{ALBOptions: &ao.Options{
						Pool: ao.PoolMemberList{{Name: "target"}, {Name: "missing"}, {Name: "front"}},
					}}
				case "template":
					target.IsTemplate = true
					front.ALBOptions = &ao.Options{Discovery: &ao.DiscoveryOptions{TemplateBackend: "target"}}
				case "rule default", "rule case":
					front.RuleName = "dispatch"
					rule := &ro.Options{}
					if via == "rule default" {
						rule.NextRoute = "target"
					} else {
						rule.CaseOptions = ro.CaseOptionsList{nil, {NextRoute: "target"}}
					}
					c.Rules = ro.Lookup{"dispatch": rule}
				case "mirror":
					front.Paths = po.List{nil, {Mirrors: []*po.MirrorOptions{nil, {BackendName: "target"}}}}
				case "user default", "user mapping":
					u := &uro.Options{}
					if via == "user default" {
						u.DefaultBackend = "target"
					} else {
						u.Users = uro.UserMappingOptionsByUser{"nil": nil, "alice": {ToBackend: "target"}}
					}
					front.ALBOptions = &ao.Options{UserRouter: u}
				}
				err := validateIPACLPlacements(c)
				if err == nil || !strings.Contains(err.Error(), "action drop") || !strings.Contains(err.Error(), "target") {
					t.Fatalf("indirect HTTP drop = %v", err)
				}
				list = mustACLs(t, ipacl.Lookup{"wall": {Action: "reject"}})["wall"].Compiled
				if attachment == "backend" {
					target.IPACL = list
				} else {
					target.Paths[1].IPACL = list
				}
				if err := validateIPACLPlacements(c); err != nil {
					t.Fatalf("indirect HTTP reject = %v", err)
				}
			})
		}
	}
}

func TestStreamMemberIPACLPlacements(t *testing.T) {
	for _, protocol := range []string{listener.ProtocolTCP, listener.ProtocolTLS, listener.ProtocolUDP} {
		for _, via := range []string{"member", "nested alb", "nested alb policy", "template", "template path"} {
			t.Run(protocol+"/"+via, func(t *testing.T) {
				c := config.NewConfig()
				c.Listeners["relay"] = listener.New("relay")
				c.Listeners["relay"].Protocol = protocol
				list := mustACLs(t, ipacl.Lookup{"wall": {}})["wall"].Compiled
				front := streamBackend("relay", providers.ALB)
				front.ALBOptions = &ao.Options{MechanismName: "rr"}
				target := streamBackend("relay", providers.ReverseProxyShort)
				target.IPACLName, target.IPACL = "wall", list
				c.Backends = bo.Lookup{"front": front, "target": target}
				switch via {
				case "member":
					front.ALBOptions.Pool = ao.PoolMemberList{{Name: "target"}}
				case "nested alb":
					front.ALBOptions.Pool = ao.PoolMemberList{{Name: "nested"}}
					nested := streamBackend("relay", providers.ALB)
					nested.ALBOptions = &ao.Options{MechanismName: "rr", Pool: ao.PoolMemberList{{Name: "target"}}}
					c.Backends["nested"] = nested
				case "nested alb policy":
					front.ALBOptions.Pool = ao.PoolMemberList{{Name: "target"}}
					target.Provider = providers.ALB
					target.ALBOptions = &ao.Options{MechanismName: "rr", Pool: ao.PoolMemberList{{Name: "leaf"}}}
					c.Backends["leaf"] = streamBackend("relay", providers.ReverseProxyShort)
				case "template", "template path":
					target.IsTemplate = true
					front.ALBOptions.Discovery = &ao.DiscoveryOptions{TemplateBackend: "target"}
					if via == "template path" {
						target.IPACLName, target.IPACL = "", nil
						target.Paths = po.List{{Path: "/", IPACLName: "wall", IPACL: list}}
					}
				}
				if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "stream member access lists are not supported") {
					t.Fatalf("stream member ACL = %v", err)
				}
				target.IPACLName, target.IPACL, target.Paths = "", nil, nil
				front.IPACLName, front.IPACL = "wall", list
				if err := Listeners(c); err != nil {
					t.Fatalf("front ACL = %v", err)
				}
			})
		}
	}
}

func TestStreamMemberIPACLPathServedOverHTTP(t *testing.T) {
	// a member that HTTP serves too keeps a path list for those requests, but not a backend list
	c := config.NewConfig()
	c.Listeners["relay"] = listener.New("relay")
	c.Listeners["relay"].Protocol = listener.ProtocolTCP
	list := mustACLs(t, ipacl.Lookup{"wall": {}})["wall"].Compiled
	front := streamBackend("relay", providers.ALB)
	front.ALBOptions = &ao.Options{MechanismName: "rr", Pool: ao.PoolMemberList{{Name: "target"}}}
	target := streamBackend("relay", providers.ReverseProxyShort)
	target.ListenerName, target.ListenerNames = "", []string{"relay", listener.DefaultFrontendName}
	target.Paths = po.List{{Path: "/api/", IPACLName: "wall", IPACL: list}}
	c.Backends = bo.Lookup{"front": front, "target": target}
	if err := Listeners(c); err != nil {
		t.Fatalf("path list on a member HTTP serves = %v", err)
	}
	target.IPACLName, target.IPACL = "wall", list
	if err := Listeners(c); err == nil || !strings.Contains(err.Error(), "stream member access lists are not supported") {
		t.Fatalf("backend list on a stream member = %v", err)
	}
}

func TestIPACLUnreachableTemplateDrop(t *testing.T) {
	c := config.NewConfig()
	list := mustACLs(t, ipacl.Lookup{"wall": {Action: "drop"}})["wall"].Compiled
	c.Backends = bo.Lookup{"unused": {IsTemplate: true, IPACLName: "wall", IPACL: list}}
	if err := validateIPACLPlacements(c); err != nil {
		t.Fatalf("unreachable template = %v", err)
	}
}
