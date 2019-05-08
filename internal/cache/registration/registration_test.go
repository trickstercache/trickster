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
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	metrics.Init()
}

const Bbolt = `bbolt`
const Memory = `memory`
const Filesystem = `filesystem`
const Badger = `badger`
const Redis = `redis`

func TestLoadCachesFromConfig(t *testing.T) {

	err := config.Load("trickster", "test", []string{"-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cacheTypes := []string{Memory, Bbolt, Filesystem, Badger, Redis}
	for _, key := range cacheTypes {
		cfg := newCacheConfig(t, key)
		config.Caches[key] = cfg
		switch key {
		case Bbolt:
			defer os.RemoveAll(cfg.BBolt.Filename)
		case Filesystem:
			defer os.RemoveAll(cfg.Filesystem.CachePath)
		case Badger:
			defer os.RemoveAll(cfg.Badger.Directory)
		}
	}

	LoadCachesFromConfig()
	_, err = GetCache("default")
	if err != nil {
		t.Error(err)
	}

	for _, key := range cacheTypes {
		_, err = GetCache(key)
		if err != nil {
			t.Error(err)
		}
	}

	_, err = GetCache("foo")
	if err == nil {
		t.Errorf("expected error")
	}

}

func newCacheConfig(t *testing.T, cacheType string) *config.CachingConfig {

	bd := "."
	fd := "."
	var err error

	switch cacheType {
	case Badger:
		bd, err = ioutil.TempDir("/tmp", Badger)
		if err != nil {
			t.Error(err)
		}

	case Filesystem:
		fd, err = ioutil.TempDir("/tmp", Filesystem)
		if err != nil {
			t.Error(err)
		}
	}

	return &config.CachingConfig{
		Type:               cacheType,
		Compression:        true,
		TimeseriesTTLSecs:  21600,
		FastForwardTTLSecs: 15,
		ObjectTTLSecs:      30,
		Redis:              config.RedisCacheConfig{Protocol: "tcp", Endpoint: "redis:6379", Endpoints: []string{"redis:6379"}},
		Filesystem:         config.FilesystemCacheConfig{CachePath: fd},
		BBolt:              config.BBoltCacheConfig{Filename: "/tmp/test.db", Bucket: "trickster_test"},
		Badger:             config.BadgerCacheConfig{Directory: bd, ValueDirectory: bd},
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
