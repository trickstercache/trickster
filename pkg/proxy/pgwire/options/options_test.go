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

package options

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"go.yaml.in/yaml/v3"
)

const typeErrorYAML = ": [1]"

func TestOptionsDefaultsCloneAndValidate(t *testing.T) {
	o := New()
	if o.UpstreamTLSMode != TLSModeDisable {
		t.Fatalf("New() = %+v", o)
	}
	clone := o.Clone()
	clone.UpstreamTLSMode = TLSModeRequire
	if clone == o || o.UpstreamTLSMode != TLSModeDisable {
		t.Fatal("Clone did not produce an independent copy")
	}
	var nilOptions *Options
	if nilOptions.Clone() != nil || nilOptions.Validate() != nil {
		t.Fatal("nil options should clone and validate as nil")
	}
	for _, mode := range TLSModes() {
		if err := (&Options{UpstreamTLSMode: mode}).Validate(); err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
	if err := (&Options{UpstreamTLSMode: "prefer"}).Validate(); err == nil {
		t.Fatal("expected an unknown TLS mode to be rejected")
	}
}

func TestOptionsUnmarshalYAML(t *testing.T) {
	var o Options
	if err := yaml.Unmarshal([]byte("{}"), &o); err != nil || o.UpstreamTLSMode != TLSModeDisable {
		t.Fatalf("empty block = %+v, %v", o, err)
	}
	if err := yaml.Unmarshal([]byte("upstream_tls_mode: "+TLSModeVerifyFull), &o); err != nil ||
		o.UpstreamTLSMode != TLSModeVerifyFull {
		t.Fatalf("configured block = %+v, %v", o, err)
	}
	if err := yaml.Unmarshal([]byte("upstream_tls_mode"+typeErrorYAML), &o); err == nil {
		t.Fatal("expected a type error")
	}
}

func TestListenerOptionsDefaultsCloneAndValidate(t *testing.T) {
	o := NewListener()
	if time.Duration(o.HandshakeTimeout) != DefaultHandshakeTimeout ||
		time.Duration(o.IdleTimeout) != DefaultIdleTimeout ||
		o.MaxMessageSizeBytes != DefaultMaxMessageSizeBytes || o.AllowMD5 || o.AllowCleartextWithoutTLS {
		t.Fatalf("NewListener() = %+v", o)
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := o.Clone()
	clone.AllowMD5 = true
	if clone == o || o.AllowMD5 {
		t.Fatal("Clone did not produce an independent copy")
	}
	var nilOptions *ListenerOptions
	if nilOptions.Clone() != nil || nilOptions.Validate() != nil {
		t.Fatal("nil listener options should clone and validate as nil")
	}
	for name, mutate := range map[string]func(*ListenerOptions){
		"handshake": func(o *ListenerOptions) { o.HandshakeTimeout = 0 },
		"read":      func(o *ListenerOptions) { o.ReadTimeout = 0 },
		"write":     func(o *ListenerOptions) { o.WriteTimeout = -1 },
		"idle":      func(o *ListenerOptions) { o.IdleTimeout = 0 },
		"too small": func(o *ListenerOptions) { o.MaxMessageSizeBytes = 0 },
		"too large": func(o *ListenerOptions) { o.MaxMessageSizeBytes = MaxProtocolMessageSizeBytes + 1 },
	} {
		invalid := NewListener()
		mutate(invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
}

func TestListenerOptionsUnmarshalYAML(t *testing.T) {
	var o ListenerOptions
	if err := yaml.Unmarshal([]byte("idle_timeout: 90s\nallow_md5: true"), &o); err != nil {
		t.Fatal(err)
	}
	if o.IdleTimeout != timeconv.Duration(90*time.Second) || !o.AllowMD5 ||
		time.Duration(o.ReadTimeout) != DefaultReadTimeout {
		t.Fatalf("configured block = %+v", o)
	}
	if err := yaml.Unmarshal([]byte("allow_md5"+typeErrorYAML), &o); err == nil {
		t.Fatal("expected a type error")
	}
}
