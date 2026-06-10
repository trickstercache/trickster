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

	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"gopkg.in/yaml.v2"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	o := New()
	o.Name = ""
	if ok, err := o.Validate(); ok || err != ErrInvalidName {
		t.Fatalf("Validate() = (%v, %v), want invalid name", ok, err)
	}

	o = New()
	o.Name = "default"
	o.Index.MaxSizeBytes = 100
	o.Index.MaxSizeBackoffBytes = 200
	if ok, err := o.Validate(); ok || err != errMaxSizeBackoffBytesTooBig {
		t.Fatalf("Validate backoff bytes = (%v, %v)", ok, err)
	}

	o = New()
	o.Name = "default"
	o.Index.MaxSizeObjects = 10
	o.Index.MaxSizeBackoffObjects = 20
	if ok, err := o.Validate(); ok || err != errMaxSizeBackoffObjectsTooBig {
		t.Fatalf("Validate backoff objects = (%v, %v)", ok, err)
	}

	o = New()
	o.Name = "default"
	if ok, err := o.Validate(); !ok || err != nil {
		t.Fatalf("Validate(default) = (%v, %v)", ok, err)
	}
}

func TestLookupValidate(t *testing.T) {
	t.Parallel()

	l := Lookup{"default": New()}
	if err := l.Validate(); err != nil {
		t.Fatalf("Lookup.Validate: %v", err)
	}

	l = Lookup{"": New()}
	if err := l.Validate(); err != ErrInvalidName {
		t.Fatalf("Lookup.Validate() = %v, want ErrInvalidName", err)
	}
}

func TestEqualProviderBranches(t *testing.T) {
	t.Parallel()

	o := New()
	o.Provider = providers.Redis
	o.ProviderID = providers.RedisID
	if !o.Equal(o.Clone()) {
		t.Fatal("redis options should be equal to clone")
	}

	o2 := o.Clone()
	o2.Redis.Endpoint = "different:6379"
	if o.Equal(o2) {
		t.Fatal("expected redis endpoint difference to make options unequal")
	}

	fs := New()
	fs.Provider = providers.Filesystem
	fs.ProviderID = providers.FilesystemID
	if !fs.Equal(fs.Clone()) {
		t.Fatal("filesystem options should be equal to clone")
	}
}

func TestInitializeProviderBranchesAndWarnings(t *testing.T) {
	t.Parallel()

	o := New()
	o.Provider = "filesystem"
	l := Lookup{"default": o}
	if _, err := l.Initialize(sets.New([]string{"default"})); err != nil {
		t.Fatalf("Initialize filesystem: %v", err)
	}
	if o.ProviderID != providers.FilesystemID || o.Redis != nil || o.Filesystem == nil {
		t.Fatalf("unexpected provider wiring: %+v", o)
	}

	o = New()
	o.Provider = "redis"
	o.Redis.ClientType = "standard"
	o.Redis.Endpoint = ""
	o.Redis.Endpoints = []string{"127.0.0.1:6379"}
	l = Lookup{"default": o}
	warnings, err := l.Initialize(sets.New([]string{"default"}))
	if err != nil {
		t.Fatalf("Initialize redis: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want standard/endpoints warning", warnings)
	}

	o = New()
	o.Provider = "redis"
	o.Redis.ClientType = "sentinel"
	o.Redis.Endpoint = "127.0.0.1:6379"
	o.Redis.Endpoints = nil
	l = Lookup{"default": o}
	warnings, err = l.Initialize(sets.New([]string{"default"}))
	if err != nil {
		t.Fatalf("Initialize sentinel: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want sentinel/endpoint warning", warnings)
	}
}

func TestInitializeRemovesInactiveCaches(t *testing.T) {
	t.Parallel()

	l := Lookup{
		"active":   New(),
		"inactive": New(),
	}
	if _, err := l.Initialize(sets.New([]string{"active"})); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, ok := l["inactive"]; ok {
		t.Fatal("expected inactive cache to be removed")
	}
}

func TestClearProviderOptionsAndUnmarshalYAML(t *testing.T) {
	t.Parallel()

	o := New()
	o.ClearProviderOptions()
	if o.Index != nil || o.Redis != nil || o.Memory != nil {
		t.Fatal("expected provider options to be cleared")
	}

	const raw = `
caches:
  default:
    provider: memory
    timeseries_chunk_factor: 4
`
	type doc struct {
		Caches Lookup `yaml:"caches"`
	}
	var d doc
	if err := yaml.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	c := d.Caches["default"]
	if c == nil || c.TimeseriesChunkFactor != 4 {
		t.Fatalf("unexpected cache options: %+v", c)
	}
}
