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

const (
	// DefaultMaxSizeBytes is the default maximum byte cost memory provider will admit to the cache (512 MB)
	DefaultMaxSizeBytes = int64(512 * 1024 * 1024)
	// DefaultNumCounters is the default number of keys memory provider tracks for admission control
	DefaultNumCounters = int64(500_000)
)

// Options holds memory-cache-specific configuration.
type Options struct {
	// MaxSizeBytes is the maximum total byte cost memory provider will admit to the cache.
	// Defaults to 512MB.
	MaxSizeBytes int64 `yaml:"max_size_bytes,omitempty"`
	// NumCounters is the number of keys memory provider tracks for admission control. Not a hard limit, but a sampling size.
	// Recommended to use ~10x the number of unique keys you expect to hold for full utilization.
	// Defaults to 500,000.
	NumCounters int64 `yaml:"num_counters,omitempty"`
}

// New returns a new Options with default values set.
func New() *Options {
	return &Options{
		MaxSizeBytes: DefaultMaxSizeBytes,
		NumCounters:  DefaultNumCounters,
	}
}

// Equal returns true if all members of the subject and provided Options are identical.
func (o *Options) Equal(o2 *Options) bool {
	if o2 == nil {
		return false
	}
	return o.MaxSizeBytes == o2.MaxSizeBytes && o.NumCounters == o2.NumCounters
}

// UnmarshalYAML applies defaults before overlaying YAML-parsed values.
func (o *Options) UnmarshalYAML(unmarshal func(any) error) error {
	type loadOptions Options
	lo := loadOptions(*(New()))
	if err := unmarshal(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
