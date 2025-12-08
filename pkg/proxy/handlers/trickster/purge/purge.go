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
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// writeValidationError writes a standardized validation error response
func writeValidationError(w http.ResponseWriter, errorMsg string) {
	w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
	w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
	w.WriteHeader(http.StatusBadRequest)
	w.Write([]byte(errorMsg))
}

// validateBackend checks if the backend exists and writes an error response if not
// Returns true if valid, false if invalid (and error response was written)
func validateBackend(w http.ResponseWriter, backend backends.Backend, backendName string) bool {
	if backend == nil {
		writeValidationError(w, "Backend "+backendName+" doesn't exist.")
		return false
	}
	return true
}

// validateCache checks if the backend has a cache and writes an error response if not
// Returns true if valid, false if invalid (and error response was written)
func validateCache(w http.ResponseWriter, cache cache.Cache, backendName string) bool {
	if cache == nil {
		writeValidationError(w, "Backend "+backendName+" doesn't have a cache.")
		return false
	}
	return true
}

// KeyHandler purges an object from a cache based on key.
func KeyHandler(pathPrefix string,
	from backends.Backends,
) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		vals := strings.Replace(req.URL.Path, pathPrefix, "", 1)
		parts := strings.Split(vals, "/")
		if len(parts) != 2 {
			http.NotFound(w, req)
			return
		}
		backendName := parts[0]
		purgeKey := parts[1]
		backend := from.Get(backendName)
		if !validateBackend(w, backend, backendName) {
			return
		}
		cache := backend.Cache()
		if !validateCache(w, cache, backendName) {
			return
		}
		cache.Remove(purgeKey)
		w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
		w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
		w.WriteHeader(http.StatusOK)
		w.Write(fmt.Appendf(nil, "purged: %s | %s\n", backendName, purgeKey))
	}
}

var (
	engines = []string{"opc", "dpc"}
	methods = []string{
		http.MethodGet, http.MethodHead, http.MethodPost,
		http.MethodPut, http.MethodPatch,
	}
)

func PathHandler(pathPrefix string,
	from *backends.Backends,
) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		vals := strings.Replace(req.URL.Path, pathPrefix, "", 1)
		parts := strings.SplitN(vals, "/", 2)
		if len(parts) != 2 {
			http.NotFound(w, req)
			return
		}
		backendName := parts[0]
		purgePath := parts[1]
		if !strings.HasPrefix(purgePath, "/") {
			purgePath = "/" + purgePath
		}
		if backendName == "" || purgePath == "" {
			logger.Warn("failed to get backend/path args", nil)
			w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
			w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Usage: " + pathPrefix + "{backend}/{path/to/purge}\n"))
			return
		}
		logger.Debug("purging cache item",
			logging.Pairs{"backend": backendName, "path": purgePath})
		backend := from.Get(backendName)
		if !validateBackend(w, backend, backendName) {
			return
		}
		cache := backend.Cache()
		if !validateCache(w, cache, backendName) {
			return
		}

		for _, engine := range engines {
			for _, method := range methods {
				cache.Remove(fmt.Sprintf("%s.%s.%s",
					backend.Configuration().CacheKeyPrefix, engine,
					md5.Checksum(fmt.Sprintf("%s.method.%s.", purgePath, method))))
			}
		}

		w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
		w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
		w.WriteHeader(http.StatusOK)
		w.Write(fmt.Appendf(nil, "purged: %s | %s\n", backendName, purgePath))
	}
}
