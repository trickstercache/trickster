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
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"

	"go.yaml.in/yaml/v3"
)

// Options stores options specific to InfluxDB backends.
type Options struct {
	// FlightUpstreamAddress overrides the upstream Apache Arrow Flight SQL
	// address used by an influxdb-protocol native listener mapped to this
	// backend. Defaults to the host:port from the backend's origin_url.
	FlightUpstreamAddress string `yaml:"flight_upstream_address,omitempty"`
}

// New returns a new InfluxDB Options with default values.
func New() *Options {
	return &Options{}
}

// Clone returns a copy of the subject Options.
func (o *Options) Clone() *Options {
	return pointers.Clone(o)
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
