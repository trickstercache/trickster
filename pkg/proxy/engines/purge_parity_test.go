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

package engines_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	ct "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/purge"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

type parityCache struct {
	mu   sync.Mutex
	data map[string][]byte
	cfg  *co.Options
}

func (m *parityCache) Connect() error { return nil }
func (m *parityCache) Store(k string, b []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[k] = b
	return nil
}

func (m *parityCache) Retrieve(k string) ([]byte, status.LookupStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.data[k]; ok {
		return b, status.LookupStatusHit, nil
	}
	return nil, status.LookupStatusKeyMiss, cache.ErrKNF
}

func (m *parityCache) Remove(keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.data, k)
	}
	return nil
}

func (m *parityCache) Close() error                   { return nil }
func (m *parityCache) Configuration() *co.Options     { return m.cfg }

type parityBackend struct {
	backends.Backend
	cfg   *bo.Options
	cache cache.Cache
}

func (f *parityBackend) Configuration() *bo.Options { return f.cfg }
func (f *parityBackend) Cache() cache.Cache         { return f.cache }

// TestPathPurgeRemovesEngineDerivedKeys stores entries under keys derived by
// the real engine key derivation for a path with a configured static identity,
// then asserts purge-by-path removes those exact entries.
func TestPathPurgeRemovesEngineDerivedKeys(t *testing.T) {
	const purgePath = "/api/v1/query"
	pc := &po.Options{
		Path:           purgePath,
		Methods:        []string{http.MethodGet, http.MethodPost},
		RequestHeaders: map[string]string{"Authorization": "Basic pinned"},
		RequestParams:  map[string]string{"local": "1"},
	}
	if err := pc.Initialize(""); err != nil {
		t.Fatal(err)
	}
	cfg := &bo.Options{Name: "gr", CacheKeyPrefix: "pfx", Paths: po.List{pc}}

	// the key a cached parameterless GET is actually stored under
	r := httptest.NewRequest(http.MethodGet, "http://origin"+purgePath, nil)
	r = r.WithContext(ct.WithResources(r.Context(),
		request.NewResources(cfg, pc, nil, nil, nil, nil)))
	suffix := engines.DeriveCacheKeyForRequest(r, "")

	mc := &parityCache{data: map[string][]byte{}, cfg: &co.Options{Provider: "memory"}}
	fullKeys := make([]string, 0, 2)
	for _, engine := range []string{"opc", "dpc"} {
		k := engines.ComposeCacheKey(cfg.Name, cfg.CacheKeyPrefix, engine, suffix)
		if err := mc.Store(k, []byte("v"), time.Minute); err != nil {
			t.Fatal(err)
		}
		fullKeys = append(fullKeys, k)
	}

	bes := backends.Backends{"gr": &parityBackend{cfg: cfg, cache: mc}}
	const prefix = "/trickster/purge/path/"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, prefix+"gr"+purgePath, nil)
	purge.PathHandler(prefix, &bes)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	for _, k := range fullKeys {
		if _, _, err := mc.Retrieve(k); err == nil {
			t.Errorf("identity-keyed entry %q survived the path purge", k)
		}
	}
}
