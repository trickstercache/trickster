/*
 * Copyright 2026 The Trickster Authors
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

// Package options provides settings for MoQ Transport backends and listeners.
package options

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

const (
	// DefaultLiveEdgeGroups is how many groups of each track the in-memory live edge keeps
	DefaultLiveEdgeGroups = 3
	// DefaultLiveEdgeSizeBytes bounds the payload bytes a backend's live edge holds
	DefaultLiveEdgeSizeBytes = 64 << 20
	// DefaultMaxObjectSizeBytes bounds one object's payload
	DefaultMaxObjectSizeBytes = 64 << 20
	// DefaultFillTimeout bounds the wait for upstream fetches when a request names no FILL_TIMEOUT
	DefaultFillTimeout = 2 * time.Second
	// DefaultReconnectBackoff is the first wait before re-subscribing after an upstream subscription is lost
	DefaultReconnectBackoff = time.Second
	// DefaultLinger keeps an upstream subscription this long after its last downstream subscriber leaves
	DefaultLinger = 5 * time.Second
	// DefaultMaxGapFetches bounds the upstream fetches one FETCH or fill issues
	DefaultMaxGapFetches = 16

	// DefaultHandshakeTimeout bounds the wait for a client's SETUP
	DefaultHandshakeTimeout = 5 * time.Second
	// DefaultIdleTimeout is the QUIC idle timeout of a listener's connections
	DefaultIdleTimeout = 2 * time.Minute
	// DefaultMaxSessions bounds the sessions one listener serves at once
	DefaultMaxSessions = 10000
	// DefaultMaxRequestsPerSession bounds the requests a client may have open at once
	DefaultMaxRequestsPerSession = 1024
	// DefaultMaxFilterRanges is the MAX_FILTER_RANGES a listener advertises
	DefaultMaxFilterRanges = 16
	// DefaultMaxRequestUpdates is the MAX_REQUEST_UPDATES a listener advertises
	DefaultMaxRequestUpdates = 64

	maxSizeBytes = 1 << 40
)

// Options contains the moq block of a backend
type Options struct {
	Upstream *UpstreamOptions `yaml:"upstream,omitempty"`
	LiveEdge *LiveEdgeOptions `yaml:"live_edge,omitempty"`
	// Spill writes groups leaving the live edge, and groups fetched upstream, to the backend's cache
	Spill bool `yaml:"spill"`
	// FillTimeout bounds the wait for upstream fetches when a FETCH or fill names no FILL_TIMEOUT
	FillTimeout timeconv.Duration `yaml:"fill_timeout,omitempty"`
	// MaxObjectSizeBytes bounds one object's payload; a larger one resets the stream carrying it
	MaxObjectSizeBytes int64 `yaml:"max_object_size_bytes,omitempty"`
}

// UpstreamOptions configure the sessions a backend opens toward its origin
type UpstreamOptions struct {
	// Drafts are the MoQ Transport drafts offered to the origin, most preferred first
	Drafts []int `yaml:"drafts,omitempty"`
	// Prewarm keeps an upstream subscription forwarding while every downstream subscriber is paused
	Prewarm bool `yaml:"prewarm,omitempty"`
	// ReconnectBackoff is the first wait before re-subscribing after an upstream subscription is lost
	ReconnectBackoff timeconv.Duration `yaml:"reconnect_backoff,omitempty"`
	// Linger keeps an upstream subscription this long after its last downstream subscriber leaves
	Linger timeconv.Duration `yaml:"linger,omitempty"`
	// MaxGapFetches bounds the upstream fetches one FETCH or fill issues for objects the relay lacks
	MaxGapFetches int `yaml:"max_gap_fetches,omitempty"`
}

// LiveEdgeOptions size the in-memory live edge
type LiveEdgeOptions struct {
	// Groups is how many groups of each track the live edge keeps
	Groups int `yaml:"groups,omitempty"`
	// MaxSizeBytes bounds the payload bytes the backend's live edge holds across all tracks
	MaxSizeBytes int64 `yaml:"max_size_bytes,omitempty"`
}

// New returns the default backend options
func New() *Options {
	return &Options{
		Upstream: &UpstreamOptions{
			Drafts: slices.Clone(codec.ClientOffer), ReconnectBackoff: timeconv.Duration(DefaultReconnectBackoff),
			Linger: timeconv.Duration(DefaultLinger), MaxGapFetches: DefaultMaxGapFetches,
		},
		LiveEdge:    &LiveEdgeOptions{Groups: DefaultLiveEdgeGroups, MaxSizeBytes: DefaultLiveEdgeSizeBytes},
		Spill:       true,
		FillTimeout: timeconv.Duration(DefaultFillTimeout), MaxObjectSizeBytes: DefaultMaxObjectSizeBytes,
	}
}

// Clone returns an independent copy
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	if o.Upstream != nil {
		u := *o.Upstream
		u.Drafts = slices.Clone(o.Upstream.Drafts)
		out.Upstream = &u
	}
	if o.LiveEdge != nil {
		l := *o.LiveEdge
		out.LiveEdge = &l
	}
	return &out
}

// Validate rejects unsupported drafts and out-of-range limits
func (o *Options) Validate() error {
	if o == nil {
		return nil
	}
	if o.Upstream == nil || o.LiveEdge == nil {
		return errors.New("moq: upstream and live_edge are required")
	}
	if err := validateDrafts("moq.upstream.drafts", o.Upstream.Drafts); err != nil {
		return err
	}
	switch {
	case o.Upstream.ReconnectBackoff <= 0:
		return errors.New("moq.upstream.reconnect_backoff must be greater than zero")
	case o.Upstream.Linger < 0:
		return errors.New("moq.upstream.linger must not be negative")
	case o.Upstream.MaxGapFetches <= 0:
		return errors.New("moq.upstream.max_gap_fetches must be greater than zero")
	case o.LiveEdge.Groups <= 0:
		return errors.New("moq.live_edge.groups must be greater than zero")
	case o.LiveEdge.MaxSizeBytes <= 0 || o.LiveEdge.MaxSizeBytes > maxSizeBytes:
		return fmt.Errorf("moq.live_edge.max_size_bytes must be between 1 and %d", int64(maxSizeBytes))
	case o.FillTimeout < 0:
		return errors.New("moq.fill_timeout must not be negative")
	case o.MaxObjectSizeBytes <= 0 || o.MaxObjectSizeBytes > maxSizeBytes:
		return fmt.Errorf("moq.max_object_size_bytes must be between 1 and %d", int64(maxSizeBytes))
	}
	return nil
}

// UnmarshalYAML overlays explicitly configured fields onto the defaults
func (o *Options) UnmarshalYAML(unmarshal func(any) error) error {
	type plain Options
	value := plain(*New())
	if err := unmarshal(&value); err != nil {
		return err
	}
	*o = Options(value)
	return nil
}

// UnmarshalYAML overlays explicitly configured fields onto the defaults
func (u *UpstreamOptions) UnmarshalYAML(unmarshal func(any) error) error {
	type plain UpstreamOptions
	value := plain(*New().Upstream)
	if err := unmarshal(&value); err != nil {
		return err
	}
	*u = UpstreamOptions(value)
	return nil
}

// UnmarshalYAML overlays explicitly configured fields onto the defaults
func (l *LiveEdgeOptions) UnmarshalYAML(unmarshal func(any) error) error {
	type plain LiveEdgeOptions
	value := plain(*New().LiveEdge)
	if err := unmarshal(&value); err != nil {
		return err
	}
	*l = LiveEdgeOptions(value)
	return nil
}

// ListenerOptions contains the moq block of a moq listener
type ListenerOptions struct {
	// HandshakeTimeout bounds the wait for a client's SETUP
	HandshakeTimeout timeconv.Duration `yaml:"handshake_timeout,omitempty"`
	// IdleTimeout is the QUIC idle timeout of the listener's connections
	IdleTimeout timeconv.Duration `yaml:"idle_timeout,omitempty"`
	// MaxSessions bounds the sessions the listener serves at once
	MaxSessions int `yaml:"max_sessions,omitempty"`
	// MaxRequestsPerSession bounds the requests a client may have open at once
	MaxRequestsPerSession int `yaml:"max_requests_per_session,omitempty"`
	// MaxFilterRanges is the MAX_FILTER_RANGES the listener advertises
	MaxFilterRanges int `yaml:"max_filter_ranges,omitempty"`
	// MaxRequestUpdates is the MAX_REQUEST_UPDATES the listener advertises
	MaxRequestUpdates int `yaml:"max_request_updates,omitempty"`
	// Drafts are the MoQ Transport drafts the listener accepts, most preferred first
	Drafts []int `yaml:"drafts,omitempty"`
	// Implementation overrides the MOQT_IMPLEMENTATION setup option
	Implementation string `yaml:"implementation,omitempty"`
}

// NewListener returns the default listener options
func NewListener() *ListenerOptions {
	return &ListenerOptions{
		HandshakeTimeout: timeconv.Duration(DefaultHandshakeTimeout), IdleTimeout: timeconv.Duration(DefaultIdleTimeout),
		MaxSessions: DefaultMaxSessions, MaxRequestsPerSession: DefaultMaxRequestsPerSession,
		MaxFilterRanges: DefaultMaxFilterRanges, MaxRequestUpdates: DefaultMaxRequestUpdates,
		Drafts: slices.Clone(codec.ServerPreference),
	}
}

// Clone returns an independent copy
func (o *ListenerOptions) Clone() *ListenerOptions {
	if o == nil {
		return nil
	}
	out := *o
	out.Drafts = slices.Clone(o.Drafts)
	return &out
}

// Equal reports whether both options are the same
func (o *ListenerOptions) Equal(other *ListenerOptions) bool {
	if o == nil || other == nil {
		return o == other
	}
	return o.HandshakeTimeout == other.HandshakeTimeout && o.IdleTimeout == other.IdleTimeout &&
		o.MaxSessions == other.MaxSessions && o.MaxRequestsPerSession == other.MaxRequestsPerSession &&
		o.MaxFilterRanges == other.MaxFilterRanges && o.MaxRequestUpdates == other.MaxRequestUpdates &&
		slices.Equal(o.Drafts, other.Drafts) && o.Implementation == other.Implementation
}

// Validate rejects unsupported drafts and disabled limits
func (o *ListenerOptions) Validate() error {
	if o == nil {
		return nil
	}
	if err := validateDrafts("moq.drafts", o.Drafts); err != nil {
		return err
	}
	switch {
	case o.HandshakeTimeout <= 0 || o.IdleTimeout <= 0:
		return errors.New("moq listener timeouts must be greater than zero")
	case o.MaxSessions <= 0 || o.MaxRequestsPerSession <= 0:
		return errors.New("moq.max_sessions and moq.max_requests_per_session must be greater than zero")
	case o.MaxFilterRanges < 0 || o.MaxRequestUpdates < 0:
		return errors.New("moq.max_filter_ranges and moq.max_request_updates must not be negative")
	}
	return nil
}

// UnmarshalYAML overlays explicitly configured fields onto the defaults
func (o *ListenerOptions) UnmarshalYAML(unmarshal func(any) error) error {
	type plain ListenerOptions
	value := plain(*NewListener())
	if err := unmarshal(&value); err != nil {
		return err
	}
	*o = ListenerOptions(value)
	return nil
}

func validateDrafts(field string, drafts []int) error {
	if len(drafts) == 0 {
		return fmt.Errorf("%s must name at least one draft", field)
	}
	seen := make(map[int]bool, len(drafts))
	for _, d := range drafts {
		if _, ok := codec.ForDraft(d); !ok {
			return fmt.Errorf("%s: draft %d is not supported; supported drafts are %v", field, d, codec.Drafts())
		}
		if seen[d] {
			return fmt.Errorf("%s names draft %d twice", field, d)
		}
		seen[d] = true
	}
	return nil
}
