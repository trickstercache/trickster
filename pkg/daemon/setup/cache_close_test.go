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

package setup

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	tsmoptions "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	bp "github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/manager"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

const (
	cacheCloseWait = 5 * time.Second
	cacheProbeKey  = "probe"
)

func cacheClosed(c cache.Cache) bool {
	return errors.Is(c.Store(cacheProbeKey, []byte(cacheProbeKey), time.Minute), manager.ErrCacheClosed)
}

func bboltCacheOptions(name, path string) *cacheoptions.Options {
	o := cacheoptions.New()
	o.Name = name
	o.Provider, o.ProviderID = providers.BBolt, providers.BBoltID
	o.BBolt.Filename = path
	return o
}

func memoryCacheOptions(name string) *cacheoptions.Options {
	o := cacheoptions.New()
	o.Name = name
	o.Provider, o.ProviderID = providers.Memory, providers.MemoryID
	return o
}

func albOptions(mechanism, cacheName string, global bool) *bo.Options {
	o := bo.New()
	o.Provider, o.CacheName = bp.ALB, cacheName
	o.ALBOptions = &ao.Options{MechanismName: mechanism}
	if global {
		o.ALBOptions.TSMOptions.Evaluation = tsmoptions.EvaluationGlobal
	}
	return o
}

func TestReplacedCachesCloseOnceTheOldClientsRetire(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cache.db")
	oc := config.NewConfig()
	oc.Caches["default"] = memoryCacheOptions("default")
	oc.Caches["gone"] = cacheoptions.New()
	oc.Caches["gone"].Name = "gone"
	oc.Caches["kept"] = cacheoptions.New()
	oc.Caches["kept"].Name = "kept"
	oc.Caches["file"] = bboltCacheOptions("file", file)
	// caches no global ALB stores in: one a merging ALB and a plain backend use, and one only an ALB of another mechanism names;
	// neither of the others evaluates globally, whatever their tsm blocks say
	oc.Caches["plain"] = memoryCacheOptions("plain")
	oc.Caches["dropped"] = memoryCacheOptions("dropped")
	oc.Backends["default"].CacheName = "plain"
	oc.Backends["default"].ALBOptions = albOptions(names.MechanismTSM, "", true).ALBOptions
	oc.Backends["merge"] = albOptions(names.MechanismTSM, "plain", false)
	oc.Backends["rr"] = albOptions(names.MechanismRoundRobin, "dropped", true)
	for _, name := range []string{"default", "gone", "kept", "file"} {
		oc.Backends["global-"+name] = albOptions(names.MechanismTSM, name, true)
	}
	old := cache.Lookup{}
	for name, o := range oc.Caches {
		old[name] = registry.NewCache(name, o)
	}
	si := &instance.ServerInstance{Config: oc, Caches: old}

	nc := config.NewConfig()
	// the drain bounds only the operations under way when a cache closes, so it doesn't hold back one that has none
	nc.MgmtConfig.ReloadDrainTimeout = timeconv.Duration(time.Hour)
	nc.Caches["default"] = memoryCacheOptions("default")
	nc.Caches["default"].UseCacheChunking = true
	nc.Caches["kept"] = oc.Caches["kept"].Clone()
	// the file cache's replacement opens the same file
	nc.Caches["file"] = bboltCacheOptions("file", file)
	nc.Caches["file"].TimeseriesChunkFactor++
	nc.Caches["plain"] = memoryCacheOptions("plain")
	nc.Caches["plain"].UseCacheChunking = true
	caches := applyCachingConfig(si, nc)
	t.Cleanup(func() {
		for _, c := range caches {
			_ = c.Close()
		}
	})
	require.Same(t, old["kept"], caches["kept"])
	require.NotSame(t, old["default"], caches["default"])
	require.NotSame(t, old["plain"], caches["plain"])
	require.NotContains(t, caches, "gone")
	require.NotContains(t, caches, "dropped")

	// a cache keeping its objects in a file closes as the reload applies, so its replacement can open that file
	require.Eventually(t, func() bool { return cacheClosed(old["file"]) }, cacheCloseWait, time.Millisecond)
	require.NoError(t, caches["file"].Store(cacheProbeKey, []byte(cacheProbeKey), time.Minute))
	// as does one no global ALB stores in, which no old client stores to once its requests have drained
	require.Eventually(t, func() bool { return cacheClosed(old["plain"]) && cacheClosed(old["dropped"]) },
		cacheCloseWait, time.Millisecond)
	// the others serve the old global ALBs until they retire
	for _, name := range []string{"default", "gone", "kept"} {
		require.False(t, cacheClosed(old[name]), name)
	}
	CloseReplacedCaches(oc, old, caches, time.Second)
	require.Eventually(t, func() bool { return cacheClosed(old["default"]) && cacheClosed(old["gone"]) },
		cacheCloseWait, time.Millisecond)
	require.Never(t, func() bool { return cacheClosed(old["kept"]) }, 50*time.Millisecond, time.Millisecond,
		"a cache the new configuration keeps was closed")
	CloseReplacedCaches(nil, old, caches, time.Second)
	CloseReplacedCaches(oc, nil, caches, time.Second)
}
