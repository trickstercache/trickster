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

package flight

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ttlCache records the TTL and keys passed to Set.
type ttlCache struct {
	mu   sync.Mutex
	data map[string][]byte
	ttls map[string]time.Duration
}

func newTTLCache() *ttlCache {
	return &ttlCache{data: make(map[string][]byte), ttls: make(map[string]time.Duration)}
}

func (c *ttlCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.data[key]
	return b, ok
}

func (c *ttlCache) Set(key string, data []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = data
	c.ttls[key] = ttl
}

func execute(t *testing.T, addr, query string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := flightsql.NewClientCtx(ctx, addr, nil, nil,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer client.Close()
	info, err := client.Execute(ctx, query)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	reader, err := client.DoGet(ctx, info.Endpoint[0].Ticket)
	if err != nil {
		t.Fatalf("doGet: %v", err)
	}
	defer reader.Release()
	for reader.Next() {
	}
	if err := reader.Err(); err != nil {
		t.Fatalf("read: %v", err)
	}
}

// TestVolatileQueriesBypassCache ensures nondeterministic statements are
// executed upstream on every call and never cached.
func TestVolatileQueriesBypassCache(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	cache := newTTLCache()
	srv := NewServer(up, cache)
	_, addr := startTestServer(t, srv)

	for range 2 {
		execute(t, addr, "SELECT now(), * FROM cpu")
	}
	if up.callCount != 2 {
		t.Fatalf("volatile query hit the cache: %d upstream calls", up.callCount)
	}
	if len(cache.data) != 0 {
		t.Fatalf("volatile query result was cached: %v", cache.data)
	}

	// a deterministic query is cached after the first execution
	for range 2 {
		execute(t, addr, "SELECT * FROM cpu")
	}
	if up.callCount != 3 {
		t.Fatalf("deterministic query not cached: %d upstream calls", up.callCount)
	}
}

// TestCacheTTLAndKeyPrefix ensures the configured TTL is applied and cache
// keys are namespaced by the configured prefix.
func TestCacheTTLAndKeyPrefix(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	cache := newTTLCache()
	srv := NewServer(up, cache,
		WithCacheTTL(5*time.Minute), WithCacheKeyPrefix("influx3"))
	_, addr := startTestServer(t, srv)

	execute(t, addr, "SELECT * FROM cpu")
	if len(cache.data) != 1 {
		t.Fatalf("expected one cache entry, got %d", len(cache.data))
	}
	for key, ttl := range cache.ttls {
		if !strings.HasPrefix(key, "influx3|") {
			t.Fatalf("cache key not namespaced by backend: %q", key)
		}
		if ttl != 5*time.Minute {
			t.Fatalf("cache ttl = %s, want 5m", ttl)
		}
	}
}

// TestDefaultCacheTTL ensures the 60s default applies when unconfigured.
func TestDefaultCacheTTL(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	cache := newTTLCache()
	srv := NewServer(up, cache)
	_, addr := startTestServer(t, srv)
	execute(t, addr, "SELECT * FROM cpu")
	for _, ttl := range cache.ttls {
		if ttl != DefaultCacheTTL {
			t.Fatalf("default ttl = %s, want %s", ttl, DefaultCacheTTL)
		}
	}
}
