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
