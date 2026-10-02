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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// truncatedMarkerSuffix names the cache entry recording that a key's query is truncated upstream
const truncatedMarkerSuffix = "|truncated"

var truncatedMarker = []byte{1}

// truncated reports whether any of tsl is a result the origin cut short at seriesCap, which it
// flags with a warning; one result at the cap without a warning is complete.
func truncated(seriesCap int, tsl ...timeseries.Timeseries) bool {
	if seriesCap <= 0 {
		return false
	}
	for _, ts := range tsl {
		if ds, ok := ts.(*dataset.DataSet); ok && ds != nil &&
			len(ds.Warnings) > 0 && ds.SeriesCount() >= seriesCap {
			return true
		}
	}
	return false
}

// markTruncated evicts key and records that its query truncates upstream, so that requests for
// it are proxied for ttl rather than fetched, found truncated, and proxied again.
func markTruncated(c cache.Cache, key string, ttl time.Duration) {
	goWithRecover("dpc.cache.markTruncated", func() {
		c.Remove(key)
		c.Store(key+truncatedMarkerSuffix, truncatedMarker, ttl)
	})
}

// isMarkedTruncated reports whether key's query was recently found truncated upstream.
func isMarkedTruncated(c cache.Cache, key string) bool {
	_, _, err := c.Retrieve(key + truncatedMarkerSuffix)
	return err == nil
}
