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

package engines

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

// staleRefreshes holds the keys with a background revalidation already in
// flight, so a burst of requests arriving during the stale-while-revalidate
// window costs one origin fetch rather than one per request.
var staleRefreshes sync.Map

// handleStaleWhileRevalidate answers from storage right away and refreshes the
// object behind the response. RFC 5861 3 trades a bounded amount of staleness
// for never making a client wait on revalidation.
func handleStaleWhileRevalidate(pr *proxyRequest) error {
	if _, busy := staleRefreshes.LoadOrStore(pr.key, struct{}{}); !busy {
		refresh := pr.Clone()
		key := pr.key
		goWithRecover("opc.stale.revalidate", func() {
			defer staleRefreshes.Delete(key)
			refreshStale(refresh)
		})
	}
	pr.cacheStatus = status.LookupStatusHit
	pr.writeToCache = false
	return handleTrueCacheHit(pr)
}

// refreshStale fetches the object again and stores it, with no client to write
// to. It is detached from the request that triggered it, which has already
// been answered and whose context is about to be cancelled.
func refreshStale(pr *proxyRequest) {
	if pr == nil || pr.upstreamRequest == nil {
		return
	}
	pr.responseWriter = nil
	pr.clientWriter = nil
	pr.upstreamRequest = pr.upstreamRequest.WithContext(
		context.WithoutCancel(pr.upstreamRequest.Context()))
	pr.cacheDocument = nil
	pr.cacheStatus = status.LookupStatusKeyMiss

	pr.prepareUpstreamRequests()
	if err := handleUpstreamTransactions(pr); err != nil {
		return
	}
	if !pr.writeToCache || pr.upstreamResponse == nil || pr.upstreamReader == nil {
		return
	}
	b, err := io.ReadAll(pr.upstreamReader)
	if err != nil {
		return
	}
	pr.cacheDocument = DocumentFromHTTPResponse(pr.upstreamResponse, b, pr.cachingPolicy)
	if err := pr.store(); err != nil {
		// the stale copy is still in place and still servable, so a failed
		// refresh costs freshness rather than correctness
		logger.Error("stale revalidation could not store the refreshed object",
			logging.Pairs{keys.Key: pr.key, keys.Detail: err.Error()})
	}
}

// serveStaleOnError reports whether a failing origin should be answered from
// what the cache already holds. RFC 5861 4 covers a server that could not
// answer, so a response that did arrive -- including a 404 saying the resource
// is gone -- is passed through rather than papered over.
func serveStaleOnError(pr *proxyRequest) bool {
	if pr.cacheDocument == nil || pr.upstreamResponse == nil {
		return false
	}
	if pr.upstreamResponse.StatusCode < http.StatusInternalServerError {
		return false
	}
	cp := pr.cacheDocument.CachingPolicy
	return cp.CanServeStaleOnError(time.Now())
}
