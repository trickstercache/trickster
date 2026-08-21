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

	"go.yaml.in/yaml/v3"
)

func TestNew(t *testing.T) {
	o := New()
	if o == nil {
		t.Fatal("expected non-nil options")
	}
	if o.MaxSizeBytes != DefaultMaxSizeBytes {
		t.Errorf("expected MaxSizeBytes %d, got %d", DefaultMaxSizeBytes, o.MaxSizeBytes)
	}
	if o.NumCounters != DefaultNumCounters {
		t.Errorf("expected NumCounters %d, got %d", DefaultNumCounters, o.NumCounters)
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

	o2 := New()
	o2.MaxSizeBytes = 1
	if o.Equal(o2) {
		t.Error("expected MaxSizeBytes difference to make options unequal")
	}

	o3 := New()
	o3.NumCounters = 1
	if o.Equal(o3) {
		t.Error("expected NumCounters difference to make options unequal")
	}
}

func TestUnmarshalYAML(t *testing.T) {
	const raw = `
max_size_bytes: 1048576
num_counters: 1000
`
	o := &Options{}
	if err := yaml.Unmarshal([]byte(raw), o); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if o.MaxSizeBytes != 1048576 {
		t.Errorf("expected MaxSizeBytes 1048576, got %d", o.MaxSizeBytes)
	}
	if o.NumCounters != 1000 {
		t.Errorf("expected NumCounters 1000, got %d", o.NumCounters)
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
	if err := yaml.Unmarshal([]byte("max_size_bytes: 42"), o3); err != nil {
		t.Fatalf("yaml.Unmarshal partial: %v", err)
	}
	if o3.MaxSizeBytes != 42 {
		t.Errorf("expected MaxSizeBytes 42, got %d", o3.MaxSizeBytes)
	}
	if o3.NumCounters != DefaultNumCounters {
		t.Errorf("expected default NumCounters %d, got %d", DefaultNumCounters, o3.NumCounters)
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
