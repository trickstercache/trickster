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

// Package options provides the Graphite-specific backend options
package options

import (
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"

	"go.yaml.in/yaml/v3"
)

// DefaultTimeZone is the time zone assumed for date-anchored from/until
// values (midnight, today, MM/DD/YY, ...) when the request has no tz
// parameter. It should match the origin's graphite-web TIME_ZONE setting.
const DefaultTimeZone = "UTC"

// Options stores information about Graphite Options. Fields are added as the
// decisions that shape them are recorded in
// the Graphite implementation plan (trickster-data, Phase 0).
type Options struct {
	// TimeZone names the origin's TIME_ZONE, used to interpret date-anchored
	// from/until values when the request carries no tz parameter
	TimeZone string `yaml:"time_zone,omitempty"`
	// PassthroughMaxDataPoints, when true, forwards the client's
	// maxDataPoints to the origin instead of stripping it and consolidating
	// in Trickster (decision D3). Requests carrying maxDataPoints are then
	// not accelerated, so that origin consolidation is byte-identical.
	PassthroughMaxDataPoints bool `yaml:"passthrough_max_data_points,omitempty"`
}

// New returns a new Graphite Options with default values
func New() *Options {
	return &Options{TimeZone: DefaultTimeZone}
}

// Clone returns a copy of the Options
func (o *Options) Clone() *Options {
	return pointers.Clone(o)
}

// UnmarshalYAML decodes the Options over a set of defaults
func (o *Options) UnmarshalYAML(value *yaml.Node) error {
	type loadOptions Options
	lo := loadOptions(*(New()))
	if err := value.Decode(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
