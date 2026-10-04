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

// Package providers enumerates the supported geo locator providers.
package providers

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	// MMDB locates addresses in a MaxMind DB format file
	MMDB = "mmdb"
	// Geofeed locates addresses by RFC 8805 geofeed entries, inline or in files
	Geofeed = "geofeed"
	// Header locates HTTP requests by a location header that a trusted upstream set
	Header = "header"
)

var supported = []string{Geofeed, Header, MMDB}

// IsValidProvider reports whether name is a supported geo locator provider
func IsValidProvider(name string) bool {
	return slices.Contains(supported, name)
}

// Names returns the supported provider names, separated by commas, for messages
func Names() string {
	return strings.Join(supported, ", ")
}

// ReadsAddresses reports whether the provider places a bare client address, which native protocol
// and stream listeners require, rather than only an HTTP request
func ReadsAddresses(name string) bool {
	return name != Header
}

// the bounds of reload_interval, which the file-based providers share
const (
	DefaultReloadInterval = 5 * time.Minute
	MinReloadInterval     = 10 * time.Second
	MaxReloadInterval     = 24 * time.Hour
)

// ErrInvalidReloadInterval is returned for a reload_interval outside its bounds
var ErrInvalidReloadInterval = fmt.Errorf("'reload_interval' must be from %s to %s", MinReloadInterval, MaxReloadInterval)

// ValidateReloadInterval checks a configured reload_interval; zero takes the default
func ValidateReloadInterval(d time.Duration) error {
	if d != 0 && (d < MinReloadInterval || d > MaxReloadInterval) {
		return ErrInvalidReloadInterval
	}
	return nil
}

// CheckReadable returns an error when the file at path cannot be opened for reading. It reads
// nothing, so a configuration is validated without loading what it names.
func CheckReadable(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}
