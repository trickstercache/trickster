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

package handlers

import (
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// PurgeHandleFunc purges an object from a cache based on key.
func PurgeKeyHandleFunc(conf *config.Config, from backends.Backends) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		vals := strings.Replace(req.URL.Path, conf.Main.PurgeKeyHandlerPath, "", 1)
		parts := strings.Split(vals, "/")
		if len(parts) != 2 {
			http.NotFound(w, req)
			return
		}
		purgeFrom := parts[0]
		purgeKey := parts[1]
		fromBackend := from.Get(purgeFrom)
		if fromBackend == nil {
			w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
			w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Backend " + purgeFrom + " doesn't exist."))
			return
		}
		fromCache := fromBackend.Cache()
		if fromCache == nil {
			w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
			w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Backend " + purgeFrom + " doesn't have a cache."))
			return
		}
		fromCache.Remove(purgeKey)
		w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
		w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Purged " + purgeFrom + ":" + purgeKey))
	}
}

func PurgePathHandlerFunc(conf *config.Config, from *backends.Backends) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		purgeFrom := req.URL.Query().Get("backend")
		purgePath := req.URL.Query().Get("path")
		if purgeFrom == "" || purgePath == "" {
			logger.Warn("failed to get backend/path args", nil)
			w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
			w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Usage: " + config.DefaultPurgePathHandlerPath + "?backend={backend}&path={path}"))
			return
		}
		logger.Debug("purging cache item",
			logging.Pairs{"backend": purgeFrom, "path": purgePath})
		fromBackend := from.Get(purgeFrom)
		if fromBackend == nil {
			w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
			w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Backend " + purgeFrom + " doesn't exist."))
			return
		}
		purgeKey := fromBackend.Configuration().CacheKeyPrefix + ".dpc." + md5.Checksum(purgePath)
		fromCache := fromBackend.Cache()
		if fromCache == nil {
			w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
			w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Backend " + purgeFrom + " doesn't have a cache."))
			return
		}
		fromCache.Remove(purgeKey)
		w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
		w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Purged " + purgeFrom + ":" + purgePath + " (" + purgeKey + ")"))
	}
}
