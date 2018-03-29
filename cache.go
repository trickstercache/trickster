package main

// Cache is the interface for the supported caching fabrics
// When making new cache types, Retrieve() must return an error on cache miss
type Cache interface {
	Connect() error
	Store(cacheKey string, data string, ttl int64) error
	Retrieve(cacheKey string) (string, error)
	Reap()
	Close() error
}

func getCache(t *TricksterHandler) Cache {

	switch t.Config.Caching.CacheType {
	case ctFilesystem:
		return &FilesystemCache{Config: t.Config.Caching.Filesystem, T: t}

	case ctRedis:
		return &RedisCache{Config: t.Config.Caching.Redis, T: t}
	}

	// Default to MemoryCache
	return &MemoryCache{T: t}
}
