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

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	mechoptions "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// Evaluations name how a TSM ALB answers queries
const (
	// EvaluationMerge merges the query responses of the members (the default)
	EvaluationMerge = "merge"
	// EvaluationGlobal evaluates every query once, over the deduplicated raw series of every member
	EvaluationGlobal = "global"
)

// Deduplications name how global evaluation combines samples of series that differ only by replica labels
const (
	// DeduplicationPenalty keeps one replica's samples and switches replicas across gaps (the default)
	DeduplicationPenalty = "penalty"
	// DeduplicationChain keeps every distinct timestamp of every replica
	DeduplicationChain = "chain"
)

const (
	// DefaultMaxSamples bounds the samples one global evaluation may load at once
	DefaultMaxSamples = 50_000_000
	// DefaultQueryTimeout bounds one global evaluation
	DefaultQueryTimeout = 2 * time.Minute
	// DefaultMaxConcurrentQueries bounds the global evaluations an ALB runs at once
	DefaultMaxConcurrentQueries = 20

	metricNameLabel = "__name__"
)

var (
	// ErrInvalidEvaluation is returned for an evaluation that is not one of the Evaluation constants
	ErrInvalidEvaluation = errors.New("invalid tsm evaluation")
	// ErrInvalidDeduplication is returned for a deduplication that is not one of the Deduplication constants
	ErrInvalidDeduplication = errors.New("invalid tsm deduplication")
	// ErrInvalidReplicaLabel is returned for an empty, repeated or metric-name replica label
	ErrInvalidReplicaLabel = errors.New("invalid tsm replica label")
	// ErrInvalidGlobalLimit is returned for a negative global evaluation limit
	ErrInvalidGlobalLimit = errors.New("invalid tsm global evaluation limit")
	// ErrGlobalOnly is returned when a global evaluation setting is used without evaluation: global
	ErrGlobalOnly = errors.New("tsm setting requires evaluation: global")
)

// Options contains the YAML-configurable time-series merge behavior.
type Options struct {
	ConcurrencyOptions mechoptions.ConcurrencyOptions `yaml:",inline"`
	// DedupToleranceMs groups replica samples within this many milliseconds.
	// Nil or zero preserves exact-timestamp deduplication.
	DedupToleranceMs *int `yaml:"dedup_tolerance_ms,omitempty"`
	// Evaluation selects how queries are answered; see the Evaluation constants
	Evaluation string `yaml:"evaluation,omitempty"`
	// ReplicaLabels are left out of a raw series' identity, so series differing only by them are replicas
	ReplicaLabels []string `yaml:"replica_labels,omitempty"`
	// Deduplication selects how replicas' samples combine; see the Deduplication constants
	Deduplication string `yaml:"deduplication,omitempty"`
	// PartialResponse answers from the available members, with a warning, when one fails; nil is true
	PartialResponse *bool `yaml:"partial_response,omitempty"`
	// MaxSamples bounds the samples one evaluation may load at once; zero is DefaultMaxSamples
	MaxSamples int `yaml:"max_samples,omitempty"`
	// QueryTimeout bounds one evaluation, including its raw retrieval; zero is DefaultQueryTimeout
	QueryTimeout timeconv.Duration `yaml:"query_timeout,omitempty"`
	// MaxConcurrentQueries bounds the evaluations run at once; zero is DefaultMaxConcurrentQueries
	MaxConcurrentQueries int `yaml:"max_concurrent_queries,omitempty"`
	// MaxFetchedSeries bounds the raw series one evaluation retrieves before deduplication; zero is unlimited
	MaxFetchedSeries int `yaml:"max_fetched_series,omitempty"`
	// MaxFetchedSamples bounds the raw samples one evaluation retrieves before deduplication; zero is unlimited
	MaxFetchedSamples int64 `yaml:"max_fetched_samples,omitempty"`
}

