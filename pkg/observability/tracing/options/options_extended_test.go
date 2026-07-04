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

	"gopkg.in/yaml.v2"
)

func TestSanitizeSampleRate(t *testing.T) {
	t.Parallel()

	o := &Options{}
	o.SanitizeSampleRate()
	if o.SampleRate == nil || *o.SampleRate != 1.0 {
		t.Fatalf("SampleRate = %v", o.SampleRate)
	}

	rate := -1.0
	o = &Options{SampleRate: &rate}
	o.SanitizeSampleRate()
	if *o.SampleRate != 0.0 {
		t.Fatalf("SampleRate = %v", *o.SampleRate)
	}

	rate = 0.5
	o = &Options{SampleRate: &rate}
	o.SanitizeSampleRate()
	if *o.SampleRate != 0.5 {
		t.Fatalf("SampleRate = %v", *o.SampleRate)
	}
}

func TestProcessTracingOptionsDefaults(t *testing.T) {
	t.Parallel()

	o := &Options{}
	ProcessTracingOptions(Lookup{"trace": o})
	if o.ServiceName != DefaultTracerServiceName {
		t.Fatalf("ServiceName = %q", o.ServiceName)
	}
	if o.Provider != DefaultTracerProvider {
		t.Fatalf("Provider = %q", o.Provider)
	}
	if o.SampleRate == nil || *o.SampleRate != 1.0 {
		t.Fatalf("SampleRate = %v", o.SampleRate)
	}

	otlp := &Options{Provider: ProviderOTLP}
	ProcessTracingOptions(Lookup{ProviderOTLP: otlp})
	if otlp.Protocol != DefaultOTLPProtocol {
		t.Fatalf("Protocol = %q", otlp.Protocol)
	}
}

func TestCloneCopiesNestedFields(t *testing.T) {
	t.Parallel()

	rate := 0.25
	base := New()
	o := &Options{
		Endpoint:      "collector:4317",
		Protocol:      OTLPProtocolGRPC,
		Timeout:       2 * time.Second,
		SampleRate:    &rate,
		Tags:          map[string]string{"env": "test"},
		OmitTagsList:  []string{"drop-me"},
		StdOutOptions: base.StdOutOptions,
	}
	cl := o.Clone()
	if cl == o {
		t.Fatal("expected distinct clone")
	}
	cl.Tags["env"] = "changed"
	if o.Tags["env"] != "test" {
		t.Fatal("clone should not share tags map")
	}
	if cl.Protocol != OTLPProtocolGRPC {
		t.Fatalf("Protocol = %q", cl.Protocol)
	}
}

func TestLookupValidate(t *testing.T) {
	t.Parallel()

	l := Lookup{"default": New()}
	if err := l.Validate(); err != nil {
		t.Fatalf("Lookup.Validate: %v", err)
	}
	if l["default"].Name != "default" {
		t.Fatalf("Name = %q", l["default"].Name)
	}
}

func TestLookupValidateRejectsInvalidProtocol(t *testing.T) {
	t.Parallel()

	l := Lookup{"default": &Options{Protocol: "udp"}}
	if err := l.Validate(); err == nil {
		t.Fatal("expected invalid protocol error")
	}
}

func TestUnmarshalYAML(t *testing.T) {
	t.Parallel()

	const raw = `
tracing:
  default:
    provider: otlp
    protocol: grpc
    service_name: trickster-test
    sample_rate: 0.5
    omit_tags: [internal]
`
	type doc struct {
		Tracing Lookup `yaml:"tracing"`
	}
	var d doc
	if err := yaml.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	o := d.Tracing["default"]
	if o == nil || o.ServiceName != "trickster-test" {
		t.Fatalf("unexpected tracing options: %+v", o)
	}
	if o.Protocol != OTLPProtocolGRPC {
		t.Fatalf("Protocol = %q", o.Protocol)
	}
	if o.SampleRate == nil || *o.SampleRate != 0.5 {
		t.Fatalf("SampleRate = %v", o.SampleRate)
	}
}
