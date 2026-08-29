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

package config

import (
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
)

func TestLegacyServerOverlayAndExplicitPrecedence(t *testing.T) {
	c := NewConfig()
	err := c.loadYAMLConfig(`
frontend:
  listen_port: 8100
  connections_limit: 7
metrics:
  listen_port: 8101
mgmt:
  listen_port: 8104
listeners:
  default:
    port: 9100
  custom:
    port: 9101
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.applyLegacyListenerOptions(); err != nil {
		t.Fatal(err)
	}

	if got := c.Listeners[listener.DefaultFrontendName].ListenPort; got != 9100 {
		t.Errorf("explicit listener did not override legacy frontend: got %d", got)
	}
	if got := c.Listeners[listener.DefaultFrontendName].ConnectionsLimit; got != 7 {
		t.Errorf("unspecified canonical field did not inherit legacy frontend value: got %d", got)
	}
	if got := c.Listeners[mgmt.ListenerNameMetrics].ListenPort; got != 8101 {
		t.Errorf("legacy metrics port was not translated: got %d", got)
	}
	if got := c.Listeners[mgmt.ListenerNameMgmt].ListenPort; got != 8104 {
		t.Errorf("legacy management port was not translated: got %d", got)
	}
	for _, warning := range []string{legacyFrontendWarning, legacyMetricsWarning, legacyMgmtWarning} {
		if !slices.Contains(c.LoaderWarnings, warning) {
			t.Errorf("missing deprecation warning %q", warning)
		}
	}
}

func TestNewConfigDefinesBuiltInListeners(t *testing.T) {
	c := NewConfig()
	for _, name := range []string{listener.DefaultFrontendName, mgmt.ListenerNameMgmt, mgmt.ListenerNameMetrics} {
		if c.Listeners[name] == nil {
			t.Errorf("missing built-in listener %q", name)
		}
	}
}

func TestLegacyNegativePortDisablesServerPort(t *testing.T) {
	c := NewConfig()
	if err := c.loadYAMLConfig("mgmt:\n  listen_port: -1\n"); err != nil {
		t.Fatal(err)
	}
	if err := c.applyLegacyListenerOptions(); err != nil {
		t.Fatal(err)
	}
	if got := c.Listeners[mgmt.ListenerNameMgmt].ListenPort; got != 0 {
		t.Errorf("legacy disabled management port = %d, want 0", got)
	}
}

func TestFrontendOptionsForListener(t *testing.T) {
	if (*Config)(nil).FrontendOptionsForListener(listener.DefaultFrontendName) != nil {
		t.Fatal("nil config should return nil")
	}
	c := NewConfig()
	c.Listeners = nil
	if c.FrontendOptionsForListener(listener.DefaultFrontendName) != nil {
		t.Fatal("nil listeners should return nil")
	}

	c = NewConfig()
	c.Listeners[listener.DefaultFrontendName].ListenPort = 9123
	got := c.FrontendOptionsForListener(listener.DefaultFrontendName)
	if got == nil || got.ListenPort != 9123 {
		t.Fatalf("unexpected frontend options: %#v", got)
	}
	if c.FrontendOptionsForListener("missing") != nil {
		t.Fatal("missing listener should return nil")
	}
	c.Listeners["nil"] = nil
	if c.FrontendOptionsForListener("nil") != nil {
		t.Fatal("nil listener entry should return nil")
	}
}

func TestRequireListener(t *testing.T) {
	if _, err := (*Config)(nil).RequireListener("default"); err == nil {
		t.Fatal("nil config should error")
	}
	c := NewConfig()
	o, err := c.RequireListener(listener.DefaultFrontendName)
	if err != nil || o == nil {
		t.Fatalf("RequireListener(default) = (%v, %v)", o, err)
	}
	if _, err := c.RequireListener("missing"); err == nil {
		t.Fatal("missing listener should error")
	}
}

func TestAddLoaderWarningDedupes(t *testing.T) {
	c := NewConfig()
	c.addLoaderWarning("warn-a")
	c.addLoaderWarning("warn-a")
	c.addLoaderWarning("warn-b")
	if len(c.LoaderWarnings) != 2 {
		t.Fatalf("LoaderWarnings = %v, want 2 unique entries", c.LoaderWarnings)
	}
}

func TestLoadMergesLegacyAndPluralListenerBindings(t *testing.T) {
	for _, test := range []struct {
		body string
		want []string
	}{
		{"listener_name: legacy", []string{"legacy"}},
		{"listener_names: [second, first, second]", []string{"first", "second"}},
		{"listener_name: legacy\n    listener_names: [second, legacy, second]", []string{"legacy", "second"}},
	} {
		c := NewConfig()
		if err := c.loadYAMLConfig("backends:\n  default:\n    " + test.body + "\n"); err != nil {
			t.Fatal(err)
		}
		if got := c.Backends["default"].ListenerNames; !slices.Equal(got, test.want) {
			t.Fatalf("got %v, want %v", got, test.want)
		}
	}
}
