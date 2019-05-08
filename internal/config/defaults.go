/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package config

const (
	defaultLogFile  = ""
	defaultLogLevel = "INFO"
	defaultHostname = "localhost.unknown"

	defaultProxyListenPort    = 9090
	defaultProxyListenAddress = ""

	defaultMetricsListenPort    = 8082
	defaultMetricsListenAddress = ""

	defaultCacheType        = "memory"
	defaultCacheCompression = true

	defaultTimeseriesTTLSecs  = 21600
	defaultFastForwardTTLSecs = 15
	defaultObjectTTLSecs      = 30

	defaultCachePath = "/tmp/trickster"

	defaultRedisClientType = "standard"
	defaultRedisProtocol   = "tcp"
	defaultRedisEndpoint   = "redis:6379"

	defaultBBoltFile   = "trickster.db"
	defaultBBoltBucket = "trickster"

	defaultCacheIndexReap        = 3
	defaultCacheIndexFlush       = 5
	defaultCacheMaxSizeBytes     = 536870912
	defaultMaxSizeBackoffBytes   = 16777216
	defaultMaxSizeObjects        = 0
	defaultMaxSizeBackoffObjects = 100

	defaultOriginServerType      = "prometheus"
	defaultOriginScheme          = "http"
	defaultOriginHost            = "prometheus:9090"
	defaultOriginPathPrefix      = ""
	defaultOriginAPIPath         = "/api/v1/"
	defaultOriginINCH            = true
	defaultOriginVRF             = 1024
	defaultOriginTimeoutSecs     = 180
	defaultOriginCacheName       = "default"
	defaultBackfillToleranceSecs = 0
)
