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

// Package options configures how an HTTP listener normalizes request paths
// before routing them.
package options

import (
	"errors"
	"fmt"
)

const (
	// DotSegmentsNormalize removes "." and ".." segments as RFC 3986 section 5.2.4 prescribes.
	DotSegmentsNormalize = "normalize"
	// DotSegmentsReject refuses any request whose path holds a dot-segment with 400.
	DotSegmentsReject = "reject"
	// DotSegmentsOff routes and forwards dot-segments as received.
	DotSegmentsOff = "off"

	// EscapedSlashesKeep treats %2F as data within a segment and forwards it encoded.
	EscapedSlashesKeep = "keep"
	// EscapedSlashesReject refuses any request whose path holds %2F with 400.
	EscapedSlashesReject = "reject"
	// EscapedSlashesUnescape decodes %2F to a segment separator before normalizing.
	EscapedSlashesUnescape = "unescape"

	// DefaultDotSegments is the dot-segment handling used when none is configured.
	DefaultDotSegments = DotSegmentsNormalize
	// DefaultEscapedSlashes is the %2F handling used when none is configured.
	DefaultEscapedSlashes = EscapedSlashesKeep
)

var (
	// ErrInvalidDotSegments indicates an unsupported dot_segments value.
	ErrInvalidDotSegments = errors.New("dot_segments must be one of normalize, reject or off")
	// ErrInvalidEscapedSlashes indicates an unsupported escaped_slashes value.
	ErrInvalidEscapedSlashes = errors.New("escaped_slashes must be one of keep, reject or unescape")
)

// Options describes a listener's request path normalization.
type Options struct {
	// DotSegments selects how "." and ".." segments are handled: normalize, reject or off.
	DotSegments string `yaml:"dot_segments,omitempty"`
	// MergeSlashes collapses each run of literal slashes in the path to one.
	MergeSlashes bool `yaml:"merge_slashes,omitempty"`
	// EscapedSlashes selects how %2F in the path is handled: keep, reject or unescape.
	EscapedSlashes string `yaml:"escaped_slashes,omitempty"`
}

// New returns the default path normalization options.
func New() *Options {
	return &Options{DotSegments: DefaultDotSegments, EscapedSlashes: DefaultEscapedSlashes}
}

// Resolved returns a copy of the options with each unset value replaced by its default.
// A nil receiver resolves to the defaults.
func (o *Options) Resolved() Options {
	out := *New()
	if o == nil {
		return out
	}
	out.MergeSlashes = o.MergeSlashes
	if o.DotSegments != "" {
		out.DotSegments = o.DotSegments
	}
	if o.EscapedSlashes != "" {
		out.EscapedSlashes = o.EscapedSlashes
	}
	return out
}

// Validate reports whether every configured value is supported.
func (o *Options) Validate() error {
	r := o.Resolved()
	switch r.DotSegments {
	case DotSegmentsNormalize, DotSegmentsReject, DotSegmentsOff:
	default:
		return fmt.Errorf("%w: got %q", ErrInvalidDotSegments, r.DotSegments)
	}
	switch r.EscapedSlashes {
	case EscapedSlashesKeep, EscapedSlashesReject, EscapedSlashesUnescape:
	default:
		return fmt.Errorf("%w: got %q", ErrInvalidEscapedSlashes, r.EscapedSlashes)
	}
	return nil
}

// Clone returns a copy of the options.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	return &out
}

// Equal reports whether both options normalize paths identically.
func (o *Options) Equal(other *Options) bool {
	return o.Resolved() == other.Resolved()
}
