/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"io/ioutil"
	"os"
	"testing"

	bao "github.com/Comcast/trickster/internal/cache/badger/options"
	bbo "github.com/Comcast/trickster/internal/cache/bbolt/options"
	flo "github.com/Comcast/trickster/internal/cache/filesystem/options"
	io "github.com/Comcast/trickster/internal/cache/index/options"
	co "github.com/Comcast/trickster/internal/cache/options"
	ro "github.com/Comcast/trickster/internal/cache/redis/options"
	"github.com/Comcast/trickster/internal/cache/types"
	"github.com/Comcast/trickster/internal/config"
	tl "github.com/Comcast/trickster/internal/util/log"
)

func TestLoadCachesFromConfig(t *testing.T) {

	conf, _, err := config.Load("trickster", "test", []string{"-log-level", "debug", "-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	for key, v := range types.Names {
		cfg := newCacheConfig(t, key)
		conf.Caches[key] = cfg
		switch v {
		case types.CacheTypeBbolt:
			defer os.RemoveAll(cfg.BBolt.Filename)
		case types.CacheTypeFilesystem:
			defer os.RemoveAll(cfg.Filesystem.CachePath)
		case types.CacheTypeBadgerDB:
			defer os.RemoveAll(cfg.Badger.Directory)
		}
	}

	caches := LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer CloseCaches(caches)
	_, ok := caches["default"]
	if !ok {
		t.Errorf("Could not find default configuration")
	}

	for key := range types.Names {
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

func newCacheConfig(t *testing.T, cacheType string) *co.Options {

	bd := "."
	fd := "."
	var err error

	ctid, ok := types.Names[cacheType]
	if !ok {
		ctid = types.CacheTypeMemory
	}

	switch ctid {
	case types.CacheTypeBadgerDB:
		bd, err = ioutil.TempDir("/tmp", cacheType)
		if err != nil {
			t.Error(err)
		}

	case types.CacheTypeFilesystem:
		fd, err = ioutil.TempDir("/tmp", cacheType)
		if err != nil {
			t.Error(err)
		}
	}

	return &co.Options{
		CacheType:  cacheType,
		Redis:      &ro.Options{Protocol: "tcp", Endpoint: "redis:6379", Endpoints: []string{"redis:6379"}},
		Filesystem: &flo.Options{CachePath: fd},
		BBolt:      &bbo.Options{Filename: "/tmp/test.db", Bucket: "trickster_test"},
		Badger:     &bao.Options{Directory: bd, ValueDirectory: bd},
		Index: &io.Options{
			ReapIntervalSecs:      3,
			FlushIntervalSecs:     5,
			MaxSizeBytes:          536870912,
			MaxSizeBackoffBytes:   16777216,
			MaxSizeObjects:        0,
			MaxSizeBackoffObjects: 100,
		},
	}
}
