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

package options

import (
	"errors"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// Options is a collection of Logging options
type Options struct {
	// LogFile provides the filepath to the instances's logfile. Set as empty string to Log to Console
	LogFile string `yaml:"log_file,omitempty"`
	// LogLevel provides the most granular level (e.g., DEBUG, INFO, ERROR) to log
	LogLevel string `yaml:"log_level,omitempty"`
	// Rotation controls when the log file is rotated
	Rotation *manager.RotationOptions `yaml:"rotation,omitempty"`
	// Retention controls how many rotated log archives are kept
	Retention *manager.RetentionOptions `yaml:"retention,omitempty"`
	// Compress indicates whether rotated archives are gzipped (default true)
	Compress *bool `yaml:"compress,omitempty"`
}

var _ types.ConfigOptions[Options] = &Options{}

var ErrInvalidLogLevel = errors.New("invalid log level")

// New returns a new Options with default values
func New() *Options {
	return &Options{LogLevel: DefaultLogLevel, LogFile: DefaultLogFile}
}

// Clone returns a clone of the Options
func (o *Options) Clone() *Options {
	out := pointers.Clone(o)
	if out == nil {
		return nil
	}
	out.Rotation = o.Rotation.Clone()
	out.Retention = o.Retention.Clone()
	out.Compress = pointers.Clone(o.Compress)
	return out
}

// ManagerOptions returns the rotation manager Options for the log file
func (o *Options) ManagerOptions() *manager.Options {
	return manager.ResolveOptions(o.LogFile, o.Rotation, o.Retention, o.Compress)
}

// RotationEqual returns true if both Options resolve to the same rotation,
// retention and compression behaviors
func (o *Options) RotationEqual(other *Options) bool {
	if o == nil || other == nil {
		return o == other
	}
	a := manager.ResolveOptions("", o.Rotation, o.Retention, o.Compress)
	b := manager.ResolveOptions("", other.Rotation, other.Retention, other.Compress)
	return *a == *b
}

func (o *Options) Initialize(_ string) error {
	if o.LogLevel == "" {
		o.LogLevel = DefaultLogLevel
	}
	return nil
}

func (o *Options) Validate() (bool, error) {
	switch strings.ToLower(o.LogLevel) {
	case "error", "warn", "fatal", "info", "debug":
		return true, nil
	}
	return false, ErrInvalidLogLevel
}
