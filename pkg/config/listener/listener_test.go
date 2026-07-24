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

package listener

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"gopkg.in/yaml.v2"
)

func TestNewLookupDefaults(t *testing.T) {
	l := NewLookup()
	if l[DefaultFrontendName].ListenPort != 8480 {
		t.Errorf("unexpected default frontend port: %d", l[DefaultFrontendName].ListenPort)
	}
	if l[mgmt.ListenerNameMetrics].ListenPort != 8481 {
		t.Errorf("unexpected metrics port: %d", l[mgmt.ListenerNameMetrics].ListenPort)
	}
	if l[mgmt.ListenerNameMgmt].ListenPort != 8484 {
		t.Errorf("unexpected management port: %d", l[mgmt.ListenerNameMgmt].ListenPort)
	}
	for name, options := range l {
		if options.Protocol != ProtocolHTTP {
			t.Errorf("listener %q protocol = %q, want %q", name, options.Protocol, ProtocolHTTP)
		}
	}
}

func TestLookupUnmarshalAndClone(t *testing.T) {
	var wrapped struct {
		Listeners Lookup `yaml:"listeners"`
	}
	err := yaml.Unmarshal([]byte(`listeners:
  default:
    port: 9000
  custom:
    address: 127.0.0.2
    port: 9001
`), &wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped.Listeners[DefaultFrontendName].ListenPort != 9000 {
		t.Errorf("default frontend override was not applied")
	}
	custom := wrapped.Listeners["custom"]
	if custom == nil || custom.Protocol != ProtocolHTTP || custom.ListenPort != 9001 {
		t.Fatalf("unexpected custom listener: %#v", custom)
	}
	if _, ok := wrapped.Listeners[mgmt.ListenerNameMgmt]; !ok {
		t.Errorf("management listener default was not retained")
	}

	clone := wrapped.Listeners.Clone()
	clone["custom"].ListenPort = 9002
	if wrapped.Listeners["custom"].ListenPort != 9001 {
		t.Errorf("clone mutated original listener options")
	}
	clone["custom"].ListenPort = 9001
	if !wrapped.Listeners["custom"].Equal(clone["custom"]) {
		t.Errorf("equal options with separately allocated scalar pointers should compare equal")
	}
	clone["custom"].ListenPort++
	if wrapped.Listeners["custom"].Equal(clone["custom"]) {
		t.Errorf("different options should not compare equal")
	}
}
