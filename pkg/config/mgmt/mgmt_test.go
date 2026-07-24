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
}
