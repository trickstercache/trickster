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
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	proxyengines "github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
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

func TestWritePurgeResultEscapesReflectedValues(t *testing.T) {
	w := httptest.NewRecorder()
	writePurgeResult(w, "<backend>", "/<script>&")

	if got, want := w.Body.String(), "purged: &lt;backend&gt; | /&lt;script&gt;&amp;\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if got := w.Header().Get(headers.NameContentType); got != headers.ValueTextPlain {
		t.Fatalf("Content-Type = %q, want %q", got, headers.ValueTextPlain)
	}
	if got := w.Header().Get(headers.NameCacheControl); got != headers.ValueNoCache {
		t.Fatalf("Cache-Control = %q, want %q", got, headers.ValueNoCache)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestValidationErrorsEscapeBackendName(t *testing.T) {
	tests := []struct {
		name     string
		validate func(http.ResponseWriter) bool
		wantBody string
	}{
		{
			name: "missing backend",
			validate: func(w http.ResponseWriter) bool {
				return validateBackend(w, nil, "<backend>")
			},
			wantBody: "Backend &lt;backend&gt; doesn't exist.",
		},
		{
			name: "missing cache",
			validate: func(w http.ResponseWriter) bool {
				return validateCache(w, nil, "<backend>")
			},
			wantBody: "Backend &lt;backend&gt; doesn't have a cache.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if tc.validate(w) {
				t.Fatal("validation unexpectedly succeeded")
			}
			if got := w.Body.String(); got != tc.wantBody {
				t.Fatalf("body = %q, want %q", got, tc.wantBody)
			}
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestPathHandler_KeyFormatMatchesEngines(t *testing.T) {
	const (
		sharedPrefix = "shared"
		purgePath    = "/api/v1/query"
	)

	// backend "a" carries a configured identity on the purged path, so its
	// entries are stored under the identity-keyed variant as well
	pathA := &po.Options{
		Path: purgePath, Methods: methods,
		RequestHeaders: map[string]string{headers.NameAuthorization: "Basic pinned"},
	}
	if err := pathA.Initialize(""); err != nil {
		t.Fatal(err)
	}

	cacheA := newMemCache()
	cacheB := newMemCache()
	bes := backends.Backends{
		"a": &fakeBackend{
			cfg:   &bo.Options{Name: "a", CacheKeyPrefix: sharedPrefix, Paths: po.List{pathA}},
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
				var identity string
				if pc := c.Paths.Match(method, purgePath); pc != nil {
					identity = pc.IdentityKeyPart()
				}
				k := fmt.Sprintf("%s.%s.%s.%s",
					c.Name, c.CacheKeyPrefix, engine,
					proxyengines.DerivePathCacheKey(purgePath, method, identity))
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

func TestPathHandler_PurgesEveryConditionalIdentity(t *testing.T) {
	// conditioned paths for one pathname key their entries on different
	// configured identities, and a purge removes every variant
	const (
		purgePath = "/api/v1/query"
		otherPath = "/api/v1/other"
	)
	newPath := func(path string, mt matching.PathMatchName, auth string,
		conds []*po.Condition,
	) *po.Options {
		p := &po.Options{
			Path: path, MatchTypeName: mt, Methods: slices.Clone(methods),
			RequestHeaders: map[string]string{headers.NameAuthorization: auth},
			MatchHeaders:   conds,
		}
		if err := p.Initialize(""); err != nil {
			t.Fatal(err)
		}
		return p
	}
	list := po.List{
		newPath(purgePath, matching.PathMatchNameExact, "Basic gold",
			[]*po.Condition{{Name: "X-Tenant", Value: "gold"}}),
		newPath(purgePath, matching.PathMatchNameExact, "Basic silver",
			[]*po.Condition{{Name: "X-Tenant", Value: "silver"}}),
		// a request matching neither condition falls through to this one
		newPath("/api/", matching.PathMatchNamePrefix, "Basic fallback", nil),
	}
	identities := []string{""}
	for _, p := range list {
		ik := p.IdentityKeyPart()
		if ik == "" || slices.Contains(identities, ik) {
			t.Fatalf("path %q must have a distinct configured identity", p.Path)
		}
		identities = append(identities, ik)
	}

	c := newMemCache()
	bes := backends.Backends{"a": &fakeBackend{
		cfg:   &bo.Options{Name: "a", CacheKeyPrefix: "shared", Paths: list},
		cache: c,
	}}

	// every variant a request to the purged path could have created, plus
	// the same variants of a neighboring path, which must survive
	var purged, kept []string
	for _, engine := range engines {
		for _, method := range methods {
			for _, identity := range identities {
				purged = append(purged, proxyengines.ComposeCacheKey("a", "shared", engine,
					proxyengines.DerivePathCacheKey(purgePath, method, identity)))
				kept = append(kept, proxyengines.ComposeCacheKey("a", "shared", engine,
					proxyengines.DerivePathCacheKey(otherPath, method, identity)))
			}
		}
	}
	for _, k := range append(slices.Clone(purged), kept...) {
		if err := c.Store(k, []byte("v"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	const pathPrefix = "/trickster/purge/path/"
	w := httptest.NewRecorder()
	PathHandler(pathPrefix, &bes)(w, httptest.NewRequest(http.MethodGet,
		pathPrefix+"a"+purgePath, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	for _, k := range purged {
		if _, _, err := c.Retrieve(k); err == nil {
			t.Errorf("key %q should have been purged", k)
		}
	}
	for _, k := range kept {
		if _, _, err := c.Retrieve(k); err != nil {
			t.Errorf("key %q is another path and should remain, got err=%v", k, err)
		}
	}
}
