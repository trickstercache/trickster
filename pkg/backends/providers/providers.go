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
	"slices"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// Provider enumerates the supported backend providers
type Provider int

const (
	// RPC represents the Reverse Proxy Cache backend provider
	RPCID = Provider(iota)
	// RP represents the Reverse Proxy (no caching) backend provider
	RPID
	// Static represents the Static File Server backend provider
	StaticID
	// ALB represents the Application Load Balancer backend provider
	ALBID
	// Rule represents the Ruler backend provider
	RuleID
	//
	// Accelerated Time Series Providers
	// Prometheus represents the Prometheus backend provider
	PrometheusID
	// InfluxDB represents the InfluxDB backend provider
	InfluxDBID
	// ClickHouse represents the ClickHouse backend provider
	ClickHouseID
	// MySQL represents the MySQL backend provider
	MySQLID
	// Graphite represents the Graphite backend provider
	GraphiteID
	// Druid represents the Apache Druid backend provider
	DruidID
	// Postgres represents the PostgreSQL wire-protocol backend provider
	PostgresID
	// GreptimeDB represents the GreptimeDB backend provider.
	GreptimeDBID

	Backends = "backends"

	ReverseProxyShort      = "rp"
	ReverseProxy           = "reverseproxy"
	ReverseProxyCacheShort = "rpc"
	ReverseProxyCache      = "reverseproxycache"
	Proxy                  = "proxy"

	Rule   = "rule"
	ALB    = "alb"
	Static = "static"

	Prometheus = "prometheus"
	ClickHouse = "clickhouse"
	InfluxDB   = "influxdb"
	MySQL      = "mysql"
	Graphite   = "graphite"
	Druid      = "druid"
	Postgres   = "postgres"
	GreptimeDB = "greptimedb"

	// provider name aliases

	// TimescaleDB is an alias of Postgres; see Canonical.
	TimescaleDB = "timescaledb"
)

// aliases maps an accepted provider name to the provider that implements it.
var aliases = map[string]string{
	TimescaleDB: Postgres,
}

// Canonical returns the implementing provider's name for an alias, or name itself.
func Canonical(name string) string {
	if canonical, ok := aliases[name]; ok {
		return canonical
	}
	return name
}

// Aliases returns the sorted alias names that resolve to the canonical provider name.
func Aliases(canonical string) []string {
	var out []string
	for alias, target := range aliases {
		if target == canonical {
			out = append(out, alias)
		}
	}
	slices.Sort(out)
	return out
}

// Names is a map of Providers keyed by string name
var Names = map[string]Provider{
	Rule:                   RuleID,
	ReverseProxyCache:      RPCID,
	ReverseProxyCacheShort: RPCID,
	ALB:                    ALBID,
	Prometheus:             PrometheusID,
	InfluxDB:               InfluxDBID,
	ClickHouse:             ClickHouseID,
	Graphite:               GraphiteID,
	MySQL:                  MySQLID,
	Druid:                  DruidID,
	Postgres:               PostgresID,
	TimescaleDB:            PostgresID,
	GreptimeDB:             GreptimeDBID,
	Proxy:                  RPID,
	ReverseProxy:           RPID,
	ReverseProxyShort:      RPID,
	Static:                 StaticID,
}

// Values is a map of Providers valued by string name
var Values = make(map[Provider]string)

func init() {
	for k, v := range Names {
		Values[v] = k
	}
	// ensure consistent reverse mapping for reverseproxycache as rpc,
	// "rp" for proxy and the canonical name for aliased providers
	Values[RPCID] = ReverseProxyCacheShort
	Values[RPID] = ReverseProxyShort
	Values[PostgresID] = Postgres
}

var supportedTimeSeries = map[string]Provider{
	Prometheus:  PrometheusID,
	InfluxDB:    InfluxDBID,
	ClickHouse:  ClickHouseID,
	Graphite:    GraphiteID,
	MySQL:       MySQLID,
	Druid:       DruidID,
	Postgres:    PostgresID,
	TimescaleDB: PostgresID,
	GreptimeDB:  GreptimeDBID,
}

// IsSupportedTimeSeriesProvider returns true if the provided time series is supported by Trickster
func IsSupportedTimeSeriesProvider(name string) bool {
	_, ok := supportedTimeSeries[name]
	return ok
}

// supportedHTTPTimeSeries is the time series providers reached over HTTP, whose API paths the
// proxy predefines; MySQL and Postgres are served over their own wire protocols and have none
var supportedHTTPTimeSeries = map[string]Provider{
	Prometheus: PrometheusID,
	InfluxDB:   InfluxDBID,
	ClickHouse: ClickHouseID,
	Graphite:   GraphiteID,
	Druid:      DruidID,
	GreptimeDB: GreptimeDBID,
}

// IsSupportedHTTPTimeSeriesProvider returns true if the named provider is a time series
// provider reached over HTTP
func IsSupportedHTTPTimeSeriesProvider(name string) bool {
	_, ok := supportedHTTPTimeSeries[name]
	return ok
}

// HTTPTimeSeriesProviderNames returns the sorted names of the time series providers reached
// over HTTP
func HTTPTimeSeriesProviderNames() []string {
	out := make([]string, 0, len(supportedHTTPTimeSeries))
	for name := range supportedHTTPTimeSeries {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// IsPrometheusCompatible reports whether the provider exposes Prometheus APIs.
func IsPrometheusCompatible(name string) bool {
	return name == Prometheus || name == GreptimeDB
}

// IsSupportedTimeSeriesMergeProvider returns true if the provided time series is
// supported by the Time Series Merge ALB mechanism
func IsSupportedTimeSeriesMergeProvider(name string) bool {
	return IsPrometheusCompatible(name)
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
	return sets.New([]string{
		ReverseProxyShort,
		ReverseProxy, ALB, Proxy, Rule, Static,
	})
}

// NonOriginBackends returns a set of backend Providers that never proxy to an
// Origin URL; they pass requests to other Providers or answer them locally.
func NonOriginBackends() sets.Set[string] {
	return sets.New([]string{ALB, Rule, Static})
}
