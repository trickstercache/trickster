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

	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"gopkg.in/yaml.v2"
)

func TestOptionsInitialize(t *testing.T) {
	o := &Options{Mode: "MERGE"}
	if err := o.Initialize(""); err != nil {
		t.Fatal(err)
	}
	if o.Mode != ModeMerge {
		t.Fatalf("Mode = %q, want %q", o.Mode, ModeMerge)
	}
	if o.Headers == nil {
		t.Fatal("Headers must be initialized")
	}
}

func TestOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		options *Options
		wantErr bool
	}{
		{name: "preserve", options: &Options{Mode: ModePreserve}},
		{name: "merge override", options: &Options{Mode: ModeMerge,
			Headers: types.EnvStringMap{"Access-Control-Allow-Origin": "https://example.com"}}},
		{name: "merge delete", options: &Options{Mode: ModeMerge,
			Headers: types.EnvStringMap{"-Access-Control-Allow-Credentials": ""}}},
		{name: "replace empty", options: &Options{Mode: ModeReplace}},
		{name: "disable", options: &Options{Mode: ModeDisable}},
		{name: "disable with empty headers", options: &Options{Mode: ModeDisable,
			Headers: types.EnvStringMap{}}, wantErr: true},
		{name: "disable with headers", options: &Options{Mode: ModeDisable,
			Headers: types.EnvStringMap{"Access-Control-Allow-Origin": "*"}}, wantErr: true},
		{name: "default mode", options: &Options{}},
		{name: "invalid mode", options: &Options{Mode: "append"}, wantErr: true},
		{name: "preserve ignores headers", options: &Options{Mode: ModePreserve,
			Headers: types.EnvStringMap{"Access-Control-Allow-Origin": "*"}}, wantErr: true},
		{name: "non cors header", options: &Options{Mode: ModeReplace,
			Headers: types.EnvStringMap{"X-Other": "value"}}, wantErr: true},
		{name: "double operation", options: &Options{Mode: ModeMerge,
			Headers: types.EnvStringMap{"+-Access-Control-Allow-Origin": "*"}}, wantErr: true},
		{name: "invalid field name", options: &Options{Mode: ModeMerge,
			Headers: types.EnvStringMap{"Access-Control-Allow Origin": "*"}}, wantErr: true},
		{name: "invalid field value", options: &Options{Mode: ModeMerge,
			Headers: types.EnvStringMap{"Access-Control-Allow-Origin": "*\r\nX-Injected: true"}},
			wantErr: true},
		{name: "case insensitive duplicate", options: &Options{Mode: ModeMerge,
			Headers: types.EnvStringMap{
				"Access-Control-Allow-Origin":  "https://first.example.com",
				"+access-control-allow-origin": "https://second.example.com",
			}}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.options.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestOptionsUnmarshalYAMLDisableTracksHeadersBlock(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		wantHeaders bool
		wantErr     bool
	}{
		{name: "without headers", yaml: "mode: disable\n"},
		{name: "with empty headers", yaml: "mode: disable\nheaders: {}\n",
			wantHeaders: true, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var o Options
			if err := yaml.Unmarshal([]byte(tc.yaml), &o); err != nil {
				t.Fatal(err)
			}
			if err := o.Initialize(""); err != nil {
				t.Fatal(err)
			}
			if got := o.Headers != nil; got != tc.wantHeaders {
				t.Fatalf("Headers configured = %t, want %t", got, tc.wantHeaders)
			}
			_, err := o.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestOptionsClone(t *testing.T) {
	o := &Options{Mode: ModeMerge,
		Headers: types.EnvStringMap{"Access-Control-Allow-Origin": "https://example.com"}}
	clone := o.Clone()
	clone.Headers["Access-Control-Allow-Origin"] = "https://other.example.com"
	if got := o.Headers["Access-Control-Allow-Origin"]; got != "https://example.com" {
		t.Fatalf("clone mutated source Headers: %q", got)
	}
}

func TestOptionsUnmarshalYAMLDefaultsToReplace(t *testing.T) {
	var o Options
	if err := yaml.Unmarshal([]byte("headers: {}\n"), &o); err != nil {
		t.Fatal(err)
	}
	if o.Mode != ModeReplace {
		t.Fatalf("Mode = %q, want %q", o.Mode, ModeReplace)
	}
	if o.Headers == nil {
		t.Fatal("Headers must be initialized")
	}
}

func TestLegacyPolicy(t *testing.T) {
	o := Legacy()
	if !o.IsLegacy() {
		t.Fatal("Legacy() must return a legacy policy")
	}
	if o.PreservesOrigin() {
		t.Fatal("legacy policy must not change existing cache-key behavior")
	}
	o.Mode = ModeReplace
	if Legacy().Mode != "" {
		t.Fatal("Legacy() returned shared mutable state")
	}
}
