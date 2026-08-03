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
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	tlsopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
)

func TestListenersBackendMappings(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		c := config.NewConfig()
		c.Backends = bo.Lookup{"test": bo.New()}
		if err := Listeners(c); err != nil {
			t.Fatal(err)
		}
		if c.Backends["test"].ListenerName != listener.DefaultFrontendName {
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
