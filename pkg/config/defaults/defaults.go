/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package defaults

import (
	"github.com/tricksterproxy/trickster/pkg/cache/evictionmethods"
	"github.com/tricksterproxy/trickster/pkg/cache/types"
)

const (
	DefaultLogFile  = ""
	DefaultLogLevel = "INFO"

	DefaultProxyListenPort    = 8480
	DefaultProxyListenAddress = ""

	DefaultMetricsListenPort    = 8481
	DefaultMetricsListenAddress = ""

	// 8482 is reserved for mockster, allowing the default TLS port to end with 3

	DefaultTLSProxyListenPort    = 8483
	DefaultTLSProxyListenAddress = ""

	DefaultTracerImplemetation    = "opentelemetry"
	DefaultExporterImplementation = "noop"

	DefaultCacheType   = "memory"
	DefaultCacheTypeID = types.CacheTypeMemory

	DefaultTimeseriesTTLSecs  = 21600
	DefaultFastForwardTTLSecs = 15
	DefaultMaxTTLSecs         = 86400
	DefaultRevalidationFactor = 2

	DefaultCachePath = "/tmp/trickster"

	DefaultRedisClientType = "standard"
	DefaultRedisProtocol   = "tcp"
	DefaultRedisEndpoint   = "redis:6379"

	DefaultBBoltFile   = "trickster.db"
	DefaultBBoltBucket = "trickster"

	DefaultCacheIndexReap        = 3
	DefaultCacheIndexFlush       = 5
	DefaultCacheMaxSizeBytes     = 536870912
	DefaultMaxSizeBackoffBytes   = 16777216
	DefaultMaxSizeObjects        = 0
	DefaultMaxSizeBackoffObjects = 100
	DefaultMaxObjectSizeBytes    = 524288

	DefaultOriginTRF               = 1024
	DefaultOriginTEM               = evictionmethods.EvictionMethodOldest
	DefaultOriginTEMName           = "oldest"
	DefaultOriginTimeoutSecs       = 180
	DefaultOriginCacheName         = "default"
	DefaultOriginNegativeCacheName = "default"
	DefaultTracingConfigName       = "default"
	DefaultBackfillToleranceSecs   = 0
	DefaultKeepAliveTimeoutSecs    = 300
	DefaultMaxIdleConns            = 20

	DefaultHealthCheckPath  = "-"
	DefaultHealthCheckQuery = "-"
	DefaultHealthCheckVerb  = "-"

	DefaultConfigHandlerPath = "/trickster/config"
	DefaultPingHandlerPath   = "/trickster/ping"

	DefaultMaxRuleExecutions = 16

	// DefaultConfigPath defines the default location of the Trickster config file
	DefaultConfigPath = "/etc/trickster/trickster.conf"
)

func DefaultCompressableTypes() []string {
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