// Clone returns a copy of the Options that shares no memory with it.
func (o Options) Clone() Options {
	c := o
	c.ConcurrencyOptions.QueryConcurrencyLimit = pointers.Clone(o.ConcurrencyOptions.QueryConcurrencyLimit)
	c.ConcurrencyOptions.QueryConcurrencyMultiplier = pointers.Clone(o.ConcurrencyOptions.QueryConcurrencyMultiplier)
	c.DedupToleranceMs = pointers.Clone(o.DedupToleranceMs)
	c.PartialResponse = pointers.Clone(o.PartialResponse)
	c.ReplicaLabels = slices.Clone(o.ReplicaLabels)
	return c
}

// IsGlobal reports whether the ALB evaluates queries over its members' raw series.
func (o *Options) IsGlobal() bool {
	return o != nil && o.Evaluation == EvaluationGlobal
}

// AllowsPartialResponse reports whether global evaluation answers from the available members when one fails.
func (o *Options) AllowsPartialResponse() bool {
	return o == nil || o.PartialResponse == nil || *o.PartialResponse
}

// GetDeduplication returns the configured deduplication, or DeduplicationPenalty.
func (o *Options) GetDeduplication() string {
	if o == nil || o.Deduplication == "" {
		return DeduplicationPenalty
	}
	return o.Deduplication
}

// GetMaxSamples returns the configured sample limit, or DefaultMaxSamples.
func (o *Options) GetMaxSamples() int {
	if o == nil || o.MaxSamples == 0 {
		return DefaultMaxSamples
	}
	return o.MaxSamples
}

// GetQueryTimeout returns the configured evaluation timeout, or DefaultQueryTimeout.
func (o *Options) GetQueryTimeout() time.Duration {
	if o == nil || o.QueryTimeout == 0 {
		return DefaultQueryTimeout
	}
	return time.Duration(o.QueryTimeout)
}

// GetMaxConcurrentQueries returns the configured evaluation concurrency, or DefaultMaxConcurrentQueries.
func (o *Options) GetMaxConcurrentQueries() int {
	if o == nil || o.MaxConcurrentQueries == 0 {
		return DefaultMaxConcurrentQueries
	}
	return o.MaxConcurrentQueries
}

// Validate checks the options that can be checked without the ALB's pool.
func (o *Options) Validate() error {
	switch o.Evaluation {
	case "", EvaluationMerge, EvaluationGlobal:
	default:
		return fmt.Errorf("%w %q; supported evaluations are %s, %s", ErrInvalidEvaluation, o.Evaluation,
			EvaluationMerge, EvaluationGlobal)
	}
	switch o.Deduplication {
	case "", DeduplicationPenalty, DeduplicationChain:
	default:
		return fmt.Errorf("%w %q; supported deduplications are %s, %s", ErrInvalidDeduplication,
			o.Deduplication, DeduplicationPenalty, DeduplicationChain)
	}
	for i, label := range o.ReplicaLabels {
		if strings.TrimSpace(label) == "" || label == metricNameLabel || slices.Contains(o.ReplicaLabels[:i], label) {
			return fmt.Errorf("%w %q", ErrInvalidReplicaLabel, label)
		}
	}
	if o.MaxSamples < 0 || o.QueryTimeout < 0 || o.MaxConcurrentQueries < 0 || o.MaxFetchedSeries < 0 ||
		o.MaxFetchedSamples < 0 {
		return ErrInvalidGlobalLimit
	}
	if !o.IsGlobal() {
		if setting := o.globalSetting(); setting != "" {
			return fmt.Errorf("%w: %s", ErrGlobalOnly, setting)
		}
	}
	return nil
}

// globalSetting names a global evaluation setting the options use, or returns ""
func (o *Options) globalSetting() string {
	switch {
	case len(o.ReplicaLabels) > 0:
		return "replica_labels"
	case o.Deduplication != "":
		return "deduplication"
	case o.PartialResponse != nil:
		return "partial_response"
	case o.MaxSamples != 0:
		return "max_samples"
	case o.QueryTimeout != 0:
		return "query_timeout"
	case o.MaxConcurrentQueries != 0:
		return "max_concurrent_queries"
	case o.MaxFetchedSeries != 0:
		return "max_fetched_series"
	case o.MaxFetchedSamples != 0:
		return "max_fetched_samples"
	}
	return ""
}
