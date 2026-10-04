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

// Package registry maps geo locator provider names to the constructors of their locators.
package registry

import (
	"errors"
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/header"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/mmdb"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/providers"
)

// NewFunc builds a locator from its validated options
type NewFunc func(name string, o *options.Options) (locator.Locator, error)

var constructors = map[string]NewFunc{
	providers.Geofeed: func(name string, o *options.Options) (locator.Locator, error) {
		return geofeed.New(name, o.Geofeed)
	},
	providers.MMDB: func(name string, o *options.Options) (locator.Locator, error) {
		return mmdb.New(name, o.MMDB)
	},
	providers.Header: func(_ string, o *options.Options) (locator.Locator, error) {
		return header.New(o.Header), nil
	},
}

// ErrNilOptions is returned by New for nil options
var ErrNilOptions = errors.New("nil geo locator options")

// New builds the locator the options name a provider for
func New(o *options.Options) (locator.Locator, error) {
	if o == nil {
		return nil, ErrNilOptions
	}
	f, ok := constructors[o.Provider]
	if !ok {
		return nil, fmt.Errorf("geo locator %q: no implementation registered for provider %q", o.Name, o.Provider)
	}
	l, err := f(o.Name, o)
	if err != nil {
		return nil, fmt.Errorf("geo locator %q: %w", o.Name, err)
	}
	return l, nil
}
