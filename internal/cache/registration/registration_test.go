/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/Comcast/trickster/internal/config"
)

func TestLoadCachesFromConfig(t *testing.T) {

	conf, _, err := config.Load("trickster", "test", []string{"-log-level", "debug", "-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	for key, v := range config.CacheTypeNames {
		cfg := newCacheConfig(t, key)
		conf.Caches[key] = cfg
		switch v {
		case config.CacheTypeBbolt:
			defer os.RemoveAll(cfg.BBolt.Filename)
		case config.CacheTypeFilesystem:
			defer os.RemoveAll(cfg.Filesystem.CachePath)
		case config.CacheTypeBadgerDB:
			defer os.RemoveAll(cfg.Badger.Directory)
		}
	}

	caches := LoadCachesFromConfig(conf)
	defer CloseCaches(caches)
	_, ok := caches["default"]
	if !ok {
		t.Errorf("Could not find default configuration")
	}

	for key := range config.CacheTypeNames {
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

func newCacheConfig(t *testing.T, cacheType string) *config.CachingConfig {

	bd := "."
	fd := "."
	var err error

	ctid, ok := config.CacheTypeNames[cacheType]
	if !ok {
		ctid = config.CacheTypeMemory
	}

	switch ctid {
	case config.CacheTypeBadgerDB:
		bd, err = ioutil.TempDir("/tmp", cacheType)
		if err != nil {
			t.Error(err)
		}

	case config.CacheTypeFilesystem:
		fd, err = ioutil.TempDir("/tmp", cacheType)
		if err != nil {
			t.Error(err)
		}
	}

	return &config.CachingConfig{
		CacheType:  cacheType,
		Redis:      config.RedisCacheConfig{Protocol: "tcp", Endpoint: "redis:6379", Endpoints: []string{"redis:6379"}},
		Filesystem: config.FilesystemCacheConfig{CachePath: fd},
		BBolt:      config.BBoltCacheConfig{Filename: "/tmp/test.db", Bucket: "trickster_test"},
		Badger:     config.BadgerCacheConfig{Directory: bd, ValueDirectory: bd},
		Index: config.CacheIndexConfig{
			ReapIntervalSecs:      3,
			FlushIntervalSecs:     5,
			MaxSizeBytes:          536870912,
			MaxSizeBackoffBytes:   16777216,
			MaxSizeObjects:        0,
			MaxSizeBackoffObjects: 100,
		},
	}
}
