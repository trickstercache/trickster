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
	"github.com/trickstercache/trickster/v2/pkg/cache/evictionmethods"
)

const (
	// DefaultTimeseriesTTLMS is the default Cache TTL for Time Series Objects
	DefaultTimeseriesTTLMS = 21600000
	// DefaultFastForwardTTLMS is the default Cache TTL for Time Series Fast Forward Objects
	DefaultFastForwardTTLMS = 15000
	// DefaultMaxTTLMS is the default Maximum TTL of any cache object
	DefaultMaxTTLMS = 86400000
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
	// DefaultBackendTimeoutMS is the default Upstream Request Timeout for Backends
	DefaultBackendTimeoutMS = 180000
	// DefaultBackendCacheName is the default Cache Name for Backends
	DefaultBackendCacheName = "default"
	// DefaultBackendNegativeCacheName is the default Negative Cache Name for Backends
	DefaultBackendNegativeCacheName = "default"
	// DefaultTracingConfigName is the default Tracing Config Name for Backends
	DefaultTracingConfigName = "default"
	// DefaultBackfillToleranceMS is the default Backfill Tolerance setting for Backends
	DefaultBackfillToleranceMS = 0
	// DefaultBackfillTolerancePoints is the default Backfill Tolerance setting for Backends
	DefaultBackfillTolerancePoints = 0
	// DefaultKeepAliveTimeoutMS is the default Keep Alive Timeout for Backends' upstream client pools
	DefaultKeepAliveTimeoutMS = 300000
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
		"text/plain",
		"text/xml",
		"text/json",
		"application/json",
		"application/javascript",
		"application/xml",
	}
}
