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

// Package options defines the options of the geofeed geo locator provider.
package options

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/feed"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/providers"
)

// Options configures a geofeed locator
type Options struct {
	// Entries are RFC 8805 lines; an entry beats a file's for the same prefix
	Entries []string `yaml:"entries,omitempty"`
	// Files are RFC 8805 geofeed files, watched for changes
	Files []string `yaml:"files,omitempty"`
	// ReloadInterval is how often the files are checked, besides change events
	ReloadInterval timeconv.Duration `yaml:"reload_interval,omitempty"`
}

// ErrNoEntries is returned when a geofeed locator has neither entries nor files
var ErrNoEntries = errors.New("at least one of 'entries' and 'files' is required")

// New returns new geofeed Options
func New() *Options {
	return &Options{}
}

// Clone returns a copy of the Options
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	return &Options{
		Entries:        slices.Clone(o.Entries),
		Files:          slices.Clone(o.Files),
		ReloadInterval: o.ReloadInterval,
	}
}

// Initialize sets the default reload interval
func (o *Options) Initialize() {
	if o.ReloadInterval == 0 {
		o.ReloadInterval = timeconv.Duration(providers.DefaultReloadInterval)
	}
}

// Validate checks the Options without loading any file
func (o *Options) Validate() error {
	if len(o.Entries) == 0 && len(o.Files) == 0 {
		return ErrNoEntries
	}
	for i, line := range o.Entries {
		if _, ok, err := feed.ParseLine(line); err != nil {
			return fmt.Errorf("'entries' item %d: %w", i, err)
		} else if !ok {
			return fmt.Errorf("'entries' item %d: %w %q: an entry may not be blank or a comment",
				i, feed.ErrInvalidLine, line)
		}
	}
	for _, path := range o.Files {
		if err := providers.CheckReadable(path); err != nil {
			return fmt.Errorf("'files': %w", err)
		}
	}
	return providers.ValidateReloadInterval(time.Duration(o.ReloadInterval))
}
