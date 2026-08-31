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

// Package options defines the registry settings for the nomad autodiscovery
// provider.
package options

import (
	"errors"
	"fmt"
	"time"

	consulopts "github.com/trickstercache/trickster/v2/pkg/discovery/consul/options"
	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

const (
	// DefaultWait is the default blocking-query wait, matching Nomad's own
	DefaultWait = 5 * time.Minute
	// MinimumWait is the lowest permitted blocking-query wait
	MinimumWait = time.Second
	// MaximumWait is the highest wait Nomad honors; larger values are
	// silently clamped by the server, so reject them instead
	MaximumWait = 10 * time.Minute
)

var (
	// ErrWaitTooLow is returned when the blocking-query wait is below the minimum
	ErrWaitTooLow = errors.New("'wait' must be at least 1s")
	// ErrWaitTooHigh is returned when the wait exceeds what Nomad honors
	ErrWaitTooHigh = errors.New(
		"'wait' must be at most 10m, which is the longest Nomad honors")
)

// PollTimeout returns the poll timeout that must bound a blocking query of
// the given wait. Nomad shares HashiCorp's blocking-query protocol with
// Consul, so the margin is the same.
func PollTimeout(wait time.Duration) time.Duration {
	return consulopts.PollTimeout(wait)
}

// Options defines the registry settings for a discoverer with the 'nomad'
// provider. Connection settings live in the shared 'http' block, where
// 'endpoint' is the agent address (commonly http://127.0.0.1:4646) and the
// ACL token is supplied either as an X-Nomad-Token header or, for a rotated
// credential, via bearer_token_file -- Nomad accepts the Authorization
// Bearer scheme as an equivalent to its own header.
//
// This reads Nomad's *native* service registry. Jobs that register into
// Consul instead (provider = "consul" in the job's service block) are
// discovered with the consul provider, which additionally conveys health.
type Options struct {
	// Namespace scopes the query; Nomad defaults it to "default"
	Namespace string `yaml:"namespace,omitempty"`
	// Region queries a region other than the agent's own
	Region string `yaml:"region,omitempty"`
	// Wait is the maximum time a blocking query parks on the server before
	// returning unchanged, making the provider event-driven rather than
	// polled
	Wait timeconv.Duration `yaml:"wait,omitempty"`
	// AllowStale permits any Nomad server to answer rather than only the
	// leader, trading a small staleness window for lower leader load
	AllowStale bool `yaml:"allow_stale,omitempty"`
}

// New returns an Options with default values
func New() *Options { return &Options{} }

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options { return pointers.Clone(o) }

// GetWait returns the configured blocking-query wait, or the default.
func (o *Options) GetWait() time.Duration {
	if o == nil || o.Wait <= 0 {
		return DefaultWait
	}
	return time.Duration(o.Wait)
}

// Validate validates the Options against the poll timeout that will bound
// its blocking queries; see the consul package for why the relationship
// matters.
func (o *Options) Validate(pollTimeout time.Duration) error {
	wait := o.GetWait()
	if wait < MinimumWait {
		return ErrWaitTooLow
	}
	if wait > MaximumWait {
		return ErrWaitTooHigh
	}
	if pollTimeout > 0 && pollTimeout <= wait {
		return fmt.Errorf(
			"'http.timeout' must be greater than 'nomad.wait' (recommended: %s)",
			PollTimeout(wait))
	}
	return nil
}

// NewErrInvalidOptions returns an error for an invalid `nomad` options block.
// It lives here rather than in pkg/discovery/options so that the base
// options package carries no per-provider constructors: a new provider
// brings its own error with it.
func NewErrInvalidOptions(name, detail string) error {
	return derrors.NewInvalidOptions("nomad", name, detail)
}
