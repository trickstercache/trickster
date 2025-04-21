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

package options

import (
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/evictionmethods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

const (
	// DefaultTimeseriesTTL is the default Cache TTL for Time Series Objects
	DefaultTimeseriesTTL = 6 * time.Hour
	// DefaultFastForwardTTL is the default Cache TTL for Time Series Fast Forward Objects
	DefaultFastForwardTTL = 15 * time.Second
	// DefaultMaxTTL is the default Maximum TTL of any cache object
	DefaultMaxTTL = 25 * time.Hour
	// DefaultRevalidationFactor is the default Cache Object Freshness Lifetime to TTL multiplier
	DefaultRevalidationFactor = 2
	// DefaultMaxObjectSizeBytes is the default Max Size of any Cache Object
	DefaultMaxObjectSizeBytes = 524288
	// DefaultBackendTRF is the default Timeseries Retention Factor for Time Series-based Backends
	DefaultBackendTRF = 1024
	// DefaultBackendTEM is the default Timeseries Eviction Method for Time Series-based Backends
	DefaultBackendTEM = evictionmethods.EvictionMethodOldest
	// DefaultBackendTEMName is the default Timeseries Eviction Method name for Time Series-based Backends
	DefaultBackendTEMName = "oldest"
	// DefaultBackendTimeout is the default Upstream Request Timeout for Backends
	DefaultBackendTimeout = 3 * time.Minute
	// DefaultBackendCacheName is the default Cache Name for Backends
	DefaultBackendCacheName = "default"
	// DefaultBackendNegativeCacheName is the default Negative Cache Name for Backends
	DefaultBackendNegativeCacheName = "default"
	// DefaultTracingConfigName is the default Tracing Config Name for Backends
	DefaultTracingConfigName = "default"
	// DefaultBackfillTolerance is the default Backfill Tolerance setting for Backends
	DefaultBackfillTolerance = 0 * time.Millisecond
	// DefaultBackfillTolerancePoints is the default Backfill Tolerance setting for Backends
	DefaultBackfillTolerancePoints = 0
	// DefaultKeepAliveTimeout is the default Keep Alive Timeout for Backends' upstream client pools
	DefaultKeepAliveTimeout = 5 * time.Minute
	// DefaultMaxIdleConns is the default number of Idle Connections in Backends' upstream client pools
	DefaultMaxIdleConns = 20
	// DefaultForwardedHeaders defines which class of 'Forwarded' headers are attached to upstream requests
	DefaultForwardedHeaders = "standard"
	// DefaullALBMechansimName defines the default ALB Mechanism Name
	DefaullALBMechansimName = "rr" // round robin
	// DefaultTimeseriesShardSize defines the default shard size of 0 (no sharding)
	DefaultTimeseriesShardSize = 0
	// DefaultTimeseriesShardStep defines the default shard step of 0 (no sharding)
	DefaultTimeseriesShardStep = 0
)

// DefaultCompressibleTypes returns a list of types that Trickster should compress before caching
func DefaultCompressibleTypes() []string {
	return []string{
		"text/html",
		"text/javascript",
		"text/css",
		headers.ValueTextPlain,
		"text/xml",
		"text/json",
		headers.ValueApplicationJSON,
		"application/javascript",
		"application/xml",
	}
}
