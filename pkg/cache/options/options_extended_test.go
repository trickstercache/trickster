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
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"go.yaml.in/yaml/v3"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", reserved.ReferenceNone} {
		o := New()
		o.Name = name
		if ok, err := o.Validate(); ok || err != ErrInvalidName {
			t.Fatalf("Validate(%q) = (%v, %v), want invalid name", name, ok, err)
		}
	}

	o := New()
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

	for name, change := range map[string]func(o *Options){
		"scan interval":    func(o *Options) { o.Index.ScanInterval = -1 },
		"scan batch size":  func(o *Options) { o.Index.ScanBatchSize = -1 },
		"scan batch pause": func(o *Options) { o.Index.ScanBatchPause = -1 },
	} {
		o = New()
		o.Name = "default"
		change(o)
		if ok, err := o.Validate(); ok || err != errNegativeScanOption {
			t.Fatalf("Validate negative %s = (%v, %v)", name, ok, err)
		}
	}

	o = New()
	o.Name = "default"
	o.Filesystem.MinFreeBytes = -1
	if ok, err := o.Validate(); ok || err != errNegativeMinFreeBytes {
		t.Fatalf("Validate negative min free bytes = (%v, %v)", ok, err)
	}

	o = New()
	o.Name = "default"
	o.Index.ScanInterval = 0
	o.Filesystem = nil
	if ok, err := o.Validate(); !ok || err != nil {
		t.Fatalf("Validate(no scan interval, no filesystem) = (%v, %v)", ok, err)
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

func TestUseCacheChunkingSurvivesInitialize(t *testing.T) {
	t.Parallel()

	const (
		chunked   = "chunked"
		unchunked = "unchunked"
	)
	const raw = `
caches:
  chunked:
    provider: memory
    use_cache_chunking: true
  unchunked:
    provider: memory
`
	var d struct {
		Caches Lookup `yaml:"caches"`
	}
	if err := yaml.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if _, err := d.Caches.Initialize(sets.New([]string{chunked, unchunked})); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	c, u := d.Caches[chunked], d.Caches[unchunked]
	if !c.UseCacheChunking {
		t.Error("use_cache_chunking: true was reset by Initialize")
	}
	if u.UseCacheChunking {
		t.Error("an unset use_cache_chunking must stay false")
	}

	cl := c.Clone()
	if !cl.UseCacheChunking || !c.Equal(cl) {
		t.Error("Clone dropped use_cache_chunking")
	}
	cl.UseCacheChunking = false
	if c.Equal(cl) {
		t.Error("Equal ignored a use_cache_chunking difference")
	}
}

func decodeCaches(t *testing.T, raw string) Lookup {
	t.Helper()
	var d struct {
		Caches Lookup `yaml:"caches"`
	}
	if err := yaml.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	return d.Caches
}

func TestValkeyProvider(t *testing.T) {
	t.Parallel()

	l := decodeCaches(t, `
caches:
  vk:
    provider: valkey
    valkey:
      client_type: cluster
      endpoints: [v0:6379]
      db: 2
      auto_pipelining: false
      pool_size: 8
      pool_timeout: 1s
`)
	warnings, err := l.Initialize(sets.New([]string{"vk"}))
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	o := l["vk"]
	if o.ProviderID != providers.ValkeyID || o.Redis != nil || o.Valkey == nil || o.Index != nil {
		t.Fatalf("unexpected provider wiring: %+v", o)
	}
	if o.Valkey.DB != 2 || o.Valkey.AutoPipelining {
		t.Fatalf("unexpected valkey options: %+v", o.Valkey)
	}
	// a pooled valkey cache uses its pool keys, but pool_timeout has no effect, and its db is no warning
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want only the pool_timeout warning", warnings)
	}
	if err := l.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	c := o.Clone()
	if !o.Equal(c) {
		t.Fatal("valkey options should be equal to clone")
	}
	c.Valkey.Endpoints[0] = "other"
	if o.Equal(c) || o.Valkey.Endpoints[0] != "v0:6379" {
		t.Fatal("expected a deep clone whose difference makes the options unequal")
	}
	o.ClearProviderOptions()
	if o.Valkey != nil {
		t.Fatal("expected valkey options to be cleared")
	}
}

