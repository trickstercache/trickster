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

package manager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/memory"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
)

func TestNewCache(t *testing.T) {
	opts := CacheOptions{
		UseIndex: true,
	}
	c := NewCache(nil, opts, nil)
	require.NotNil(t, c)
	require.Equal(t, opts, c.(*Manager).opts)
}

func TestManager(t *testing.T) {
	opts := CacheOptions{
		UseIndex: true,
	}
	cacheConfig := co.Options{Provider: "memory"}
	mc := memory.New("test", &cacheConfig)
	c := NewCache(mc, opts, &cacheConfig)

	t.Run("create/read", func(t *testing.T) {
		key := "foo"
		require.NoError(t, c.Store(key, []byte("bar"), 0))
		b, s, err := c.Retrieve(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, []byte("bar"), b)
	})

	t.Run("create/read/delete", func(t *testing.T) {
		key := "foo"
		require.NoError(t, c.Store(key, []byte("bar"), 0))
		b, s, err := c.Retrieve(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, []byte("bar"), b)
		require.NoError(t, c.Remove(key))
		b, s, err = c.Retrieve(key)
		require.ErrorContains(t, err, "key not found in cache")
		require.Equal(t, status.LookupStatusKeyMiss, s)
		require.Len(t, b, 0)
	})

	t.Run("create/update/read", func(t *testing.T) {
		key := "foo"
		require.NoError(t, c.Store(key, []byte("bar"), 0))
		b, s, err := c.Retrieve(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, []byte("bar"), b)
		require.NoError(t, c.Store(key, []byte("baz"), 0))
		b, s, err = c.Retrieve(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, []byte("baz"), b)
	})

	t.Run("reference", func(t *testing.T) {
		mc := c.(cache.MemoryCache)
		key := "foo"
		val := object{"bar"}
		require.NoError(t, mc.StoreReference(key, &val, 0))
		v, s, err := mc.RetrieveReference(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, val, *v.(*object))
	})

}

type object struct {
	field string
}

func (o *object) Size() int {
	return len(o.field)
}
