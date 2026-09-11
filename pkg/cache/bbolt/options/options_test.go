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
	if o.Filename != DefaultBBoltFile {
		t.Errorf("expected Filename %q, got %q", DefaultBBoltFile, o.Filename)
	}
	if o.Bucket != DefaultBBoltBucket {
		t.Errorf("expected Bucket %q, got %q", DefaultBBoltBucket, o.Bucket)
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
	o2.Filename = "other.db"
	if o.Equal(o2) {
		t.Error("expected Filename difference to make options unequal")
	}

	o3 := New()
	o3.Bucket = "other"
	if o.Equal(o3) {
		t.Error("expected Bucket difference to make options unequal")
	}
}

func TestUnmarshalYAML(t *testing.T) {
	const raw = `
filename: /tmp/custom.db
bucket: custom_bucket
`
	o := &Options{}
	if err := yaml.Unmarshal([]byte(raw), o); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if o.Filename != "/tmp/custom.db" {
		t.Errorf("expected Filename /tmp/custom.db, got %q", o.Filename)
	}
	if o.Bucket != "custom_bucket" {
		t.Errorf("expected Bucket custom_bucket, got %q", o.Bucket)
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
	if err := yaml.Unmarshal([]byte("filename: /tmp/partial.db"), o3); err != nil {
		t.Fatalf("yaml.Unmarshal partial: %v", err)
	}
	if o3.Filename != "/tmp/partial.db" {
		t.Errorf("expected Filename /tmp/partial.db, got %q", o3.Filename)
	}
	if o3.Bucket != DefaultBBoltBucket {
		t.Errorf("expected default Bucket %q, got %q", DefaultBBoltBucket, o3.Bucket)
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
