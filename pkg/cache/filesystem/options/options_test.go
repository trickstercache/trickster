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

func TestNewDefaults(t *testing.T) {
	o := New()
	if o == nil {
		t.Fatal("expected non-nil options")
	}
	if o.CachePath != d.DefaultCachePath {
		t.Errorf("expected CachePath %q, got %q", d.DefaultCachePath, o.CachePath)
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
	o2.CachePath = "/other"
	if o.Equal(o2) {
		t.Error("expected CachePath difference to make options unequal")
	}
}

func TestUnmarshalYAML(t *testing.T) {
	const raw = `
cache_path: /tmp/fs-cache
`
	o := &Options{}
	if err := yaml.Unmarshal([]byte(raw), o); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if o.CachePath != "/tmp/fs-cache" {
		t.Errorf("expected CachePath /tmp/fs-cache, got %q", o.CachePath)
	}

	o2 := &Options{}
	if err := yaml.Unmarshal([]byte("{}"), o2); err != nil {
		t.Fatalf("yaml.Unmarshal empty: %v", err)
	}
	if !o2.Equal(New()) {
		t.Errorf("expected defaults, got %+v", o2)
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
