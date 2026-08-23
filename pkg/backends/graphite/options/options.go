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
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"

	"go.yaml.in/yaml/v3"
)

// DefaultTimeZone is the time zone assumed for date-anchored from/until
// values (midnight, today, MM/DD/YY, ...) when the request has no tz
// parameter. It should match the origin's graphite-web TIME_ZONE setting.
const DefaultTimeZone = "UTC"

// Resolution registry defaults
const (
	// DefaultRegistryTTL is how long a learned ladder is trusted. Whisper
	// ladders only change by operator action (whisper-resize.py), so this is
	// long; a misprediction bumps the registry generation regardless.
	DefaultRegistryTTL = 24 * time.Hour
	// DefaultNegativeTTL is the initial backoff after a failed resolution;
	// it doubles per consecutive failure up to DefaultNegativeTTLMax
	DefaultNegativeTTL    = 30 * time.Second
	DefaultNegativeTTLMax = 10 * time.Minute
	// DefaultMaxEntries bounds each registry layer
	DefaultMaxEntries = 100000
	// DefaultProbeConcurrency caps concurrent ladder-learning runs per backend
	DefaultProbeConcurrency = 2
	// DefaultProbeBudget caps the probes one learning run may issue
	DefaultProbeBudget = 96
	// DefaultFindCacheTTL is how long a wildcard expansion is reused
	DefaultFindCacheTTL = time.Minute
)

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
	// ResolutionRegistry configures the step-resolution registry and its
	// probe engine
	ResolutionRegistry RegistryOptions `yaml:"resolution_registry,omitempty"`
	// StaticRetentions is an ordered, first-match-wins list of
	// storage-schemas.conf-shaped retention rules used as a seed and
	// override for resolution. It is never the sole source of truth: a
	// static match is probed for confirmation, and the probe wins.
	StaticRetentions []StaticRetention `yaml:"static_retentions,omitempty"`
	// FindCacheTTL is how long a wildcard expansion from /metrics/find is
	// reused before being refreshed
	FindCacheTTL timeconv.Duration `yaml:"find_cache_ttl,omitempty"`
}

// RegistryOptions configures the resolution registry
type RegistryOptions struct {
	// TTL is how long a learned ladder and its leaf bindings are trusted
	TTL timeconv.Duration `yaml:"ttl,omitempty"`
	// NegativeTTL is the initial backoff after a failed resolution
	NegativeTTL timeconv.Duration `yaml:"negative_ttl,omitempty"`
	// MaxEntries bounds each layer of the registry
	MaxEntries int `yaml:"max_entries,omitempty"`
	// Persist writes learned ladders through to the backend's cache so a
	// restart does not relearn them (decision D6)
	Persist bool `yaml:"persist"`
	// ProbeConcurrency caps concurrent ladder-learning runs
	ProbeConcurrency int `yaml:"probe_concurrency,omitempty"`
	// ProbeBudget caps the probes one learning run may issue
	ProbeBudget int `yaml:"probe_budget,omitempty"`
}

// StaticRetention is one static_retentions rule
type StaticRetention struct {
	// Pattern is a regular expression matched against the metric path
	// (re.search semantics, as carbon applies storage-schemas.conf)
	Pattern string `yaml:"pattern"`
	// Retentions is the storage-schemas.conf retention list, e.g.
	// 10s:6h,1m:7d,10m:5y
	Retentions string `yaml:"retentions"`
}

// New returns a new Graphite Options with default values
func New() *Options {
	return &Options{
		TimeZone: DefaultTimeZone,
		ResolutionRegistry: RegistryOptions{
			TTL:              timeconv.Duration(DefaultRegistryTTL),
			NegativeTTL:      timeconv.Duration(DefaultNegativeTTL),
			MaxEntries:       DefaultMaxEntries,
			Persist:          true,
			ProbeConcurrency: DefaultProbeConcurrency,
			ProbeBudget:      DefaultProbeBudget,
		},
		FindCacheTTL: timeconv.Duration(DefaultFindCacheTTL),
	}
}

// Clone returns a copy of the Options
func (o *Options) Clone() *Options {
	out := pointers.Clone(o)
	if o.StaticRetentions != nil {
		out.StaticRetentions = slices.Clone(o.StaticRetentions)
	}
	return out
}

// UnmarshalYAML decodes the Options over a set of defaults
func (o *Options) UnmarshalYAML(value *yaml.Node) error {
	type loadOptions Options
	lo := loadOptions(*New())
	if err := value.Decode(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
