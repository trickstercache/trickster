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

// Package options defines the resolver settings for the dns_srv and dns_a
// autodiscovery providers.
package options

import (
	"errors"
	"net"
	"time"

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

const (
	// DefaultInterval is the default DNS poll cadence
	DefaultInterval = 30 * time.Second
	// MinimumInterval is the lowest permitted DNS poll cadence
	MinimumInterval = time.Second
)

var (
	// ErrInvalidResolver is returned when the resolver is not a host:port
	ErrInvalidResolver = errors.New("'resolver' must be a host:port")
	// ErrIntervalTooLow is returned when the poll cadence is below the minimum
	ErrIntervalTooLow = errors.New("'interval' must be at least 1s")
)

// Options defines the resolver settings for a discoverer with the
// 'dns_srv' or 'dns_a' provider
type Options struct {
	// Resolver is the host:port of the DNS server to query. When empty, the
	// system resolver is used.
	Resolver string `yaml:"resolver,omitempty"`
	// Interval is the poll cadence for re-resolving records. Record TTLs act
	// as a floor: a record is never re-resolved before its TTL expires.
	Interval timeconv.Duration `yaml:"interval,omitempty"`
}

// New returns an Options with default values
func New() *Options {
	return &Options{Interval: timeconv.Duration(DefaultInterval)}
}

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options { return pointers.Clone(o) }

// Initialize applies defaults
func (o *Options) Initialize() {
	if o == nil {
		return
	}
	if o.Interval == 0 {
		o.Interval = timeconv.Duration(DefaultInterval)
	}
}

// Validate validates the Options
func (o *Options) Validate() error {
	if o == nil {
		return nil
	}
	if o.Resolver != "" {
		if _, _, err := net.SplitHostPort(o.Resolver); err != nil {
			return ErrInvalidResolver
		}
	}
	if o.Interval != 0 && time.Duration(o.Interval) < MinimumInterval {
		return ErrIntervalTooLow
	}
	return nil
}

// NewErrInvalidOptions returns an error for an invalid `dns` options block.
// It lives here rather than in pkg/discovery/options so that the base
// options package carries no per-provider constructors: a new provider
// brings its own error with it.
func NewErrInvalidOptions(name, detail string) error {
	return derrors.NewInvalidOptions("dns", name, detail)
}
