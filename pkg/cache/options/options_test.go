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
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	strutil "github.com/trickstercache/trickster/v2/pkg/util/strings"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"
)

func TestNew(t *testing.T) {
	o := New()
	if o == nil {
		t.Error("expected non-nil options")
	}
}

func TestCloneAndEqual(t *testing.T) {

	o := New()
	o2 := o.Clone()

	if !o.Equal(o2) {
		t.Error("expected true")
	}

	if o.Equal(nil) {
		t.Error("expected false")
	}

}

func TestSetDefaults(t *testing.T) {

	// SetDefaults(metadata yamlx.KeyLookup,
	//activeCaches map[string]interface{})
	// ([]string, error) {

	o := New()
	l := Lookup{"default": o}
	_, err := l.SetDefaults(nil, nil)
	if err != nil {
		t.Error()
	}

	kl, err := yamlx.GetKeyList(testYAML)
	if err != nil {
		t.Error(err)
	}

	o.Provider = "Redis"
	o.ProviderID = providers.Redis
	l = Lookup{"default": o}

	ac := strutil.Lookup{"default": nil}
	lw, err := l.SetDefaults(kl, ac)
	if err != nil {
		t.Error()
	}

	if len(lw) != 1 {
		t.Errorf("expected %d got %d", 1, len(lw))
	}

	ty := strings.Replace(
		strings.Replace(testYAML,
			"client_type: standard", "client_type: sentinel", -1),
		"endpoints: [ 127.0.0.1:6839 ]", "endpoint: 127.0.0.1:6839", -1)

	kl, err = yamlx.GetKeyList(ty)
	if err != nil {
		t.Error(err)
	}

	l = Lookup{"default": o}
	o.Redis.ClientType = "sentinel"
	ac = strutil.Lookup{"default": nil}
	lw, err = l.SetDefaults(kl, ac)
	if err != nil {
		t.Error()
	}
	if len(lw) != 1 {
		t.Errorf("expected %d got %d", 1, len(lw))
	}

	l = Lookup{"default": o}
	o.Index.MaxSizeBackoffBytes = 16384
	o.Index.MaxSizeBytes = 1
	_, err = l.SetDefaults(kl, ac)
	if err != errMaxSizeBackoffBytesTooBig {
		t.Error(err)
	}

	l = Lookup{"default": o}
	o.Index.MaxSizeBackoffBytes = 16384
	o.Index.MaxSizeBytes = 32768
	o.Index.MaxSizeBackoffObjects = 32768
	o.Index.MaxSizeObjects = 16384

	_, err = l.SetDefaults(kl, ac)
	if err != errMaxSizeBackoffObjectsTooBig {
		t.Error(err)
	}

}

const testYAML = `
caches:
  default:
    provider: redis
    redis:
      client_type: standard
      protocol: tcp
      endpoints: [ 127.0.0.1:6839 ]
      client_type: sentinel
      sentinel_master: 127.0.0.1:6839
      password: '********'
      db: trickster
      max_retries: 3
      min_retry_backoff_ms: 2000
      max_retry_backoff_ms: 4000
      dial_timeout_ms: 2000
      read_timeout_ms: 1000
      write_timeout_ms: 3000
      pool_size: 16
      min_idle_conns: 16
      max_conn_age_ms: 16
      pool_timeout_ms: 16
      idle_timeout_ms: 16
      idle_check_frequency_ms: 16
    filesystem:
      cache_path: /tmp/trickster
    bbolt:
      filename: trickster.bbolt.db
      bucket: trickster
    badger:
      directory: /tmp/trickster
      value_directory: /tmp/trickster
    index:
      reap_interval_ms: 2000
      flush_interval_ms: 2000
      max_size_bytes: 1
      max_size_backoff_bytes: 16384
      max_size_objects: 4096
      max_size_backoff_objects: 24

`
