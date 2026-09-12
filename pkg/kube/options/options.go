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

// Package options defines the Kubernetes API connection settings shared by
// every Trickster subsystem that talks to a cluster. The autodiscovery
// kubernetes provider aliases this type, so the same vocabulary applies
// wherever a connection is configured.
package options

import (
	"errors"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// ErrInClusterAndKubeconfig is returned when both credential sources are set
var ErrInClusterAndKubeconfig = errors.New(
	"'in_cluster' and 'kubeconfig' are mutually exclusive")

// ErrNegativeQPS is returned when 'qps' is below zero
var ErrNegativeQPS = errors.New("'qps' must not be negative")

// ErrNegativeBurst is returned when 'burst' is below zero
var ErrNegativeBurst = errors.New("'burst' must not be negative")

// ErrNegativeTimeout is returned when 'timeout' is below zero
var ErrNegativeTimeout = errors.New("'timeout' must not be negative")

const (
	// DefaultQPS is the default client-side rate limit for API requests; client-go's own default
	// of 5 is sized for a CLI and throttles a process that watches many objects
	DefaultQPS float32 = 20
	// DefaultBurst is the default client-side request burst allowance
	DefaultBurst = 40
	// DefaultTimeout is the default bound on a one-shot API call
	DefaultTimeout = 10 * time.Second
)

// Options defines the Kubernetes API client settings for one connection
type Options struct {
	// InCluster, when true, uses the pod's service account for API access.
	// Defaults to true when no kubeconfig is provided.
	InCluster bool `yaml:"in_cluster,omitempty"`
	// Kubeconfig is the path to a kubeconfig file, for use when running
	// outside the target cluster. Mutually exclusive with in_cluster.
	Kubeconfig string `yaml:"kubeconfig,omitempty"`
	// QPS is the client-side sustained request rate limit; 0 takes the
	// default and a negative value is invalid
	QPS float32 `yaml:"qps,omitempty"`
	// Burst is the client-side request burst allowance
	Burst int `yaml:"burst,omitempty"`
	// UserAgent overrides the User-Agent sent to the API server; empty
	// yields 'trickster/<version> (<os>/<arch>)'
	UserAgent string `yaml:"user_agent,omitempty"`
	// Timeout bounds a one-shot API call such as the connectivity preflight; it is not applied to
	// the REST client, whose timeout would also truncate long-running informer watch streams
	Timeout timeconv.Duration `yaml:"timeout,omitempty"`
}

// New returns an Options with default values
func New() *Options {
	return &Options{
		InCluster: true,
		QPS:       DefaultQPS,
		Burst:     DefaultBurst,
		Timeout:   timeconv.Duration(DefaultTimeout),
	}
}

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
	if o.QPS == 0 {
		o.QPS = DefaultQPS
	}
	if o.Burst == 0 {
		o.Burst = DefaultBurst
	}
	if o.Timeout == 0 {
		o.Timeout = timeconv.Duration(DefaultTimeout)
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
	if o.QPS < 0 {
		return ErrNegativeQPS
	}
	if o.Burst < 0 {
		return ErrNegativeBurst
	}
	if o.Timeout < 0 {
		return ErrNegativeTimeout
	}
	return nil
}
