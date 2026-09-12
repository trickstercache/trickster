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

// Package options defines the connection settings for the kubernetes
// autodiscovery provider. The settings themselves are the shared
// Kubernetes connection options, aliased here so that a discoverer's
// 'kubernetes' block and the controller's connection block are one
// vocabulary; this package adds only the discovery-specific error wrapper.
package options

import (
	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	kubeopts "github.com/trickstercache/trickster/v2/pkg/kube/options"
)

// Options is the Kubernetes API client settings for a discoverer with the
// 'kubernetes' provider
type Options = kubeopts.Options

// ErrInClusterAndKubeconfig is returned when both credential sources are set
var ErrInClusterAndKubeconfig = kubeopts.ErrInClusterAndKubeconfig

// New returns an Options with default values
func New() *Options { return kubeopts.New() }

// NewErrInvalidOptions returns an error for an invalid `kubernetes` options block.
// It lives here rather than in pkg/discovery/options so that the base
// options package carries no per-provider constructors: a new provider
// brings its own error with it.
func NewErrInvalidOptions(name, detail string) error {
	return derrors.NewInvalidOptions("kubernetes", name, detail)
}
