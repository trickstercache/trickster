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

package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/manager"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/daemon/setup"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"

	"github.com/stretchr/testify/require"
)

const (
	retireTestMember = "member"
	cacheProbe       = "probe"
	retireTestRounds = 3
	retireTestWait   = 5 * time.Second
	// retireTestDrain is the reload drain storeAPIConfig sets
	retireTestDrain = 300 * time.Millisecond
)

// storeAPIConfig returns a config of one Thanos member read over StoreAPI at port, with every listener left unbound and
// the drain the old clients' requests get after each reload
func storeAPIConfig(port int) string {
	return fmt.Sprintf(`
listeners:
  default:
    port: 0
  mgmt:
    port: 0
  metrics:
    port: 0
mgmt:
  reload_drain_timeout: %s
backends:
  %s:
    provider: prometheus
    origin_url: 'http://thanos-query:10904'
    prometheus:
      flavor: thanos
      series_endpoint: 'grpc://thanos-sidecar:%d'
`, retireTestDrain, retireTestMember, port)
}

func retired(member backends.Backend) bool {
	_, err := member.(backends.TSMGlobalSourceProvider).TSMGlobalSource()
	return err != nil
}

func TestReloadRetiresEachGenerationAfterItsDrain(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, storeAPIConfig(10901))
	conf, clients, err := setup.BootstrapConfig("-config", path)
	require.NoError(t, err)
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	si := &instance.ServerInstance{Listeners: group}
	require.NoError(t, setup.ApplyConfig(si, conf, clients, nil, nil, group))
	t.Cleanup(func() { setup.Shutdown(si) })
	modified := time.Now()
	for round := 1; round <= retireTestRounds; round++ {
		member := si.Backends[retireTestMember]
		// the source a global evaluation would have read the member through
		source, err := member.(backends.TSMGlobalSourceProvider).TSMGlobalSource()
		require.NoError(t, err)
		writeConfig(t, dir, storeAPIConfig(10901+round))
		modified = modified.Add(time.Second)
		require.NoError(t, os.Chtimes(path, modified, modified))
		ok, err := Reload(si, "test", "-config", path)
		require.NoError(t, err)
		require.True(t, ok)
		require.NotSame(t, member, si.Backends[retireTestMember])
		require.Never(t, func() bool { return retired(member) }, retireTestDrain/3, time.Millisecond,
			"the old generation's requests get their drain")
		require.Eventually(t, func() bool { return retired(member) }, retireTestWait, time.Millisecond)
		require.Error(t, source.(io.Closer).Close(), "the old generation's connection was closed")
		require.False(t, retired(si.Backends[retireTestMember]))
	}
}

// a backend for storeAPIConfig evaluating queries over its member's raw series, which it caches in default
const globalALBConfig = `
  global:
    provider: alb
    alb:
      mechanism: tsm
      pool: [member]
      tsm:
        evaluation: global
`

// cachedStoreAPIConfig is storeAPIConfig with a memory cache, chunked when chunked is set, which a change rebuilds, and with
// globalALBConfig when global is set
func cachedStoreAPIConfig(port int, chunked, global bool) string {
	conf := storeAPIConfig(port)
	if global {
		conf += globalALBConfig
	}
	return conf + fmt.Sprintf(`
caches:
  default:
    provider: memory
    use_cache_chunking: %t
`, chunked)
}

func cacheClosed(c cache.Cache) bool {
	return errors.Is(c.Store(cacheProbe, []byte(cacheProbe), time.Minute), manager.ErrCacheClosed)
}

func reloadCache(t *testing.T, global bool) (backends.Backend, cache.Cache, *instance.ServerInstance) {
	t.Helper()
	// applies the configuration, then reloads it with its cache rebuilt, returning the old member and cache
	dir := t.TempDir()
	path := writeConfig(t, dir, cachedStoreAPIConfig(10901, false, global))
	conf, clients, err := setup.BootstrapConfig("-config", path)
	require.NoError(t, err)
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	si := &instance.ServerInstance{Listeners: group}
	require.NoError(t, setup.ApplyConfig(si, conf, clients, nil, nil, group))
	t.Cleanup(func() { setup.Shutdown(si) })
	member, old := si.Backends[retireTestMember], si.Caches["default"]
	writeConfig(t, dir, cachedStoreAPIConfig(10902, true, global))
	modified := time.Now().Add(time.Second)
	require.NoError(t, os.Chtimes(path, modified, modified))
	ok, err := Reload(si, "test", "-config", path)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotSame(t, old, si.Caches["default"])
	return member, old, si
}

func TestReloadClosesAReplacedCacheOnceItsClientsRetire(t *testing.T) {
	member, old, si := reloadCache(t, true)
	require.Never(t, func() bool { return cacheClosed(old) }, retireTestDrain/3, time.Millisecond,
		"the old generation's requests can still use the cache it replaced")
	// what the old global ALB runs may still store into the cache, so it closes only once the old clients have retired
	var early atomic.Bool
	require.Eventually(t, func() bool {
		if !cacheClosed(old) {
			return false
		}
		early.Store(!retired(member))
		return true
	}, retireTestWait, time.Millisecond)
	require.False(t, early.Load(), "the replaced cache closed before the clients using it retired")
	require.False(t, cacheClosed(si.Caches["default"]))
}

func TestReloadClosesAReplacedCacheNoGlobalALBUsesAsItApplies(t *testing.T) {
	member, old, si := reloadCache(t, false)
	// nothing the old clients run stores into the cache after their requests drain, so it doesn't wait for them to retire
	var late atomic.Bool
	require.Eventually(t, func() bool {
		if !cacheClosed(old) {
			return false
		}
		late.Store(retired(member))
		return true
	}, retireTestWait, time.Millisecond)
	require.False(t, late.Load(), "the replaced cache waited for the old clients to retire")
	require.False(t, cacheClosed(si.Caches["default"]))
}
