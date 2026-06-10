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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

func TestKeyHandler(t *testing.T) {
	t.Parallel()

	const pathPrefix = "/trickster/purge/key/"
	cache := newMemCache()
	bes := backends.Backends{
		"backend-a": &fakeBackend{
			cfg:   &bo.Options{Name: "backend-a"},
			cache: cache,
		},
	}
	h := KeyHandler(pathPrefix, bes)

	t.Run("success", func(t *testing.T) {
		key := "object-key"
		if err := cache.Store(key, []byte("v"), 0); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, pathPrefix+"backend-a/"+key, nil)
		h(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		if _, _, err := cache.Retrieve(key); err == nil {
			t.Fatal("expected key to be purged")
		}
	})

	t.Run("not found path", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, pathPrefix+"backend-a", nil)
		h(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d", w.Code)
		}
	})

	t.Run("missing backend", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, pathPrefix+"missing/key", nil)
		h(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("missing cache", func(t *testing.T) {
		bes := backends.Backends{
			"no-cache": &fakeBackend{cfg: &bo.Options{Name: "no-cache"}, cache: nil},
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, pathPrefix+"no-cache/key", nil)
		KeyHandler(pathPrefix, bes)(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestPathHandlerValidation(t *testing.T) {
	t.Parallel()

	const pathPrefix = "/trickster/purge/path/"
	bes := backends.Backends{
		"a": &fakeBackend{
			cfg:   &bo.Options{Name: "a", CacheKeyPrefix: "pfx"},
			cache: newMemCache(),
		},
	}

	t.Run("usage error", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, pathPrefix+"/only-path", nil)
		PathHandler(pathPrefix, &bes)(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("not found", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, pathPrefix, nil)
		PathHandler(pathPrefix, &bes)(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d", w.Code)
		}
	})

	t.Run("missing backend", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, pathPrefix+"missing/path", nil)
		PathHandler(pathPrefix, &bes)(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
	})
}
