/*
 * Copyright 2018 The Trickster Authors
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

package mgmt

import (
	"errors"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"go.yaml.in/yaml/v3"
)

func TestValidate(t *testing.T) {
	c := New()
	if c.ConfigHandlerListener != ListenerNameMgmt {
		t.Fatalf("expected config handler listener to default to mgmt, got %s", c.ConfigHandlerListener)
	}

	c.ConfigHandlerListener = ""
	c.PprofListener = ""

	err := c.Validate()
	if err != nil {
		t.Error(err)
	}

	if c.PprofListener != DefaultPprofListenerName {
		t.Errorf("expected %s got %s", DefaultPprofListenerName, c.PprofListener)
	}
	if c.ConfigHandlerListener != DefaultConfigHandlerListenerName {
		t.Errorf("expected %s got %s", DefaultConfigHandlerListenerName, c.ConfigHandlerListener)
	}

	c.ConfigHandlerListener = "x"
	if err = c.Validate(); err != ErrInvalidConfigHandlerListenerName {
		t.Errorf("expected invalid config handler listener error, got %v", err)
	}
	c.ConfigHandlerListener = DefaultConfigHandlerListenerName

	c.PprofListener = "x"

	err = c.Validate()
	if err == nil {
		t.Error("expected error for invalid pprof listener name")
	}
}

func TestValidatePprofListenerNames(t *testing.T) {
	for _, name := range []string{ListenerNameMetrics, ListenerNameMgmt, ListenerNameBoth, ListenerNameOff} {
		c := New()
		c.PprofListener = name
		if err := c.Validate(); err != nil {
			t.Errorf("expected pprof listener name %q to be valid, got %v", name, err)
		}
	}

	for _, name := range []string{"reload", "management"} {
		c := New()
		c.PprofListener = name
		if err := c.Validate(); err != ErrInvalidPprofListenerName {
			t.Errorf("expected pprof listener name %q to be invalid, got %v", name, err)
		}
	}

	c := New()
	c.AutoReloadInterval = timeconv.Duration(-time.Second)
	if err := c.Validate(); !errors.Is(err, ErrInvalidAutoReloadInterval) {
		t.Errorf("error = %v; want %v", err, ErrInvalidAutoReloadInterval)
	}
}

func TestReloadOptionsYAML(t *testing.T) {
	o := New()
	const yml = `reload_handler_path: /reload
reload_drain_timeout: 17s
reload_rate_limit: 2s
auto_reload_interval: 10s
`
	if err := yaml.Unmarshal([]byte(yml), o); err != nil {
		t.Fatal(err)
	}
	if o.ReloadHandlerPath != "/reload" {
		t.Errorf("reload handler path = %q; want %q", o.ReloadHandlerPath, "/reload")
	}
	if o.ReloadDrainTimeout != timeconv.Duration(17*time.Second) {
		t.Errorf("reload drain timeout = %v; want %v", o.ReloadDrainTimeout, 17*time.Second)
	}
	if o.ReloadRateLimit != timeconv.Duration(2*time.Second) {
		t.Errorf("reload rate limit = %v; want %v", o.ReloadRateLimit, 2*time.Second)
	}
	if o.AutoReloadInterval != timeconv.Duration(10*time.Second) {
		t.Errorf("auto reload interval = %v; want %v", o.AutoReloadInterval, 10*time.Second)
	}
	if got := o.Clone().AutoReloadInterval; got != o.AutoReloadInterval {
		t.Errorf("cloned auto reload interval = %v; want %v", got, o.AutoReloadInterval)
	}
}

func TestClone(t *testing.T) {
	o := New()
	o.ListenPort = 9999
	o.ConfigHandlerPath = "/custom"
	clone := o.Clone()
	if clone == o {
		t.Fatal("Clone should return a distinct pointer")
	}
	if clone.ListenPort != 9999 || clone.ConfigHandlerPath != "/custom" {
		t.Fatalf("unexpected clone: %#v", clone)
	}
	clone.ListenPort = 1
	if o.ListenPort != 9999 {
		t.Fatal("mutating clone should not affect original")
	}
}
