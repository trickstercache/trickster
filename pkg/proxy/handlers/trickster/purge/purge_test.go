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

package purge

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/checksum/md5"
)

// memCache is a minimal in-memory cache.Cache used to assert key-format parity
// between the engines and the purge handler.
type memCache struct {
	mu   sync.Mutex
	data map[string][]byte
	cfg  *options.Options
}

func newMemCache() *memCache {
	return &memCache{data: map[string][]byte{}, cfg: &options.Options{Provider: "memory"}}
}

func (m *memCache) Connect() error { return nil }
func (m *memCache) Store(k string, b []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[k] = b
	return nil
}

func (m *memCache) Retrieve(k string) ([]byte, status.LookupStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.data[k]; ok {
		return b, status.LookupStatusHit, nil
	}
	return nil, status.LookupStatusKeyMiss, cache.ErrKNF
}

func (m *memCache) Remove(keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.data, k)
	}
	return nil
}

func (m *memCache) Close() error                    { return nil }
func (m *memCache) Configuration() *options.Options { return m.cfg }

// fakeBackend exposes just the Backend surface PathHandler touches.
type fakeBackend struct {
	backends.Backend
	cfg   *bo.Options
	cache cache.Cache
}

func (f *fakeBackend) Configuration() *bo.Options { return f.cfg }
func (f *fakeBackend) Cache() cache.Cache         { return f.cache }

// TestPathHandler_KeyFormatMatchesEngines stores entries under the engine key
// format for two backends sharing CacheKeyPrefix, then issues purges and
// asserts only the targeted backend's entries are removed.
func TestPathHandler_KeyFormatMatchesEngines(t *testing.T) {
	const (
		sharedPrefix = "shared"
		purgePath    = "/api/v1/query"
	)

	cacheA := newMemCache()
	cacheB := newMemCache()
	bes := backends.Backends{
		"a": &fakeBackend{
			cfg:   &bo.Options{Name: "a", CacheKeyPrefix: sharedPrefix},
			cache: cacheA,
		},
		"b": &fakeBackend{
			cfg:   &bo.Options{Name: "b", CacheKeyPrefix: sharedPrefix},
			cache: cacheB,
		},
	}

	// Pre-populate every (backend, engine, method) key the purge handler
	// reconstructs. Format must stay in sync with the engines.
	keys := map[string]string{}
	for name, be := range bes {
		c := be.Configuration()
		for _, engine := range engines {
			for _, method := range methods {
				k := fmt.Sprintf("%s.%s.%s.%s",
					c.Name, c.CacheKeyPrefix, engine,
					md5.Checksum(fmt.Sprintf("%s.method.%s.", purgePath, method)))
				if err := be.Cache().Store(k, []byte("v"), time.Minute); err != nil {
					t.Fatal(err)
				}
				keys[name+"."+engine+"."+method] = k
			}
		}
	}

	const pathPrefix = "/trickster/purge/path/"
	h := PathHandler(pathPrefix, &bes)

	// Purge backend "a"; "b" entries must remain.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, pathPrefix+"a"+purgePath, nil)
	h(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	for _, engine := range engines {
		for _, method := range methods {
			ka := keys["a."+engine+"."+method]
			if _, _, err := cacheA.Retrieve(ka); err == nil {
				t.Errorf("backend a key %q should have been purged", ka)
			}
			kb := keys["b."+engine+"."+method]
			if _, _, err := cacheB.Retrieve(kb); err != nil {
				t.Errorf("backend b key %q should still be present, got err=%v", kb, err)
			}
		}
	}

	// Purge backend "b"; everything should be gone.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, pathPrefix+"b"+purgePath, nil)
	h(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	for _, engine := range engines {
		for _, method := range methods {
			kb := keys["b."+engine+"."+method]
			if _, _, err := cacheB.Retrieve(kb); err == nil {
				t.Errorf("backend b key %q should have been purged", kb)
			}
		}
	}
}
