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

// Package options defines the connection settings for the docker
// autodiscovery provider.
package options

import (
	"errors"
	"regexp"

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// DefaultHost is the Engine API endpoint used when the shared http block
// names none. It is the well-known socket path, in the same URL form the
// DOCKER_HOST environment variable takes, so an operator can paste one
// into the other.
const DefaultHost = "unix:///var/run/docker.sock"

// DefaultAPIVersion is the Engine API version this provider requests.
//
// It is pinned deliberately low rather than left off. An unversioned
// request binds to whatever the daemon's newest version happens to be, so
// the response shape would change under Trickster when the host upgrades
// Docker; a version the daemon is too old to serve is refused outright.
// v1.41 ships with Docker 20.10 (2020), covers every field this provider
// reads, and is accepted by every daemon since.
const DefaultAPIVersion = "v1.41"

// apiVersionRE matches the Engine API's version path segment.
var apiVersionRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+$`)

// ErrInvalidAPIVersion is returned when api_version is not a 'v1.41'-style
// version segment
var ErrInvalidAPIVersion = errors.New(
	"'api_version' must look like 'v1.41'")

// Options defines the connection settings for a discoverer with the
// 'docker' provider.
//
// The endpoint itself lives in the shared 'http' block, as it does for
// every polling provider; leaving it unset selects DefaultHost. Both
// unix:// and tcp:// (or http(s)://) forms are accepted, and a tcp
// endpoint takes its client TLS from the same shared block, which is how
// a remote daemon's mutual TLS is configured.
type Options struct {
	// APIVersion pins the Engine API version segment; see
	// DefaultAPIVersion for why this is pinned rather than omitted
	APIVersion string `yaml:"api_version,omitempty"`
}

// New returns an Options with default values
func New() *Options { return &Options{} }

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options { return pointers.Clone(o) }

// GetAPIVersion returns the configured API version, or the default
func (o *Options) GetAPIVersion() string {
	if o == nil || o.APIVersion == "" {
		return DefaultAPIVersion
	}
	return o.APIVersion
}

// Validate validates the Options
func (o *Options) Validate() error {
	if o == nil {
		return errors.New("the 'docker' block is required")
	}
	if o.APIVersion != "" && !apiVersionRE.MatchString(o.APIVersion) {
		return ErrInvalidAPIVersion
	}
	return nil
}

// NewErrInvalidOptions returns an error for an invalid `docker` options
// block. It lives here rather than in pkg/discovery/options so that the
// base options package carries no per-provider constructors: a new
// provider brings its own error with it.
func NewErrInvalidOptions(name, detail string) error {
	return derrors.NewInvalidOptions("docker", name, detail)
}
