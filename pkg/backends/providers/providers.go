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

package providers

import "strconv"

// Provider enumerates the supported backend providers
type Provider int

const (
	// ProviderRPC represents thee Reverse Proxy Cache backend provider
	ProviderRPC = Provider(iota)
	// ProviderRule represents the Ruler backend provider
	ProviderRule
	// ProviderPrometheus represents the Prometheus backend provider
	ProviderPrometheus
	// ProviderInfluxDB represents the InfluxDB backend provider
	ProviderInfluxDB
	// ProviderIronDB represents the IRONdb backend provider
	ProviderIronDB
	// ProviderClickHouse represents the ClickHouse backend provider
	ProviderClickHouse
)

// Names is a map of Providers keyed by string name
var Names = map[string]Provider{
	"rule":              ProviderRule,
	"reverseproxycache": ProviderRPC,
	"rpc":               ProviderRPC,
	"prometheus":        ProviderPrometheus,
	"influxdb":          ProviderInfluxDB,
	"irondb":            ProviderIronDB,
	"clickhouse":        ProviderClickHouse,
}

// Values is a map of Providers valued by string name
var Values = make(map[Provider]string)

func init() {
	for k, v := range Names {
		Values[v] = k
	}
	// ensure consistent reverse mapping for reverseproxycache as rpc
	Values[ProviderRPC] = "rpc"
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
