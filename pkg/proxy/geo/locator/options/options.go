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

// Package options defines the geo_locators configuration section: named, provider-backed lookups that place
// a client address, each with one options block for its provider.
package options

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	geofeedopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	headeropts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/header/options"
	mmdbopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/mmdb/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/providers"
)

// DefaultName is the locator a geo ACL uses when it names none
const DefaultName = "default"

// Lookup is a map of geo locator Options keyed by name
type Lookup map[string]*Options

// Options defines a named geo locator
type Options struct {
	// Provider is how the locator places clients: mmdb, geofeed or header
	Provider string `yaml:"provider,omitempty"`
	// MMDB configures the mmdb provider
	MMDB *mmdbopts.Options `yaml:"mmdb,omitempty"`
	// Geofeed configures the geofeed provider
	Geofeed *geofeedopts.Options `yaml:"geofeed,omitempty"`
	// Header configures the header provider
	Header *headeropts.Options `yaml:"header,omitempty"`
	// Name is the locator's name, from its key in the Lookup
	Name string `yaml:"-"`
}

var (
	// ErrInvalidName is wrapped by the error for an empty or reserved locator name
	ErrInvalidName = errors.New("invalid geo locator name")
	// ErrMissingProvider is wrapped by the error for a locator with no provider
	ErrMissingProvider = errors.New("missing provider")
	// ErrInvalidProvider is wrapped by the error for an unsupported provider
	ErrInvalidProvider = errors.New("invalid provider")
	// ErrInvalidBlock is wrapped by the error for another provider's options block
	ErrInvalidBlock = errors.New("options block does not match the provider")
)

// New returns new geo locator Options
func New() *Options {
	return &Options{}
}

// Clone returns a copy of the Options
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	return &Options{
		Provider: o.Provider,
		MMDB:     o.MMDB.Clone(),
		Geofeed:  o.Geofeed.Clone(),
		Header:   o.Header.Clone(),
		Name:     o.Name,
	}
}

// Equal reports whether two Options build the same locator
func (o *Options) Equal(other *Options) bool {
	return reflect.DeepEqual(o, other)
}

// Initialize normalizes the provider name and sets its block's defaults, creating the block when absent
func (o *Options) Initialize(name string) {
	if name != "" {
		o.Name = name
	}
	o.Provider = strings.ToLower(strings.TrimSpace(o.Provider))
	switch o.Provider {
	case providers.MMDB:
		if o.MMDB == nil {
			o.MMDB = mmdbopts.New()
		}
		o.MMDB.Initialize()
	case providers.Geofeed:
		if o.Geofeed == nil {
			o.Geofeed = geofeedopts.New()
		}
		o.Geofeed.Initialize()
	case providers.Header:
		if o.Header == nil {
			o.Header = headeropts.New()
		}
		o.Header.Initialize()
	}
}

// Validate checks the Options without loading any database or file
func (o *Options) Validate() error {
	if o.Name == "" || reserved.IsReference(o.Name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, o.Name)
	}
	if o.Provider == "" {
		return o.wrap(ErrMissingProvider)
	}
	if !providers.IsValidProvider(o.Provider) {
		return o.wrap(fmt.Errorf("%w %q (expected one of %s)", ErrInvalidProvider, o.Provider, providers.Names()))
	}
	for _, b := range [...]struct {
		name    string
		present bool
	}{
		{providers.MMDB, o.MMDB != nil}, {providers.Geofeed, o.Geofeed != nil}, {providers.Header, o.Header != nil},
	} {
		if b.present && b.name != o.Provider {
			return o.wrap(fmt.Errorf("the %q %w %q", b.name, ErrInvalidBlock, o.Provider))
		}
	}
	var err error
	switch o.Provider {
	case providers.MMDB:
		err = o.MMDB.Validate()
	case providers.Geofeed:
		err = o.Geofeed.Validate()
	case providers.Header:
		err = o.Header.Validate()
	}
	if err != nil {
		return o.wrap(fmt.Errorf("%s: %w", o.Provider, err))
	}
	return nil
}

func (o *Options) wrap(err error) error {
	return fmt.Errorf("geo locator %q: %w", o.Name, err)
}

// Initialize initializes each Options in the Lookup, naming it by its key
func (l Lookup) Initialize() {
	for name, o := range l {
		if o != nil {
			o.Initialize(name)
		}
	}
}

// ReadsAddresses reports whether the named locator places a bare address, as stream and native listeners need;
// an undefined name reports true, its error being another check's
func (l Lookup) ReadsAddresses(name string) bool {
	o := l[name]
	return o == nil || providers.ReadsAddresses(o.Provider)
}

// Validate validates each Options in the Lookup, naming it by its key first; a nil entry is skipped
func (l Lookup) Validate() error {
	for name, o := range l {
		if o == nil {
			continue
		}
		o.Name = name
		if err := o.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns a copy of the Lookup
func (l Lookup) Clone() Lookup {
	if l == nil {
		return nil
	}
	out := make(Lookup, len(l))
	for name, o := range l {
		out[name] = o.Clone()
	}
	return out
}
