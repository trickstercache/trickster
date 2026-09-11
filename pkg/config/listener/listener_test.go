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
	"time"

	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	frontend "github.com/trickstercache/trickster/v2/pkg/frontend/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"go.yaml.in/yaml/v3"
)

func TestFrontendOptions(t *testing.T) {
	t.Parallel()
	if (*Options)(nil).FrontendOptions() != nil {
		t.Fatal("nil Options should return nil FrontendOptions")
	}

	size := int64(4096)
	o := &Options{
		ListenAddress:               "127.0.0.1",
		ListenPort:                  9000,
		TLSListenAddress:            "127.0.0.1",
		TLSListenPort:               9443,
		ConnectionsLimit:            10,
		MaxRequestBodySizeBytes:     &size,
		TruncateRequestBodyTooLarge: true,
		ServeTLS:                    true,
	}
	fopt := o.FrontendOptions()
	if fopt == nil {
		t.Fatal("expected non-nil frontend options")
	}
	if fopt.ListenPort != 9000 || fopt.TLSListenPort != 9443 ||
		fopt.ConnectionsLimit != 10 || !fopt.TruncateRequestBodyTooLarge || !fopt.ServeTLS {
		t.Fatalf("unexpected frontend options: %#v", fopt)
	}
	if fopt.MaxRequestBodySizeBytes == nil || *fopt.MaxRequestBodySizeBytes != size {
		t.Fatalf("unexpected max body size: %#v", fopt.MaxRequestBodySizeBytes)
	}
	*fopt.MaxRequestBodySizeBytes = 1
	if *o.MaxRequestBodySizeBytes != size {
		t.Fatal("FrontendOptions should deep-copy MaxRequestBodySizeBytes")
	}

	o.MaxRequestBodySizeBytes = nil
	fopt = o.FrontendOptions()
	if fopt.MaxRequestBodySizeBytes != nil {
		t.Fatal("nil MaxRequestBodySizeBytes should remain nil")
	}
}

func TestFromFrontendAndCloneEqual(t *testing.T) {
	t.Parallel()
	if FromFrontend(nil).ListenPort != 0 {
		t.Fatal("FromFrontend(nil) should return zero options")
	}

	size := int64(2048)
	src := frontend.New()
	src.ListenPort = 9100
	src.MaxRequestBodySizeBytes = &size
	src.TruncateRequestBodyTooLarge = true
	src.ServeTLS = true
	o := FromFrontend(src)
	if o.ListenPort != 9100 || o.MaxRequestBodySizeBytes == nil ||
		*o.MaxRequestBodySizeBytes != size || !o.TruncateRequestBodyTooLarge || !o.ServeTLS {
		t.Fatalf("unexpected FromFrontend result: %#v", o)
	}

	if (*Options)(nil).Clone() != nil {
		t.Fatal("Clone(nil) should return nil")
	}
	clone := o.Clone()
	if !o.Equal(clone) {
		t.Fatal("clone should equal original")
	}
	*clone.MaxRequestBodySizeBytes = 1
	if o.Equal(clone) {
		t.Fatal("differing MaxRequestBodySizeBytes should not compare equal")
	}
	clone.MaxRequestBodySizeBytes = nil
	if o.Equal(clone) {
		t.Fatal("nil vs non-nil MaxRequestBodySizeBytes should not compare equal")
	}
	if (*Options)(nil).Equal(nil) != true {
		t.Fatal("nil should equal nil")
	}
	if o.Equal(nil) {
		t.Fatal("non-nil should not equal nil")
	}
}

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

func TestLookupUnmarshalYAMLMerge(t *testing.T) {
	var wrapped struct {
		Listeners Lookup `yaml:"listeners"`
	}
	err := yaml.Unmarshal([]byte(`
listener_defaults: &listener_defaults
  address: 127.0.0.2
  port: 9001
listeners:
  custom:
    <<: *listener_defaults
    port: 9002
`), &wrapped)
	if err != nil {
		t.Fatal(err)
	}
	custom := wrapped.Listeners["custom"]
	if custom == nil || custom.ListenAddress != "127.0.0.2" || custom.ListenPort != 9002 {
		t.Fatalf("unexpected merged listener: %#v", custom)
	}
}

