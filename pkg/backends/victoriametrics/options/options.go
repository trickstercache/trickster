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

// Package options defines the options of VictoriaMetrics backends.
package options

import (
	"errors"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// ErrInvalidGraphitePath is returned for a graphite_path that isn't an absolute URL path.
var ErrInvalidGraphitePath = errors.New("victoriametrics graphite_path must be an absolute path " +
	"without a query or fragment")

// Options holds the settings specific to VictoriaMetrics backends.
type Options struct {
	// GraphitePath is the upstream path of the Graphite APIs. When empty, it is the origin URL's
	// path, with a trailing /prometheus replaced by /graphite as vmselect's tenant paths require.
	GraphitePath string `yaml:"graphite_path,omitempty"`
	// SearchDisableCache mirrors the origin's -search.disableCache flag, under which VictoriaMetrics
	// keeps the requested grid of range queries with 50 or more points instead of aligning it.
	SearchDisableCache bool `yaml:"search_disable_cache,omitempty"`
}

// New returns a new VictoriaMetrics Options with default values.
func New() *Options {
	return &Options{}
}

// Clone returns a copy of the Options.
func (o *Options) Clone() *Options {
	return pointers.Clone(o)
}

// Validate checks the Options.
func (o *Options) Validate() error {
	if o == nil || o.GraphitePath == "" {
		return nil
	}
	if !strings.HasPrefix(o.GraphitePath, "/") || strings.ContainsAny(o.GraphitePath, "?#") {
		return ErrInvalidGraphitePath
	}
	return nil
}
