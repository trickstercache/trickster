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

package metrics

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

// ObserveCacheMiss records a Cache Miss event
func ObserveCacheMiss(cacheKey, cacheName, cacheProvider string) {
	ObserveCacheOperation(cacheName, cacheProvider, "get", "miss", 0)
}

// ObserveCacheDel records a cache deletion event
func ObserveCacheDel(cache, cacheProvider string, count float64) {
	ObserveCacheOperation(cache, cacheProvider, "del", "none", count)
}

// CacheError returns an empty cache object and the formatted error
func CacheError(cacheKey, cacheName, cacheProvider string, msg string) ([]byte, error) {
	ObserveCacheEvent(cacheName, cacheProvider, "error", msg)
	return nil, fmt.Errorf(msg, cacheKey)
}

// ObserveCacheOperation increments counters as cache operations occur
func ObserveCacheOperation(cache, cacheProvider, operation, status string, bytes float64) {
	metrics.CacheObjectOperations.WithLabelValues(cache, cacheProvider, operation, status).Inc()
	if bytes > 0 {
		metrics.CacheByteOperations.WithLabelValues(cache, cacheProvider, operation, status).Add(bytes)
	}
}

// ObserveCacheEvent increments counters as cache events occur
func ObserveCacheEvent(cache, cacheProvider, event, reason string) {
	metrics.CacheEvents.WithLabelValues(cache, cacheProvider, event, reason).Inc()
}

// ObserveCacheSizeChange adjust counters and gauges as the cache size changes due to object operations
func ObserveCacheSizeChange(cache, cacheProvider string, byteCount, objectCount int64) {
	metrics.CacheObjects.WithLabelValues(cache, cacheProvider).Set(float64(objectCount))
	metrics.CacheBytes.WithLabelValues(cache, cacheProvider).Set(float64(byteCount))
}
