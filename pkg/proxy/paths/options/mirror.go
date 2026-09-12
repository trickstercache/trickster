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

package options

import "errors"

const (
	// DefaultMirrorMaxInFlight bounds the mirrored requests in progress at once per path.
	DefaultMirrorMaxInFlight = 64
	// DefaultMirrorPercent is the share of requests mirrored when percent is unset.
	DefaultMirrorPercent = 100
)

var (
	// ErrMirrorBackendRequired is returned for a mirror that names no backend.
	ErrMirrorBackendRequired = errors.New("mirror backend_name is required")
	// ErrInvalidMirrorPercent is returned when the mirror percentage is out of range.
	ErrInvalidMirrorPercent = errors.New("mirror percent must be between 1 and 100")
	// ErrInvalidMirrorInFlight is returned for a negative in-flight bound.
	ErrInvalidMirrorInFlight = errors.New("mirror max_in_flight must not be negative")
)

// MirrorOptions duplicates a share of a path's requests to another backend,
// whose responses are discarded.
type MirrorOptions struct {
	// BackendName is the backend that receives the mirrored requests
	BackendName string `yaml:"backend_name,omitempty"`
	// Percent is the share of requests mirrored, 1-100; unset mirrors all of them
	Percent int `yaml:"percent,omitempty"`
	// MaxInFlight bounds mirrored requests in progress; further ones are dropped
	MaxInFlight int `yaml:"max_in_flight,omitempty"`
}

// Clone returns a copy of the mirror options.
func (m *MirrorOptions) Clone() *MirrorOptions {
	if m == nil {
		return nil
	}
	out := *m
	return &out
}

// Validate checks the mirror settings.
func (m *MirrorOptions) Validate() error {
	if m == nil {
		return ErrMirrorBackendRequired
	}
	if m.BackendName == "" {
		return ErrMirrorBackendRequired
	}
	if m.Percent < 0 || m.Percent > 100 {
		return ErrInvalidMirrorPercent
	}
	if m.MaxInFlight < 0 {
		return ErrInvalidMirrorInFlight
	}
	return nil
}

// ResolvedPercent returns the share of requests mirrored, defaulted when unset.
func (m *MirrorOptions) ResolvedPercent() int {
	if m == nil || m.Percent == 0 {
		return DefaultMirrorPercent
	}
	return m.Percent
}

// ResolvedMaxInFlight returns the in-flight bound, defaulted when unset.
func (m *MirrorOptions) ResolvedMaxInFlight() int {
	if m == nil || m.MaxInFlight == 0 {
		return DefaultMirrorMaxInFlight
	}
	return m.MaxInFlight
}

// Equal reports whether both mirror configurations are the same.
func (m *MirrorOptions) Equal(o *MirrorOptions) bool {
	if m == nil || o == nil {
		return m == o
	}
	return *m == *o
}
