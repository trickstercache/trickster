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

// Package options provides the per-backend access_log configuration
package options

import (
	"errors"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/format"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// DefaultErrorThreshold is the minimum response status code written to the
// error log when error_threshold is not configured
const DefaultErrorThreshold = http.StatusBadRequest

var ErrInvalidErrorThreshold = errors.New(
	"error_threshold must be a valid http status code (100-599)")

// Options configures per-backend access and error logging. Error settings
// inherit their access-log counterparts when unset.
type Options struct {
	// Filename is the path to the access log; empty disables access logging
	Filename string `yaml:"filename,omitempty"`
	// Format is a named preset (common, combined, extended, json) or a
	// custom %-token format string; default is combined
	Format string `yaml:"format,omitempty"`
	// Rotation controls when the access log is rotated
	Rotation *manager.RotationOptions `yaml:"rotation,omitempty"`
	// Retention controls how many rotated access log archives are kept
	Retention *manager.RetentionOptions `yaml:"retention,omitempty"`
	// Compress indicates whether rotated archives are gzipped (default true)
	Compress *bool `yaml:"compress,omitempty"`
	// ErrorFilename is the path to the error log; empty disables error logging
	ErrorFilename string `yaml:"error_filename,omitempty"`
	// ErrorFormat overrides Format for the error log
	ErrorFormat string `yaml:"error_format,omitempty"`
	// ErrorThreshold is the minimum response status code written to the
	// error log (default 400)
	ErrorThreshold int `yaml:"error_threshold,omitempty"`
	// ErrorRotation overrides Rotation for the error log
	ErrorRotation *manager.RotationOptions `yaml:"error_rotation,omitempty"`
	// ErrorRetention overrides Retention for the error log
	ErrorRetention *manager.RetentionOptions `yaml:"error_retention,omitempty"`
	// ErrorCompress overrides Compress for the error log
	ErrorCompress *bool `yaml:"error_compress,omitempty"`
}

// New returns a new Options with default values
func New() *Options {
	return &Options{}
}

// Clone returns a copy of the Options
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	out.Rotation = o.Rotation.Clone()
	out.Retention = o.Retention.Clone()
	out.Compress = pointers.Clone(o.Compress)
	out.ErrorRotation = o.ErrorRotation.Clone()
	out.ErrorRetention = o.ErrorRetention.Clone()
	out.ErrorCompress = pointers.Clone(o.ErrorCompress)
	return &out
}

func (o *Options) Initialize(_ string) error {
	return nil
}

// IsEnabled returns true if access or error logging is configured
func (o *Options) IsEnabled() bool {
	return o != nil && (o.Filename != "" || o.ErrorFilename != "")
}

// Validate validates the Options, including compiling the format strings
func (o *Options) Validate() (bool, error) {
	if _, err := format.ParseFormat(o.ResolvedFormat()); err != nil {
		return false, err
	}
	if _, err := format.ParseFormat(o.ResolvedErrorFormat()); err != nil {
		return false, err
	}
	if o.ErrorThreshold != 0 &&
		(o.ErrorThreshold < 100 || o.ErrorThreshold > 599) {
		return false, ErrInvalidErrorThreshold
	}
	return true, nil
}

// ResolvedFormat returns the access log format, defaulted when unset
func (o *Options) ResolvedFormat() string {
	if o.Format == "" {
		return format.DefaultFormatName
	}
	return o.Format
}

// ResolvedErrorFormat returns the error log format, inheriting the access
// log format when unset
func (o *Options) ResolvedErrorFormat() string {
	if o.ErrorFormat == "" {
		return o.ResolvedFormat()
	}
	return o.ErrorFormat
}

// ResolvedErrorThreshold returns the error log status threshold,
// defaulted when unset
func (o *Options) ResolvedErrorThreshold() int {
	if o.ErrorThreshold == 0 {
		return DefaultErrorThreshold
	}
	return o.ErrorThreshold
}

// AccessManagerOptions returns the rotation manager Options for the access
// log file
func (o *Options) AccessManagerOptions() *manager.Options {
	return manager.ResolveOptions(o.Filename, o.Rotation, o.Retention, o.Compress)
}

// ErrorManagerOptions returns the rotation manager Options for the error
// log file, inheriting access log settings for unset error_* fields
func (o *Options) ErrorManagerOptions() *manager.Options {
	rot := o.ErrorRotation
	if rot == nil {
		rot = o.Rotation
	}
	ret := o.ErrorRetention
	if ret == nil {
		ret = o.Retention
	}
	compress := o.ErrorCompress
	if compress == nil {
		compress = o.Compress
	}
	return manager.ResolveOptions(o.ErrorFilename, rot, ret, compress)
}
