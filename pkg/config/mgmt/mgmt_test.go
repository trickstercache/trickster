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

	"gopkg.in/yaml.v2"
)

func TestValidate(t *testing.T) {
	c := New()
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
	c.PprofServer = ""

	err := c.Validate()
	if err != nil {
		t.Error(err)
	}

	if c.PprofServer != DefaultPprofServerName {
		t.Errorf("expected %s got %s", DefaultPprofServerName, c.PprofServer)
	}

	c.PprofServer = "x"

	err = c.Validate()
	if err == nil {
		t.Error("expected error for invalid pprof server name")
	}

	c = New()
	c.AutoReloadInterval = -time.Second
	if err := c.Validate(); !errors.Is(err, ErrInvalidAutoReloadInterval) {
		t.Errorf("error = %v; want %v", err, ErrInvalidAutoReloadInterval)
	}
}

func TestAutoReloadIntervalYAML(t *testing.T) {
	o := New()
	if err := yaml.Unmarshal([]byte("auto_reload_interval: 10s\n"), o); err != nil {
		t.Fatal(err)
	}
	if o.AutoReloadInterval != 10*time.Second {
		t.Errorf("auto reload interval = %v; want %v", o.AutoReloadInterval, 10*time.Second)
	}
	if got := o.Clone().AutoReloadInterval; got != o.AutoReloadInterval {
		t.Errorf("cloned auto reload interval = %v; want %v", got, o.AutoReloadInterval)
	}
}
