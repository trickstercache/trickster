package main

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

const trickster = "trickster"

// Origin Database Types
const (
	otPrometheus = "prometheus"
	otInfluxDb   = "influxdb"
)

// Prometheus API method names
const (
	mnQueryRange = "query_range"
	mnQuery      = "query"
	mnLabels     = "label/__name__/values"
	mnHealth     = "health"
)

// Cache Lookup Results
const (
	crKeyMiss    = "kmiss"
	crRangeMiss  = "rmiss"
	crHit        = "hit"
	crPartialHit = "phit"
	crPurge      = "purge"
)

// Common URL Parameter Names
const (
	upQuery      = "query"
	upStart      = "start"
	upEnd        = "end"
	upStep       = "step"
	upOriginFqdn = "origin_fqdn"
	upOriginPort = "origin_port"
	upTimeout    = "timeout"
	upOrigin     = "origin"
	upTime       = "time"
)

// Common HTTP Header Names
const (
	hnCacheControl  = "Cache-Control"
	hnAllowOrigin   = "Access-Control-Allow-Origin"
	hnContentType   = "Content-Type"
	hnAuthorization = "Authorization"
)

// Common HTTP Header Values
const (
	hvNoCache         = "no-cache"
	hvApplicationJSON = "application/json"
)

// Prometheus Response Fields
const (
	rfStatus     = "status"
	rfData       = "data"
	rfResultType = "resultType"
	rfResult     = "result"
	rfMetric     = "metric"
	rfValues     = "values"
	rfValue      = "value"
)

// Prometheus Response Values
const (
	rvSuccess = "success"
	rvMatrix  = "matrix"
	rvVector  = "vector"
)

// Cache Interface Types
const (
	ctMemory     = "memory"
	ctFilesystem = "filesystem"
	ctRedis      = "redis"
)

// Log Fields
const (
	lfEvent      = "event"
	lfDetail     = "detail"
	lfParamName  = "paramName"
	lfParamValue = "paramValue"
	lfCacheKey   = "cacheKey"
)

// http methods
const (
	hmGet = "GET"
)

// Environment Variables
const (
	evOrigin      = "TRK_ORIGIN"
	evProxyPort   = "TRK_PROXY_PORT"
	evMetricsPort = "TRK_METRICS_PORT"
	evLogLevel    = "TRK_LOG_LEVEL"
)

// Command Line Flags
const (
	cfConfig      = "config"
	cfVersion     = "version"
	cfLogLevel    = "log-level"
	cfInstanceId  = "instance-id"
	cfOrigin      = "origin"
	cfProxyPort   = "proxy-port"
	cfMetricsPort = "metrics-port"
)
