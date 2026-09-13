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

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"

	"go.yaml.in/yaml/v3"
)

// Options stores the Graphite-specific backend options
type Options struct {
	// TimeZone names the origin's TIME_ZONE, used to interpret date-anchored
	// from/until values when the request carries no tz parameter
	TimeZone string `yaml:"time_zone,omitempty"`
	// OriginUsername and OriginPassword form a static HTTP Basic credential
	// every request to the origin carries, proxied and synthetic alike
	OriginUsername string `yaml:"origin_username,omitempty"`
	OriginPassword string `yaml:"origin_password,omitempty"`
	// OriginAuthorization is a verbatim Authorization header value (e.g.,
	// 'Bearer <token>'); mutually exclusive with OriginUsername/OriginPassword
	OriginAuthorization string `yaml:"origin_authorization,omitempty"`
	// PassthroughMaxDataPoints forwards the client's maxDataPoints to the origin
	// instead of consolidating in Trickster; such requests are not accelerated
	PassthroughMaxDataPoints bool `yaml:"passthrough_max_data_points,omitempty"`
	// ResolutionRegistry configures the step-resolution registry and its
	// probe engine
	ResolutionRegistry RegistryOptions `yaml:"resolution_registry,omitempty"`
	// StaticRetentions is an ordered, first-match-wins list of storage-schemas.conf-shaped
	// retention rules seeding resolution; a static match is still probed, and the probe wins
	StaticRetentions []StaticRetention `yaml:"static_retentions,omitempty"`
	// FindCacheTTL is how long a wildcard expansion from /metrics/find is
	// reused before being refreshed
	FindCacheTTL timeconv.Duration `yaml:"find_cache_ttl,omitempty"`
	// MaxTargetsPerRequest bounds how many targets one render request may carry and
	// still be accelerated; over the limit it uses the object lane. 0 means the default.
	MaxTargetsPerRequest int `yaml:"max_targets_per_request,omitempty"`
	// MaxTargetLength bounds one target expression's length in bytes; a longer
	// target is served through the object lane. 0 means the default.
	MaxTargetLength int `yaml:"max_target_length,omitempty"`
	// MaxExpandedLeaves bounds how many leaf paths one wildcard expansion may resolve
	// to; over the limit the request uses the object lane. 0 means the default.
	MaxExpandedLeaves int `yaml:"max_expanded_leaves,omitempty"`
	// MaxExpansionBytes bounds the aggregate decoded leaf-name bytes of
	// one wildcard expansion. 0 means the default.
	MaxExpansionBytes int `yaml:"max_expansion_bytes,omitempty"`
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
	// restart does not relearn them
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
		FindCacheTTL:         timeconv.Duration(DefaultFindCacheTTL),
		MaxTargetsPerRequest: DefaultMaxTargetsPerRequest,
		MaxTargetLength:      DefaultMaxTargetLength,
		MaxExpandedLeaves:    DefaultMaxExpandedLeaves,
		MaxExpansionBytes:    DefaultMaxExpansionBytes,
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
