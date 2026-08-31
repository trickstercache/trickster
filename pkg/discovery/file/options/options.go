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

// Package options defines the change-detection settings for the file
// autodiscovery provider.
package options

import (
	"errors"
	"time"

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

const (
	// DefaultPollInterval is the default member-file stat-poll cadence
	DefaultPollInterval = 30 * time.Second
	// MinimumPollInterval is the lowest permitted stat-poll cadence
	MinimumPollInterval = time.Second
)

// ErrPollIntervalTooLow is returned when the poll cadence is below the minimum
var ErrPollIntervalTooLow = errors.New("'poll_interval' must be at least 1s")

// Options defines the change-detection settings for a discoverer with the
// 'file' provider. The provider always watches the member-list file's parent
// directory for filesystem change notifications AND stat-polls the file as a
// fallback; poll_interval controls that fallback cadence. On filesystems
// where change notification is unreliable or unavailable -- NFS-backed
// volumes, some FUSE/CSI mounts -- the poll is the effective update
// mechanism, so lower it to the freshness the deployment needs.
type Options struct {
	// PollInterval is the cadence of the stat-based change poll that
	// backstops filesystem change notification
	PollInterval timeconv.Duration `yaml:"poll_interval,omitempty"`
}

// New returns an Options with default values
func New() *Options {
	return &Options{PollInterval: timeconv.Duration(DefaultPollInterval)}
}

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options { return pointers.Clone(o) }

// Initialize applies defaults
func (o *Options) Initialize() {
	if o == nil {
		return
	}
	if o.PollInterval == 0 {
		o.PollInterval = timeconv.Duration(DefaultPollInterval)
	}
}

// Validate validates the Options
func (o *Options) Validate() error {
	if o == nil {
		return nil
	}
	if o.PollInterval != 0 && time.Duration(o.PollInterval) < MinimumPollInterval {
		return ErrPollIntervalTooLow
	}
	return nil
}

// NewErrInvalidOptions returns an error for an invalid `file` options block.
// It lives here rather than in pkg/discovery/options so that the base
// options package carries no per-provider constructors: a new provider
// brings its own error with it.
func NewErrInvalidOptions(name, detail string) error {
	return derrors.NewInvalidOptions("file", name, detail)
}
