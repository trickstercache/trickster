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

	ct "github.com/trickstercache/trickster/v2/pkg/config/types"
	"gopkg.in/yaml.v2"
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
		Password:        ct.EnvString("secret"),
		MinRetryBackoff: time.Second,
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
