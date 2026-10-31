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
	"slices"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/secret"

	"go.yaml.in/yaml/v3"
)

func TestEqual(t *testing.T) {
	t.Parallel()

	o := New()
	if !o.Equal(o) {
		t.Fatal("expected options equal to self")
	}
	if o.Equal(nil) {
		t.Fatal("expected false for nil comparison")
	}

	o2 := &Options{
		ClientType: DefaultRedisClientType,
		Protocol:   DefaultRedisProtocol,
		Endpoint:   DefaultRedisEndpoint,
		Endpoints:  []string{DefaultRedisEndpoint},
	}
	o2.Endpoint = "other:6379"
	if o.Equal(o2) {
		t.Fatal("expected endpoint difference to make options unequal")
	}

	o3 := &Options{
		ClientType:      DefaultRedisClientType,
		Protocol:        DefaultRedisProtocol,
		Endpoint:        DefaultRedisEndpoint,
		Endpoints:       []string{DefaultRedisEndpoint},
		Password:        secret.Secret("secret"),
		MinRetryBackoff: timeconv.Duration(time.Second),
		UseTLS:          true,
	}
	if !o3.Equal(o3) {
		t.Fatal("expected custom options to be equal to self")
	}
}

func TestEqualStringSlices(t *testing.T) {
	t.Parallel()

	if !equalStringSlices([]string{"a", "b"}, []string{"a", "b"}) {
		t.Fatal("expected equal slices")
	}
	if equalStringSlices([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("expected unequal slice lengths")
	}
	if equalStringSlices([]string{"a"}, []string{"b"}) {
		t.Fatal("expected unequal slice values")
	}
}

func TestUnmarshalYAML(t *testing.T) {
	t.Parallel()

	const raw = `
caches:
  default:
    redis:
      client_type: sentinel
      endpoint: redis:6379
      password: secret
`
	type doc struct {
		Caches map[string]struct {
			Redis *Options `yaml:"redis"`
		} `yaml:"caches"`
	}
	var d doc
	if err := yaml.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	o := d.Caches["default"].Redis
	if o == nil || o.ClientType != "sentinel" || o.Password != "secret" {
		t.Fatalf("unexpected redis options: %+v", o)
	}
}

func TestDecodeYAMLKeepsUnsetValues(t *testing.T) {
	t.Parallel()
	o := &Options{Endpoint: "kept:6379", DB: 4}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("db: 2\n"), &n); err != nil {
		t.Fatal(err)
	}
	if err := o.DecodeYAML(n.Content[0]); err != nil {
		t.Fatal(err)
	}
	if o.Endpoint != "kept:6379" || o.DB != 2 {
		t.Fatalf("unexpected options: %+v", o)
	}
	if err := yaml.Unmarshal([]byte("db: [1]\n"), &n); err != nil {
		t.Fatal(err)
	}
	if err := o.DecodeYAML(n.Content[0]); err == nil {
		t.Fatal("expected an error decoding a sequence into db")
	}
}

func TestIneffectivePoolKeys(t *testing.T) {
	t.Parallel()
	if keys := New().IneffectivePoolKeys(false); len(keys) != 0 {
		t.Fatalf("expected no keys for defaults, got %v", keys)
	}
	o := &Options{PoolSize: 4, MinIdleConns: 1, ConnMaxIdleTime: timeconv.Duration(time.Minute),
		PoolTimeout: timeconv.Duration(time.Second)}
	if keys := o.IneffectivePoolKeys(true); !slices.Equal(keys, []string{KeyPoolTimeout}) {
		t.Fatalf("expected only pool_timeout when pooled, got %v", keys)
	}
	want := []string{KeyPoolSize, KeyMinIdleConns, KeyIdleTimeout, KeyPoolTimeout}
	if keys := o.IneffectivePoolKeys(false); !slices.Equal(keys, want) {
		t.Fatalf("expected %v when not pooled, got %v", want, keys)
	}
}

func TestClone(t *testing.T) {
	t.Parallel()
	var nilOpts *Options
	if nilOpts.Clone() != nil {
		t.Fatal("expected nil clone of nil options")
	}
	if nilOpts.Equal(New()) {
		t.Fatal("expected nil options to differ from defaults")
	}
	o := New()
	c := o.Clone()
	if !c.Equal(o) {
		t.Fatal("expected the clone to equal its source")
	}
	c.Endpoints[0] = "changed:6379"
	if o.Endpoints[0] != DefaultRedisEndpoint {
		t.Fatal("expected the clone's endpoints to be a separate slice")
	}
}