func TestMySQLLimitsYAMLDefaultsCloneAndEquality(t *testing.T) {
	var wrapped struct {
		Listeners Lookup `yaml:"listeners"`
	}
	err := yaml.Unmarshal([]byte(`listeners:
  mysql1:
    protocol: mysql
    port: 3306
    mysql:
      idle_timeout: 1m
      max_query_size_bytes: 4096
`), &wrapped)
	if err != nil {
		t.Fatal(err)
	}
	o := wrapped.Listeners["mysql1"]
	if o.MySQL == nil || o.MySQL.IdleTimeout != timeconv.Duration(time.Minute) ||
		o.MySQL.ReadTimeout != timeconv.Duration(mo.DefaultReadTimeout) ||
		o.MySQL.MaxQuerySizeBytes != 4096 {
		t.Fatalf("unexpected MySQL limits: %#v", o.MySQL)
	}
	if err := o.MySQL.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := o.Clone()
	clone.MySQL.MaxQuerySizeBytes++
	if o.Equal(clone) {
		t.Fatal("MySQL limit change should affect listener equality")
	}
}

func TestHTTP3EnabledAndEndpoint(t *testing.T) {
	base := func() *Options {
		o := New(DefaultFrontendName)
		o.Protocol = ProtocolHTTP
		o.ServeTLS = true
		o.TLSListenAddress = "10.0.0.1"
		o.TLSListenPort = 8443
		o.HTTP3 = &HTTP3Options{Enabled: true}
		return o
	}

	o := base()
	if !o.HTTP3Enabled() {
		t.Fatal("expected HTTP/3 to be enabled")
	}
	addr, port, advertised := o.HTTP3Endpoint()
	if addr != "10.0.0.1" || port != 8443 || advertised != 8443 {
		t.Errorf("expected defaults from the TLS endpoint, got %s:%d adv=%d", addr, port, advertised)
	}

	o = base()
	o.HTTP3.ListenAddress = "0.0.0.0"
	o.HTTP3.ListenPort = 8444
	o.HTTP3.AdvertisedPort = 443
	addr, port, advertised = o.HTTP3Endpoint()
	if addr != "0.0.0.0" || port != 8444 || advertised != 443 {
		t.Errorf("explicit values not honored: %s:%d adv=%d", addr, port, advertised)
	}

	// HTTP/3 requires TLS, and only applies to http listeners
	for _, tc := range []struct {
		name string
		mod  func(*Options)
	}{
		{"no tls", func(o *Options) { o.ServeTLS = false }},
		{"no tls port", func(o *Options) { o.TLSListenPort = 0 }},
		{"not http protocol", func(o *Options) { o.Protocol = ProtocolMySQL }},
		{"not enabled", func(o *Options) { o.HTTP3.Enabled = false }},
		{"no block", func(o *Options) { o.HTTP3 = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := base()
			tc.mod(o)
			if o.HTTP3Enabled() {
				t.Error("expected HTTP/3 to be disabled")
			}
			if _, p, _ := o.HTTP3Endpoint(); p != 0 {
				t.Errorf("expected no endpoint, got port %d", p)
			}
		})
	}
}

func TestHTTP3OptionsEqualAndClone(t *testing.T) {
	a := &HTTP3Options{Enabled: true, ListenPort: 8443}
	if !a.Equal(a.Clone()) {
		t.Error("clone should equal its source")
	}
	if a.Equal(&HTTP3Options{Enabled: true, ListenPort: 8444}) {
		t.Error("differing ports should not be equal")
	}
	if a.Equal(nil) || (*HTTP3Options)(nil).Equal(a) {
		t.Error("nil should not equal a populated config")
	}
	if !(*HTTP3Options)(nil).Equal(nil) {
		t.Error("two nils should be equal")
	}
	if (*HTTP3Options)(nil).Clone() != nil {
		t.Error("cloning nil should yield nil")
	}

	// Options.Equal must account for the HTTP/3 block
	o1, o2 := New(DefaultFrontendName), New(DefaultFrontendName)
	o1.HTTP3 = &HTTP3Options{Enabled: true}
	if o1.Equal(o2) {
		t.Error("listeners differing only in HTTP/3 must not compare equal")
	}
}
