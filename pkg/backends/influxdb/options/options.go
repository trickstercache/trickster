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

// Package options provides InfluxDB-specific backend options.
package options

import (
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"

	"go.yaml.in/yaml/v3"
)

// Options stores options specific to InfluxDB backends.
type Options struct {
	// FlightUpstreamAddress overrides the upstream Apache Arrow Flight SQL
	// address used by a flight-sql-protocol native listener mapped to this
	// backend. Defaults to the host:port from the backend's origin_url.
	FlightUpstreamAddress string `yaml:"flight_upstream_address,omitempty"`
	// FlightUpstreamTLS dials the upstream Flight SQL endpoint with TLS.
	// Certificate verification honors the backend's tls block
	// (insecure_skip_verify).
	FlightUpstreamTLS bool `yaml:"flight_upstream_tls,omitempty"`
	// FlightCacheTTL bounds the lifetime of cached Flight SQL response bytes.
	// Defaults to 60s when unset.
	FlightCacheTTL timeconv.Duration `yaml:"flight_cache_ttl,omitempty"`
	// FlightMaxResponseBytes bounds how many bytes a single upstream Flight SQL
	// response may buffer. Responses are buffered whole so they can be cached,
	// so this is what keeps one very large query from exhausting the heap.
	// Defaults to 128MiB; a negative value removes the bound.
	FlightMaxResponseBytes int64 `yaml:"flight_max_response_bytes,omitempty"`
	// FlightMaxBufferedBytes bounds response bytes concurrently assembled or
	// streamed across Flight SQL RPCs. Defaults to 512MiB.
	FlightMaxBufferedBytes int64 `yaml:"flight_max_buffered_bytes,omitempty"`
	// FlightAllowedLocationHosts lists exact host:port authorities that an
	// upstream may advertise for endpoint retrieval. Credentials are forwarded
	// only to listed authorities.
	FlightAllowedLocationHosts []string `yaml:"flight_allowed_location_hosts,omitempty"`
	// FlightMaxLocationClients caps cached alternate endpoint connections.
	// Defaults to 16.
	FlightMaxLocationClients int `yaml:"flight_max_location_clients,omitempty"`
}

// New returns a new InfluxDB Options with default values.
func New() *Options {
	return &Options{}
}

// Clone returns a copy of the subject Options.
func (o *Options) Clone() *Options {
	clone := pointers.Clone(o)
	if clone != nil {
		clone.FlightAllowedLocationHosts = slices.Clone(o.FlightAllowedLocationHosts)
	}
	return clone
}

// UnmarshalYAML unmarshals onto default values so omitted fields keep them.
func (o *Options) UnmarshalYAML(value *yaml.Node) error {
	type loadOptions Options
	lo := loadOptions(*New())
	if err := value.Decode(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
