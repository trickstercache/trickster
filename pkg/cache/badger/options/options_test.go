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

	d "github.com/trickstercache/trickster/v2/pkg/cache/options/defaults"

	"go.yaml.in/yaml/v3"
)

func TestNew(t *testing.T) {
	o := New()
	if o == nil {
		t.Fatal("expected non-nil options")
	}
	if o.Directory != d.DefaultCachePath {
		t.Errorf("expected Directory %q, got %q", d.DefaultCachePath, o.Directory)
	}
	if o.ValueDirectory != d.DefaultCachePath {
		t.Errorf("expected ValueDirectory %q, got %q", d.DefaultCachePath, o.ValueDirectory)
	}
}

func TestEqual(t *testing.T) {
	o := New()

	if o.Equal(nil) {
		t.Error("expected false for nil comparison")
	}
	if !o.Equal(o) {
		t.Error("expected options equal to self")
	}
	if !o.Equal(New()) {
		t.Error("expected default options to be equal")
	}
	if !(*Options)(nil).Equal(nil) {
		t.Error("expected nil receivers to be equal")
	}
	if (*Options)(nil).Equal(o) {
		t.Error("expected nil receiver unequal to non-nil")
	}

	o2 := New()
	o2.Directory = "/other"
	if o.Equal(o2) {
		t.Error("expected Directory difference to make options unequal")
	}

	o3 := New()
	o3.ValueDirectory = "/other"
	if o.Equal(o3) {
		t.Error("expected ValueDirectory difference to make options unequal")
	}
}

func TestUnmarshalYAML(t *testing.T) {
	const raw = `
directory: /tmp/badger-dir
value_directory: /tmp/badger-val
`
	o := &Options{}
	if err := yaml.Unmarshal([]byte(raw), o); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if o.Directory != "/tmp/badger-dir" {
		t.Errorf("expected Directory /tmp/badger-dir, got %q", o.Directory)
	}
	if o.ValueDirectory != "/tmp/badger-val" {
		t.Errorf("expected ValueDirectory /tmp/badger-val, got %q", o.ValueDirectory)
	}

	// Empty YAML should retain defaults applied by UnmarshalYAML.
	o2 := &Options{}
	if err := yaml.Unmarshal([]byte("{}"), o2); err != nil {
		t.Fatalf("yaml.Unmarshal empty: %v", err)
	}
	if !o2.Equal(New()) {
		t.Errorf("expected defaults, got %+v", o2)
	}

	// Partial YAML should overlay provided fields onto defaults.
	o3 := &Options{}
	if err := yaml.Unmarshal([]byte("directory: /custom"), o3); err != nil {
		t.Fatalf("yaml.Unmarshal partial: %v", err)
	}
	if o3.Directory != "/custom" {
		t.Errorf("expected Directory /custom, got %q", o3.Directory)
	}
	if o3.ValueDirectory != d.DefaultCachePath {
		t.Errorf("expected default ValueDirectory %q, got %q", d.DefaultCachePath, o3.ValueDirectory)
	}
}

func TestUnmarshalYAMLError(t *testing.T) {
	o := &Options{}
	// a sequence cannot be decoded into the options struct
	err := yaml.Unmarshal([]byte("- boom"), o)
	if err == nil {
		t.Fatal("expected an error")
	}
}
