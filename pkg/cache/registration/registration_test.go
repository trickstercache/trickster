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

package registration

import (
	"testing"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	bao "github.com/trickstercache/trickster/v2/pkg/cache/badger/options"
	bbo "github.com/trickstercache/trickster/v2/pkg/cache/bbolt/options"
	flo "github.com/trickstercache/trickster/v2/pkg/cache/filesystem/options"
	io "github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

func TestLoadCachesFromConfig(t *testing.T) {

	conf, _, err := config.Load("trickster", "test",
		[]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	for key, v := range providers.Names {
		cfg := newCacheConfig(t, key)
		conf.Caches[key] = cfg
		switch v {
		case providers.Bbolt:
			cfg.BBolt.Filename = t.TempDir() + "/" + key + "-testcache.db"
		case providers.Filesystem:
			cfg.BBolt.Filename = t.TempDir() + "/" + key + "-testcache"
		case providers.BadgerDB:
			cfg.BBolt.Filename = t.TempDir() + "/" + key + "-testcache"
		}
	}

	caches := LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer CloseCaches(caches)
	_, ok := caches["default"]
	if !ok {
		t.Errorf("Could not find default configuration")
	}

	for key := range providers.Names {
		_, ok := caches[key]
		if !ok {
			t.Errorf("Could not find the configuration for %q", key)
		}
	}

	_, ok = caches["foo"]
	if ok {
		t.Errorf("expected error")
	}

}

func newCacheConfig(t *testing.T, cacheProvider string) *co.Options {

	bd := "."
	fd := "."

	ctid, ok := providers.Names[cacheProvider]
	if !ok {
		ctid = providers.Memory
	}

	switch ctid {
	case providers.BadgerDB:
		bd = t.TempDir() + "/" + cacheProvider
	case providers.Filesystem:
		fd = t.TempDir() + "/" + cacheProvider
	}

	return &co.Options{
		Provider:   cacheProvider,
		Redis:      &ro.Options{Protocol: "tcp", Endpoint: "redis:6379", Endpoints: []string{"redis:6379"}},
		Filesystem: &flo.Options{CachePath: fd},
		BBolt:      &bbo.Options{Filename: "/tmp/test.db", Bucket: "trickster_test"},
		Badger:     &bao.Options{Directory: bd, ValueDirectory: bd},
		Index: &io.Options{
			ReapIntervalMS:        3000,
			FlushIntervalMS:       5000,
			MaxSizeBytes:          536870912,
			MaxSizeBackoffBytes:   16777216,
			MaxSizeObjects:        0,
			MaxSizeBackoffObjects: 100,
		},
	}
}
