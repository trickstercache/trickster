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

package options

import "github.com/trickstercache/trickster/v2/pkg/config/types"

const (
	// DefaultTimestampField is the Elasticsearch timestamp field used when a
	// backend does not provide an explicit timestamp_field.
	DefaultTimestampField = "@timestamp"
)

// Options holds Elasticsearch-specific backend options.
type Options struct {
	// TimestampField is the Elasticsearch date field Trickster uses to detect,
	// normalize, and rewrite time range filters for date_histogram requests.
	TimestampField string `yaml:"timestamp_field,omitempty"`
}

var _ types.ConfigOptions[Options] = &Options{}

// New returns a default Elasticsearch options object.
func New() *Options {
	return &Options{TimestampField: DefaultTimestampField}
}

// Clone returns an exact copy of the subject Options.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	return &Options{TimestampField: o.TimestampField}
}

// Initialize sets option defaults.
func (o *Options) Initialize(_ string) error {
	if o.TimestampField == "" {
		o.TimestampField = DefaultTimestampField
	}
	return nil
}

// Validate validates the Elasticsearch options.
func (o *Options) Validate() (bool, error) {
	return true, nil
}
