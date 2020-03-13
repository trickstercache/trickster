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

package types

import "strconv"

// OriginType enumerates the supported origin types
type OriginType int

const (
	// OriginTypeRPC represents thee Reverse Proxy Cache origin type
	OriginTypeRPC = OriginType(iota)
	// OriginTypeRule represents the Ruler origin type
	OriginTypeRule
	// OriginTypePrometheus represents the Prometheus origin type
	OriginTypePrometheus
	// OriginTypeInfluxDB represents the InfluxDB origin type
	OriginTypeInfluxDB
	// OriginTypeIronDB represents the IRONdb origin type
	OriginTypeIronDB
	// OriginTypeClickHouse represents the ClickHouse origin type
	OriginTypeClickHouse
)

var Names = map[string]OriginType{
	"rule":              OriginTypeRule,
	"reverseproxycache": OriginTypeRPC,
	"rpc":               OriginTypeRPC,
	"prometheus":        OriginTypePrometheus,
	"influxdb":          OriginTypeInfluxDB,
	"irondb":            OriginTypeIronDB,
	"clickhouse":        OriginTypeClickHouse,
}

var Values = make(map[OriginType]string)

func init() {
	for k, v := range Names {
		Values[v] = k
	}
	// ensure consistent reverse mapping for reverseproxycache as rpc
	Values[OriginTypeRPC] = "rpc"
}

func (t OriginType) String() string {
	if v, ok := Values[t]; ok {
		return v
	}
	return strconv.Itoa(int(t))
}

// IsValidOriginType returns true if the provided OriginType is valid for use with Trickster
func IsValidOriginType(t string) bool {
	_, ok := Names[t]
	return ok
}
