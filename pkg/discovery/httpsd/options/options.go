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

// Package options defines the payload settings for the http_sd
// autodiscovery provider.
package options

import (
	"errors"

	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// Member-list document formats accepted by the http_sd provider
const (
	// FormatTrickster is Trickster's native member list, carrying scheme,
	// path prefix, weight and replica group per member
	FormatTrickster = "trickster"
	// FormatPrometheus is the document Prometheus's own file_sd and http_sd
	// consume: [{"targets": [...], "labels": {...}}]
	FormatPrometheus = "prometheus"
)

// ErrInvalidFormat is returned when the member-list format is unrecognized
var ErrInvalidFormat = errors.New("'format' must be trickster or prometheus")

// Options defines the payload settings for a discoverer with the 'http_sd'
// provider. Connection settings live in the shared 'http' block.
type Options struct {
	// Format names the member-list document the endpoint serves:
	// 'trickster' (default) or 'prometheus'.
	//
	// It is explicit rather than sniffed. The two documents are structurally
	// distinguishable, but guessing means a typo in one format can parse as
	// a valid, wrong membership in the other -- and the cost of guessing
	// wrong is a silently drained pool.
	Format string `yaml:"format,omitempty"`
}

// New returns an Options with default values
func New() *Options { return &Options{Format: FormatTrickster} }

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options { return pointers.Clone(o) }

// Initialize applies defaults
func (o *Options) Initialize() {
	if o == nil {
		return
	}
	if o.Format == "" {
		o.Format = FormatTrickster
	}
}

// GetFormat returns the configured member-list format, defaulting to
// trickster. It tolerates a nil receiver so callers need not distinguish an
// absent block from an unset field.
func (o *Options) GetFormat() string {
	if o == nil || o.Format == "" {
		return FormatTrickster
	}
	return o.Format
}

// Validate validates the Options
func (o *Options) Validate() error {
	if f := o.GetFormat(); f != FormatTrickster && f != FormatPrometheus {
		return ErrInvalidFormat
	}
	return nil
}
