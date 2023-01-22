package handlers

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// PurgeHandleFunc purges an object from a cache based on key.
func PurgeKeyHandleFunc(conf *config.Config, from backends.Backends) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		params := mux.Vars(req)
		purgeFrom, purgeKey := params["backend"], params["key"]
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
		rsc := request.GetResources(req)
		purgeFrom := req.URL.Query().Get("backend")
		purgePath := req.URL.Query().Get("path")
		if purgeFrom == "" || purgePath == "" {
			logging.Warn(rsc.Logger, "failed to get backend/path args", nil)
			w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
			w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Usage: " + config.DefaultPurgePathHandlerPath + "?backend={backend}&path={path}"))
			return
		}
		logging.Debug(rsc.Logger, "purging cache item", logging.Pairs{"backend": purgeFrom, "path": purgePath})
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
