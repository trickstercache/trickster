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

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
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
