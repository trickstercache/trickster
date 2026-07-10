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

// Package options defines configurable CORS response-header policies.
package options

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
	"golang.org/x/net/http/httpguts"
)

// Mode identifies how Trickster combines origin and configured CORS headers.
type Mode string

const (
	// ModePreserve leaves origin-provided CORS headers unchanged.
	ModePreserve Mode = "preserve"
	// ModeMerge preserves origin-provided CORS headers and applies configured overrides.
	ModeMerge Mode = "merge"
	// ModeReplace removes origin-provided CORS headers before applying configured headers.
	ModeReplace Mode = "replace"
	// ModeDisable removes all origin-provided CORS headers.
	ModeDisable Mode = "disable"
)

const corsHeaderPrefix = "access-control-"

// Options defines a CORS response-header policy.
type Options struct {
	Mode    Mode               `yaml:"mode,omitempty"`
	Headers types.EnvStringMap `yaml:"headers,omitempty"`
	legacy  bool
}

var _ types.ConfigOptions[Options] = &Options{}

// New returns the default replace policy.
func New() *Options {
	return &Options{Mode: ModeReplace}
}

// Legacy returns the backwards-compatible wildcard CORS policy.
func Legacy() *Options {
	return &Options{legacy: true}
}

// Clone returns an exact copy of the options.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := pointers.Clone(o)
	out.Headers = maps.Clone(o.Headers)
	return out
}

// Initialize normalizes configured values.
func (o *Options) Initialize(_ string) error {
	if o == nil {
		return nil
	}
	o.Mode = Mode(strings.ToLower(string(o.Mode)))
	if o.Mode == "" {
		o.Mode = ModeReplace
	}
	if o.Headers == nil && o.Mode != ModeDisable {
		o.Headers = make(types.EnvStringMap)
	}
	return nil
}

// Validate validates the CORS mode and configured response headers.
func (o *Options) Validate() (bool, error) {
	if o == nil {
		return true, nil
	}
	mode := Mode(strings.ToLower(string(o.Mode)))
	if mode == "" {
		mode = ModeReplace
	}
	switch mode {
	case ModePreserve, ModeMerge, ModeReplace, ModeDisable:
	default:
		return false, fmt.Errorf("invalid CORS mode: %s", o.Mode)
	}
	if mode == ModePreserve && len(o.Headers) > 0 {
		return false, errors.New("CORS headers cannot be configured in preserve mode")
	}
	if mode == ModeDisable && o.Headers != nil {
		return false, errors.New("CORS headers cannot be configured in disable mode")
	}
	seen := make(map[string]string, len(o.Headers))
	for configuredName, value := range o.Headers {
		operation, name, ok := configuredHeaderName(configuredName)
		if !ok || !httpguts.ValidHeaderFieldName(name) ||
			!strings.HasPrefix(strings.ToLower(name), corsHeaderPrefix) {
			return false, fmt.Errorf("invalid CORS response header: %s", configuredName)
		}
		if operation != '-' && !httpguts.ValidHeaderFieldValue(value) {
			return false, fmt.Errorf("invalid value for CORS response header: %s", configuredName)
		}
		normalized := strings.ToLower(name)
		if previous, ok := seen[normalized]; ok {
			return false, fmt.Errorf("duplicate CORS response header: %s and %s",
				previous, configuredName)
		}
		seen[normalized] = configuredName
	}
	return true, nil
}

func configuredHeaderName(configuredName string) (byte, string, bool) {
	if configuredName == "" {
		return 0, "", false
	}
	var operation byte
	name := configuredName
	if name[0] == '+' || name[0] == '-' {
		operation = name[0]
		name = name[1:]
	}
	if name == "" || name[0] == '+' || name[0] == '-' {
		return 0, "", false
	}
	return operation, name, true
}

// PreservesOrigin reports whether origin-provided CORS headers remain in the response.
func (o *Options) PreservesOrigin() bool {
	if o == nil || o.legacy {
		return false
	}
	mode := Mode(strings.ToLower(string(o.Mode)))
	return mode == ModePreserve || mode == ModeMerge
}

// IsLegacy reports whether this is the backwards-compatible implicit policy.
func (o *Options) IsLegacy() bool {
	return o != nil && o.legacy
}

// UnmarshalYAML applies defaults before decoding a CORS configuration block.
func (o *Options) UnmarshalYAML(unmarshal func(any) error) error {
	type loadOptions Options
	lo := loadOptions(*(New()))
	if err := unmarshal(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
