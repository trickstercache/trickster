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
// autodiscovery provider.
package options

import (
	"errors"

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// ErrInClusterAndKubeconfig is returned when both credential sources are set
var ErrInClusterAndKubeconfig = errors.New(
	"'in_cluster' and 'kubeconfig' are mutually exclusive")

// Options defines the Kubernetes API client settings for a discoverer with
// the 'kubernetes' provider
type Options struct {
	// InCluster, when true, uses the pod's service account for API access.
	// Defaults to true when no kubeconfig is provided.
	InCluster bool `yaml:"in_cluster,omitempty"`
	// Kubeconfig is the path to a kubeconfig file, for use when running
	// outside the target cluster. Mutually exclusive with in_cluster.
	Kubeconfig string `yaml:"kubeconfig,omitempty"`
}

// New returns an Options with default values
func New() *Options { return &Options{InCluster: true} }

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options { return pointers.Clone(o) }

// Initialize applies defaults
func (o *Options) Initialize() {
	if o == nil {
		return
	}
	if !o.InCluster && o.Kubeconfig == "" {
		o.InCluster = true
	}
}

// Validate validates the Options
func (o *Options) Validate() error {
	if o == nil {
		return nil
	}
	if o.InCluster && o.Kubeconfig != "" {
		return ErrInClusterAndKubeconfig
	}
	return nil
}

// NewErrInvalidOptions returns an error for an invalid `kubernetes` options block.
// It lives here rather than in pkg/discovery/options so that the base
// options package carries no per-provider constructors: a new provider
// brings its own error with it.
func NewErrInvalidOptions(name, detail string) error {
	return derrors.NewInvalidOptions("kubernetes", name, detail)
}
