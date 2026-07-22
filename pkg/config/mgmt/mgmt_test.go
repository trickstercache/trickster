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

import "testing"

func TestValidate(t *testing.T) {
	c := New()
	if c.ConfigHandlerServer != ServerNameMgmt {
		t.Fatalf("expected config handler server to default to mgmt, got %s", c.ConfigHandlerServer)
	}

	c.ConfigHandlerServer = ""
	c.PprofServer = ""

	err := c.Validate()
	if err != nil {
		t.Error(err)
	}

	if c.PprofServer != DefaultPprofServerName {
		t.Errorf("expected %s got %s", DefaultPprofServerName, c.PprofServer)
	}
	if c.ConfigHandlerServer != DefaultConfigHandlerServerName {
		t.Errorf("expected %s got %s", DefaultConfigHandlerServerName, c.ConfigHandlerServer)
	}

	c.ConfigHandlerServer = "x"
	if err = c.Validate(); err != ErrInvalidConfigHandlerServerName {
		t.Errorf("expected invalid config handler server error, got %v", err)
	}
	c.ConfigHandlerServer = DefaultConfigHandlerServerName

	c.PprofServer = "x"

	err = c.Validate()
	if err == nil {
		t.Error("expected error for invalid pprof server name")
	}
}

func TestValidatePprofServerNames(t *testing.T) {
	for _, name := range []string{ServerNameMetrics, ServerNameMgmt, ServerNameBoth, ServerNameOff} {
		c := New()
		c.PprofServer = name
		if err := c.Validate(); err != nil {
			t.Errorf("expected pprof server name %q to be valid, got %v", name, err)
		}
	}

	for _, name := range []string{"reload", "management"} {
		c := New()
		c.PprofServer = name
		if err := c.Validate(); err != ErrInvalidPprofServerName {
			t.Errorf("expected pprof server name %q to be invalid, got %v", name, err)
		}
	}
}
