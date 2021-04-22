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
)

// Provider enumerates the supported backend providers
type Provider int

const (
	// RPC represents the Reverse Proxy Cache backend provider
	RPC = Provider(iota)
	// ALB represents the Application Load Balancer backend provider
	ALB
	// RP represents the Reverse Proxy (no caching) backend provider
	RP
	// Rule represents the Ruler backend provider
	Rule
	// Prometheus represents the Prometheus backend provider
	Prometheus
	// InfluxDB represents the InfluxDB backend provider
	InfluxDB
	// IronDB represents the IRONdb backend provider
	IronDB
	// ClickHouse represents the ClickHouse backend provider
	ClickHouse
)

// Names is a map of Providers keyed by string name
var Names = map[string]Provider{
	"rule":              Rule,
	"reverseproxycache": RPC,
	"rpc":               RPC,
	"alb":               ALB,
	"prometheus":        Prometheus,
	"influxdb":          InfluxDB,
	"irondb":            IronDB,
	"clickhouse":        ClickHouse,
	"proxy":             RP,
	"reverseproxy":      RP,
	"rp":                RP,
}

// Values is a map of Providers valued by string name
var Values = make(map[Provider]string)

func init() {
	for k, v := range Names {
		Values[v] = k
	}
	// ensure consistent reverse mapping for reverseproxycache as rpc
	// and "rp" for proxy
	Values[RPC] = "rpc"
	Values[RP] = "rp"
}

var supportedTimeSeries = map[string]Provider{
	"prometheus": Prometheus,
	"influxdb":   InfluxDB,
	"clickhouse": ClickHouse,
	"irondb":     IronDB,
}

// IsSupportedTimeSeriesProvider returns true if the provided time series is supported by Trickster
func IsSupportedTimeSeriesProvider(name string) bool {
	_, ok := supportedTimeSeries[name]
	return ok
}

var supportedTimeSeriesMerge = map[string]Provider{
	"prometheus": Prometheus,
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
