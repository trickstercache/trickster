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

// Package options defines the options of the header geo locator provider.
package options

import (
	"errors"
	"fmt"
	"slices"

	"golang.org/x/net/http/httpguts"
)

// Options configures a header locator
type Options struct {
	// Country is the request header holding an ISO 3166-1 alpha-2 country code
	Country string `yaml:"country,omitempty"`
	// Subdivision is the request header holding a first-level subdivision code, with or without its country
	Subdivision string `yaml:"subdivision,omitempty"`
	// Continent is the request header holding a continent code
	Continent string `yaml:"continent,omitempty"`
	// UnknownValues are header values that mean no location
	UnknownValues []string `yaml:"unknown_values,omitempty"`
}

// DefaultUnknownValues are the values that CDNs send for a client they cannot place
var DefaultUnknownValues = []string{"XX", "T1", "ZZ", "-"}

// ErrNoCountryHeader is returned when a header locator names no country header
var ErrNoCountryHeader = errors.New("'country' is required")

// New returns new header Options
func New() *Options {
	return &Options{}
}

// Clone returns a copy of the Options
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	out.UnknownValues = slices.Clone(o.UnknownValues)
	return &out
}

// Initialize sets the default unknown values
func (o *Options) Initialize() {
	if o.UnknownValues == nil {
		o.UnknownValues = slices.Clone(DefaultUnknownValues)
	}
}

// Validate checks the Options
func (o *Options) Validate() error {
	if o.Country == "" {
		return ErrNoCountryHeader
	}
	for _, h := range [...]struct{ field, name string }{
		{"country", o.Country}, {"subdivision", o.Subdivision}, {"continent", o.Continent},
	} {
		if h.name != "" && !httpguts.ValidHeaderFieldName(h.name) {
			return fmt.Errorf("'%s': %q is not a valid header name", h.field, h.name)
		}
	}
	return nil
}
