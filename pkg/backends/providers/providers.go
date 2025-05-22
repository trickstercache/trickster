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

package providers

import (
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// Provider enumerates the supported backend providers
type Provider int

const (
	// RPC represents the Reverse Proxy Cache backend provider
	RPCID = Provider(iota)
	// ALB represents the Application Load Balancer backend provider
	ALBID
	// RP represents the Reverse Proxy (no caching) backend provider
	RPID
	// Rule represents the Ruler backend provider
	RuleID
	// Prometheus represents the Prometheus backend provider
	PrometheusID
	// InfluxDB represents the InfluxDB backend provider
	InfluxDBID
	// ClickHouse represents the ClickHouse backend provider
	ClickHouseID

	ReverseProxyShort      = "rp"
	ReverseProxy           = "reverseproxy"
	ReverseProxyCacheShort = "rpc"
	ReverseProxyCache      = "reverseproxycache"
	Proxy                  = "proxy"

	Rule = "rule"
	ALB  = "alb"

	Prometheus = "prometheus"
	ClickHouse = "clickhouse"
	InfluxDB   = "influxdb"
)

// Names is a map of Providers keyed by string name
var Names = map[string]Provider{
	Rule:                   RuleID,
	ReverseProxyCache:      RPCID,
	ReverseProxyCacheShort: RPCID,
	ALB:                    ALBID,
	Prometheus:             PrometheusID,
	InfluxDB:               InfluxDBID,
	ClickHouse:             ClickHouseID,
	Proxy:                  RPID,
	ReverseProxy:           RPID,
	ReverseProxyShort:      RPID,
}

// Values is a map of Providers valued by string name
var Values = make(map[Provider]string)

func init() {
	for k, v := range Names {
		Values[v] = k
	}
	// ensure consistent reverse mapping for reverseproxycache as rpc
	// and "rp" for proxy
	Values[RPCID] = ReverseProxyCacheShort
	Values[RPID] = ReverseProxyShort
}

var supportedTimeSeries = map[string]Provider{
	Prometheus: PrometheusID,
	InfluxDB:   InfluxDBID,
	ClickHouse: ClickHouseID,
}

// IsSupportedTimeSeriesProvider returns true if the provided time series is supported by Trickster
func IsSupportedTimeSeriesProvider(name string) bool {
	_, ok := supportedTimeSeries[name]
	return ok
}

var supportedTimeSeriesMerge = map[string]Provider{
	Prometheus: PrometheusID,
}

// IsSupportedTimeSeriesProvider returns true if the provided time series is supported by Trickster
func IsSupportedTimeSeriesMergeProvider(name string) bool {
	_, ok := supportedTimeSeriesMerge[name]
	return ok
}

func (t Provider) String() string {
	if v, ok := Values[t]; ok {
		return v
	}
	return strconv.Itoa(int(t))
}

// IsValidProvider returns true if the provided Provider is valid for use with Trickster
func IsValidProvider(t string) bool {
	_, ok := Names[t]
	return ok
}

// NonCacheBackends returns a set of backend Providers that do not use a cache
func NonCacheBackends() sets.Set[string] {
	return sets.New([]string{ReverseProxyShort,
		ReverseProxy, ALB, Proxy, Rule})
}

// NonOriginBackends returns a set of backend Providers that never proxy to an
// Origin URL, but instead pass requests off to other Providers that do.
func NonOriginBackends() sets.Set[string] {
	return sets.New([]string{ALB, Rule})
}
