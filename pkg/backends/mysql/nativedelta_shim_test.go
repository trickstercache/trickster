/*
 * Copyright 2026 The Trickster Authors
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

package mysql

// This file adapts the pre-nativedelta package internals onto the shared
// engine so the existing behavioral tests keep exercising the same surfaces.

import (
	"errors"
	"time"
	"unsafe"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	cacheproviders "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"vitess.io/vitess/go/sqltypes"
)

func memoryCacheClient(cacheClient cache.Cache) (cache.MemoryCache, bool) {
	if cacheClient == nil || cacheClient.Configuration() == nil ||
		cacheClient.Configuration().Provider != cacheproviders.Memory {
		return nil, false
	}
	memoryCache, ok := cacheClient.(cache.MemoryCache)
	return memoryCache, ok
}

type cachedQueryResult struct {
	result  *sqltypes.Result
	extents timeseries.ExtentList
	size    int
}

func (c *cachedQueryResult) Size() int {
	if c == nil {
		return 0
	}
	return c.size
}

func estimateCachedQueryResultSize(cached *cachedQueryResult) int {
	if cached == nil {
		return 0
	}
	return int(unsafe.Sizeof(*cached)) +
		cap(cached.extents)*int(unsafe.Sizeof(timeseries.Extent{})) +
		estimateResultSize(cached.result)
}

type deltaRequestWindow struct {
	output    timeseries.Extent
	cacheable timeseries.ExtentList
	lower     time.Time
	upper     time.Time
	empty     bool
}

func buildDeltaRequestWindow(plan *sqlanalyzer.QueryPlan) (deltaRequestWindow, error) {
	window, err := nativedelta.BuildWindow(plan, time.Now(), true)
	if err != nil {
		return deltaRequestWindow{}, err
	}
	return deltaRequestWindow{
		output: window.Output, cacheable: window.Cacheable,
		lower: window.Lower, upper: window.Upper, empty: window.Empty,
	}, nil
}

// testDelta builds a fresh engine from the handler's current configuration so
// tests that mutate config between calls observe the change, matching the old
// per-call handler-method behavior.
func (h *protocolHandler) testDelta() *nativedelta.Engine[*sqltypes.Result] {
	return nativedelta.New(nativedelta.Config{
		Protocol:              mysqlDialect,
		BackendName:           h.config.BackendName,
		CacheClient:           h.cacheClient,
		CacheTTL:              h.config.CacheTTL,
		MaxObjectSize:         h.config.MaxObjectSize,
		ObserveCacheFailure:   h.observeCacheFailure,
		ObserveRewriteFailure: h.observeRewriteFailure,
	}, resultCodec{})
}

func (h *protocolHandler) storeCached(key string, cached *cachedQueryResult) {
	if cached == nil || cached.result == nil {
		h.testDelta().Store(key, nil)
		return
	}
	entry := &nativedelta.Entry[*sqltypes.Result]{
		Payload: cached.result, Extents: cached.extents,
	}
	h.testDelta().Store(key, entry)
	cached.size = entry.Size()
}

func (h *protocolHandler) retrieveCached(key string) (*cachedQueryResult, bool) {
	entry, ok := h.testDelta().Retrieve(key)
	if !ok || entry.Marker {
		return nil, false
	}
	return &cachedQueryResult{
		result: entry.Payload, extents: entry.Extents, size: entry.Size(),
	}, true
}

func (h *protocolHandler) removeCached(key, _ string) {
	h.testDelta().Remove(key)
}

// The shared envelope's magic and version, mirrored for envelope-corruption
// tests.
var cacheEnvelopeMagic = [4]byte{'T', 'N', 'D', 'C'}

const cacheEnvelopeVersion byte = 1

var envelopeShimEngine = nativedelta.New(nativedelta.Config{}, resultCodec{})

func marshalCachedQueryResult(cached *cachedQueryResult) ([]byte, error) {
	if cached == nil || cached.result == nil {
		return nil, errors.New("nil MySQL cache result")
	}
	return envelopeShimEngine.MarshalEntry(&nativedelta.Entry[*sqltypes.Result]{
		Payload: cached.result, Extents: cached.extents,
	})
}

func unmarshalCachedQueryResult(data []byte) (*cachedQueryResult, error) {
	entry, err := envelopeShimEngine.UnmarshalEntry(data)
	if err != nil {
		return nil, err
	}
	return &cachedQueryResult{result: entry.Payload, extents: entry.Extents}, nil
}
