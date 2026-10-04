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

// Package options defines the options of the mmdb geo locator provider.
package options

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/providers"
)

// Schema names where a MaxMind DB file's records keep the location fields
type Schema uint8

const (
	// SchemaAuto chooses a schema from the file's database_type, confirmed against a record
	SchemaAuto Schema = iota + 1
	// SchemaGeoIP2 is MaxMind's layout, which DB-IP and IP2Location's MMDB editions follow
	SchemaGeoIP2
	// SchemaIPinfo is IPinfo's flat layout
	SchemaIPinfo
	// SchemaCustom takes the record paths from Fields
	SchemaCustom
)

// schema names, as configured
const (
	SchemaNameAuto   = "auto"
	SchemaNameGeoIP2 = "geoip2"
	SchemaNameIPinfo = "ipinfo"
	SchemaNameCustom = "custom"
)

var schemaNames = [...]string{SchemaNameAuto, SchemaNameGeoIP2, SchemaNameIPinfo, SchemaNameCustom}

// ErrInvalidSchema is returned when a name matches no schema
var ErrInvalidSchema = errors.New("invalid mmdb schema")

// ParseSchema returns the schema named by name, ignoring case and surrounding space; an empty name returns
// the zero value, meaning none was chosen
func ParseSchema(name string) (Schema, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, nil
	}
	for i, n := range schemaNames {
		if strings.EqualFold(name, n) {
			return Schema(i + 1), nil
		}
	}
	return 0, fmt.Errorf("%w: %q (expected one of %s)", ErrInvalidSchema, name, strings.Join(schemaNames[:], ", "))
}

// String returns the schema's name, or an empty string for the zero value
func (s Schema) String() string {
	if s == 0 || int(s) > len(schemaNames) {
		return ""
	}
	return schemaNames[s-1]
}

// MarshalText returns the name of the schema
func (s Schema) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

// UnmarshalText sets s to the schema named by text
func (s *Schema) UnmarshalText(text []byte) error {
	v, err := ParseSchema(string(text))
	if err != nil {
		return err
	}
	*s = v
	return nil
}

// Fields are the paths into a record where each location field is, for SchemaCustom. A path is a list of map
// keys and array indexes, such as [subdivisions, 0, iso_code].
type Fields struct {
	Country     []string `yaml:"country,omitempty"`
	Continent   []string `yaml:"continent,omitempty"`
	Subdivision []string `yaml:"subdivision,omitempty"`
}

// Options configures an mmdb locator
type Options struct {
	// Path is the MaxMind DB format file
	Path string `yaml:"path,omitempty"`
	// Schema says where the file's records keep the location fields; the zero value is auto
	Schema Schema `yaml:"schema,omitempty"`
	// Fields are the record paths for SchemaCustom
	Fields *Fields `yaml:"fields,omitempty"`
	// ReloadInterval is how often the file is checked, besides change events
	ReloadInterval timeconv.Duration `yaml:"reload_interval,omitempty"`
	// MaxAge is the build age past which a loaded file logs a warning; zero never warns
	MaxAge timeconv.Duration `yaml:"max_age,omitempty"`
}

var (
	// ErrNoPath is returned when an mmdb locator names no file
	ErrNoPath = errors.New("'path' is required")
	// ErrFieldsNotCustom is returned for 'fields' with a schema other than custom
	ErrFieldsNotCustom = errors.New("'fields' is valid only with 'schema: custom'")
	// ErrNoCountryField is returned for a custom schema with no country path
	ErrNoCountryField = errors.New("'schema: custom' requires 'fields.country'")
	// ErrNegativeMaxAge is returned for a negative max_age
	ErrNegativeMaxAge = errors.New("'max_age' may not be negative")
)

// New returns new mmdb Options
func New() *Options {
	return &Options{}
}

// Clone returns a copy of the Options
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	if o.Fields != nil {
		out.Fields = &Fields{
			Country:     slices.Clone(o.Fields.Country),
			Continent:   slices.Clone(o.Fields.Continent),
			Subdivision: slices.Clone(o.Fields.Subdivision),
		}
	}
	return &out
}

// Initialize sets the default reload interval
func (o *Options) Initialize() {
	if o.ReloadInterval == 0 {
		o.ReloadInterval = timeconv.Duration(providers.DefaultReloadInterval)
	}
}

// Validate checks the Options without opening the file as a database
func (o *Options) Validate() error {
	if o.Path == "" {
		return ErrNoPath
	}
	if err := providers.CheckReadable(o.Path); err != nil {
		return fmt.Errorf("'path': %w", err)
	}
	if o.Fields != nil && o.Schema != SchemaCustom {
		return ErrFieldsNotCustom
	}
	if o.Schema == SchemaCustom && (o.Fields == nil || len(o.Fields.Country) == 0) {
		return ErrNoCountryField
	}
	if o.MaxAge < 0 {
		return ErrNegativeMaxAge
	}
	return providers.ValidateReloadInterval(time.Duration(o.ReloadInterval))
}