func TestValkeyProviderDefaultsAndPoolWarnings(t *testing.T) {
	t.Parallel()

	l := decodeCaches(t, `
caches:
  vk:
    provider: valkey
    valkey:
      auto_pipelining: true
      pool_size: 8
      min_idle_conns: 2
      idle_timeout: 1m
`)
	warnings, err := l.Initialize(sets.New([]string{"vk"}))
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := l["vk"].Valkey.Endpoint; got != "valkey:6379" {
		t.Fatalf("endpoint = %q, want the valkey default", got)
	}
	// a pipelining valkey cache doesn't use the pool keys
	if len(warnings) != 3 {
		t.Fatalf("warnings = %v, want one per pool key", warnings)
	}

	// a valkey cache without a valkey key gets the valkey defaults
	o := New()
	o.Provider = providers.Valkey
	o.Valkey = nil
	if err := o.Initialize("bare"); err != nil || o.Valkey == nil {
		t.Fatalf("Initialize bare valkey: %v %+v", err, o.Valkey)
	}
}

func TestRedisClusterDBWarning(t *testing.T) {
	t.Parallel()

	o := New()
	o.Provider = providers.Redis
	o.Redis.ClientType = "cluster"
	o.Redis.DB = 3
	warnings, err := Lookup{"c": o}.Initialize(sets.New([]string{"c"}))
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want the cluster db warning", warnings)
	}
}

func TestProviderKeysDoNotMix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"redis with redis", "provider: redis\n    redis:\n      endpoint: r:6379\n", true},
		{"valkey with valkey", "provider: valkey\n    valkey:\n      endpoint: v:6379\n", true},
		{"valkey without a key", "provider: valkey\n", true},
		{"redis with valkey", "provider: redis\n    valkey:\n      endpoint: v:6379\n", false},
		{"memory with valkey", "provider: memory\n    valkey:\n      endpoint: v:6379\n", false},
		{"valkey with redis", "provider: valkey\n    redis:\n      endpoint: r:6379\n", false},
		{"valkey with a bad setting", "provider: valkey\n    valkey:\n      pipeline_multiplex: 9\n", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			l := decodeCaches(t, "caches:\n  c:\n    "+test.raw)
			if _, err := l.Initialize(sets.New([]string{"c"})); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			if err := l.Validate(); (err == nil) != test.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, test.ok)
			}
		})
	}
}

func TestInitializeFillsMissingProviderBlocks(t *testing.T) {
	t.Parallel()
	for _, p := range []string{providers.Redis, providers.Valkey, providers.Filesystem,
		providers.BBolt, providers.BadgerDB, providers.Memory} {
		o := New()
		o.ClearProviderOptions()
		o.Provider = p
		o.TimeseriesChunkFactor, o.ByterangeChunkSize = 0, 0
		if err := o.Initialize("default"); err != nil {
			t.Fatalf("Initialize %s: %v", p, err)
		}
		c := o.Clone()
		if !o.Equal(c) {
			t.Fatalf("%s options should equal their clone", p)
		}
		if o.TimeseriesChunkFactor == 0 || o.ByterangeChunkSize == 0 {
			t.Fatalf("%s chunk defaults were not set", p)
		}
		c.Name = "other"
		if o.Equal(c) {
			t.Fatalf("%s options should differ by name", p)
		}
	}
}

func TestEqualIndexMismatch(t *testing.T) {
	t.Parallel()
	o := New()
	o2 := o.Clone()
	o2.Index = nil
	if o.Equal(o2) || o.Equal(nil) {
		t.Fatal("expected a missing index or nil options to make options unequal")
	}
}

func TestUnmarshalYAMLError(t *testing.T) {
	t.Parallel()
	var o Options
	if err := yaml.Unmarshal([]byte("provider: [redis]\n"), &o); err == nil {
		t.Fatal("expected an error decoding a sequence into provider")
	}
}
