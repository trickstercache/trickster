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

// Package options defines the catalog settings for the consul autodiscovery
// provider.
package options

import (
	"errors"
	"fmt"
	"time"

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

const (
	// DefaultWait is the default blocking-query wait
	DefaultWait = 5 * time.Minute
	// MinimumWait is the lowest permitted blocking-query wait
	MinimumWait = time.Second
	// MaximumWait is the highest wait Consul honors; larger values are
	// silently clamped by the server, so reject them instead
	MaximumWait = 10 * time.Minute
	// WaitTimeoutFloor is the fixed part of the margin between the
	// blocking-query wait and the poll timeout that must outlast it
	WaitTimeoutFloor = 10 * time.Second
)

var (
	// ErrWaitTooLow is returned when the blocking-query wait is below the minimum
	ErrWaitTooLow = errors.New("'wait' must be at least 1s")
	// ErrWaitTooHigh is returned when the wait exceeds what Consul honors
	ErrWaitTooHigh = errors.New(
		"'wait' must be at most 10m, which is the longest Consul honors")
)

// PollTimeout returns the poll timeout that must bound a blocking query of
// the given wait. Consul adds up to wait/16 of its own jitter before
// returning, so a timeout of merely wait would abort perfectly healthy long
// polls; the margin covers that jitter plus round-trip slack.
func PollTimeout(wait time.Duration) time.Duration {
	return wait + wait/16 + WaitTimeoutFloor
}

// Options defines the catalog settings for a discoverer with the 'consul'
// provider. Connection settings live in the shared 'http' block, where
// 'endpoint' is the agent or server address (commonly
// http://127.0.0.1:8500) and the ACL token is supplied either as an
// X-Consul-Token header or, for a rotated credential, via
// bearer_token_file -- Consul accepts the Authorization Bearer scheme as an
// equivalent to its own header.
type Options struct {
	// Datacenter queries a datacenter other than the agent's own
	Datacenter string `yaml:"datacenter,omitempty"`
	// Namespace and Partition scope the query on Consul Enterprise
	Namespace string `yaml:"namespace,omitempty"`
	Partition string `yaml:"partition,omitempty"`
	// Wait is the maximum time a blocking query parks on the server before
	// returning unchanged. It is what makes this provider event-driven
	// rather than polled: membership changes are observed within a round
	// trip, and an unchanged service costs one parked connection per Wait.
	Wait timeconv.Duration `yaml:"wait,omitempty"`
	// AllowStale permits any Consul server to answer rather than only the
	// leader, trading a small staleness window for lower latency and much
	// lower load on the leader. Recommended for discovery, which is
	// already eventually consistent by nature.
	AllowStale bool `yaml:"allow_stale,omitempty"`
	// OnlyPassing asks Consul to return only instances whose checks all
	// pass. The default is false, so that failing instances are reported as
	// NotReady rather than vanishing -- which lets an ALB using
	// health_mode: probe decide for itself, and keeps a wholly-unhealthy
	// service from looking like an empty one.
	OnlyPassing bool `yaml:"only_passing,omitempty"`
	// WarningIsReady controls how an instance whose worst check is
	// 'warning' is reported. Consul treats warning as still-serving for DNS
	// purposes, so this defaults to true; set it false to drain warning
	// instances out of pools.
	WarningIsReady *bool `yaml:"warning_is_ready,omitempty"`
}

// New returns an Options with default values
func New() *Options { return &Options{} }

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := pointers.Clone(o)
	if o.WarningIsReady != nil {
		out.WarningIsReady = pointers.Clone(o.WarningIsReady)
	}
	return out
}

// GetWait returns the configured blocking-query wait, or the default.
func (o *Options) GetWait() time.Duration {
	if o == nil || o.Wait <= 0 {
		return DefaultWait
	}
	return time.Duration(o.Wait)
}

// GetWarningIsReady reports whether a warning instance counts as ready,
// defaulting to true.
func (o *Options) GetWarningIsReady() bool {
	if o == nil || o.WarningIsReady == nil {
		return true
	}
	return *o.WarningIsReady
}

// Validate validates the Options against the poll timeout that will bound
// its blocking queries. A timeout that does not outlast the wait aborts
// every long poll, turning an event-driven provider into a failing one;
// catching it at startup beats a stream of timeouts in production.
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
			"'http.timeout' must be greater than 'consul.wait' (recommended: %s)",
			PollTimeout(wait))
	}
	return nil
}

// NewErrInvalidOptions returns an error for an invalid `consul` options block.
// It lives here rather than in pkg/discovery/options so that the base
// options package carries no per-provider constructors: a new provider
// brings its own error with it.
func NewErrInvalidOptions(name, detail string) error {
	return derrors.NewInvalidOptions("consul", name, detail)
}
