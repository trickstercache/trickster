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

package registry

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/bbolt"
	bbo "github.com/trickstercache/trickster/v2/pkg/cache/bbolt/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/filesystem"
	flo "github.com/trickstercache/trickster/v2/pkg/cache/filesystem/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	io "github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/manager"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

const (
	diskObjects = 25
	diskTimeout = 10 * time.Second
)

func diskConfig(dir, provider string) *co.Options {
	return &co.Options{
		Name:       "disk",
		Provider:   provider,
		Filesystem: &flo.Options{CachePath: filepath.Join(dir, "fs")},
		BBolt:      &bbo.Options{Filename: filepath.Join(dir, "disk.db"), Bucket: "disk"},
		Index: &io.Options{
			ReapInterval:   timeconv.Duration(time.Hour),
			FlushInterval:  timeconv.Duration(time.Hour),
			IndexExpiry:    timeconv.Duration(time.Hour),
			ScanBatchSize:  4,
			ScanBatchPause: timeconv.Duration(time.Millisecond),
		},
	}
}

// the store of the cache's own files, opened apart from the cache
func metaStore(t *testing.T, cfg *co.Options) (blob.MetaStore, func()) {
	t.Helper()
	var s interface {
		blob.Store
		blob.MetaStore
	}
	if cfg.Provider == providers.Filesystem {
		s = filesystem.NewStore(cfg.Name, cfg)
	} else {
		s = bbolt.NewStore(cfg.Name, "", "", cfg)
	}
	require.NoError(t, s.Connect())
	return s, func() { require.NoError(t, s.Close()) }
}

func indexOf(t *testing.T, c cache.Cache) *index.IndexedClient {
	t.Helper()
	idx, ok := c.(*manager.Manager).Client.(*index.IndexedClient)
	require.True(t, ok)
	return idx
}

// a disk cache's index outlasts the cache being closed, and is rebuilt from the cache when lost
func TestDiskCacheIndexLifecycle(t *testing.T) {
	for _, provider := range []string{providers.Filesystem, providers.BBolt} {
		t.Run(provider, func(t *testing.T) {
			cfg := diskConfig(t.TempDir(), provider)
			c := NewCache(cfg.Name, cfg)
			var size int64
			for i := range diskObjects {
				require.NoError(t, c.Store("key-"+strconv.Itoa(i), make([]byte, i+1), time.Hour))
				size += int64(i + 1)
			}
			idx := indexOf(t, c)
			require.True(t, idx.SupportsSplit())
			require.True(t, idx.SupportsStream())
			require.Equal(t, int64(diskObjects), idx.Count())
			require.NoError(t, c.Close())

			// closed in good order, the index is as it was when the cache is opened again
			c = NewCache(cfg.Name, cfg)
			idx = indexOf(t, c)
			require.Equal(t, int64(diskObjects), idx.Count())
			require.Equal(t, size, idx.Size())
			b, s, err := c.Retrieve("key-4")
			require.NoError(t, err)
			require.Equal(t, status.LookupStatusHit, s)
			require.Len(t, b, 5)
			require.NoError(t, c.Close())

			// the index is lost, and the objects are not
			ms, done := metaStore(t, cfg)
			names, err := ms.ListMeta()
			require.NoError(t, err)
			require.NotEmpty(t, names)
			require.NoError(t, ms.RemoveMeta(names...))
			done()

			c = NewCache(cfg.Name, cfg)
			t.Cleanup(func() { c.Close() })
			idx = indexOf(t, c)
			require.Eventually(t, func() bool {
				return idx.Count() == diskObjects && idx.Size() == size
			}, diskTimeout, time.Millisecond, "the index is rebuilt from the cache")
		})
	}
}
