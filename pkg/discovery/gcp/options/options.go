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

// Package options defines the API and credential settings for the gcp
// autodiscovery provider.
package options

import (
	"errors"
	"slices"
	"strings"

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// ComputeReadonlyScope is the OAuth2 scope the provider requests. It is
// read-only by construction: Trickster never mutates a project.
const ComputeReadonlyScope = "https://www.googleapis.com/auth/compute.readonly"

// Google Cloud products the 'gcp' discovery provider can read from.
//
// A value names the product an operator would recognize, not the API that
// happens to serve it, mirroring aws.service: 'ec2' and 'elbv2' are ELB and
// EC2 to an operator whatever the request goes to. That matters here because
// Cloud Load Balancing is served by the Compute API alongside instances --
// naming these for the API would collide, naming them for the product does
// not.
const (
	// ServiceGCE discovers Compute Engine instances via the Compute API's
	// instances.aggregatedList
	ServiceGCE = "gce"
)

var (
	// ErrMissingService is returned when gcp.service is not set. There is
	// deliberately no default even though only one service exists today:
	// a default added now could never be removed, and every later service
	// would be reached by an operator opting out of a value they never
	// chose. Requiring it while the field is new costs one line of config
	// and keeps the 'gcp' and 'aws' blocks shaped identically.
	ErrMissingService = errors.New(
		"'service' is required and must be one of " +
			strings.Join(SupportedServices(), ", "))
	// ErrInvalidService is returned when gcp.service names an unsupported product
	ErrInvalidService = errors.New(
		"'service' must be one of " +
			strings.Join(SupportedServices(), ", "))
)

// SupportedServices returns the gcp.service values this build supports.
func SupportedServices() []string { return []string{ServiceGCE} }

// ErrMissingProject is returned when no project is configured and none can
// be inferred.
var ErrMissingProject = errors.New(
	"'project' is required unless Trickster runs on a GCE instance, " +
		"where it is read from the metadata server")

// Options defines the API and credential settings for a discoverer with
// the 'gcp' provider.
//
// 'service' selects which Google Cloud product to read, mirroring the aws
// provider's field of the same name. Unlike aws, it names only the source:
// the endpoint and authorization do not vary with it, so it carries no
// second meaning.
//
// Leaving CredentialsFile empty selects Application Default Credentials:
// the GOOGLE_APPLICATION_CREDENTIALS environment variable, gcloud's
// user credentials, Workload Identity on GKE, or the instance metadata
// server on GCE. Prefer that over a key file wherever the platform offers
// it -- on GKE use Workload Identity, on GCE the instance service account.
type Options struct {
	// Service names the Google Cloud product to discover from; see
	// SupportedServices
	Service string `yaml:"service,omitempty"`
	// Project is the GCP project to list resources in. When empty and
	// Trickster is running on a GCE instance, it is read from the metadata
	// server.
	Project string `yaml:"project,omitempty"`
	// CredentialsFile is the path to a **service account** JSON key. When
	// empty, Application Default Credentials are used.
	//
	// The credential type is required to be service_account rather than
	// taken from the file: an external_account or
	// impersonated_service_account configuration can name an arbitrary
	// token URL or local executable, so accepting whichever type a file
	// happens to declare would hand credential resolution somewhere
	// unintended. For user credentials, use Application Default
	// Credentials instead of this field.
	CredentialsFile string `yaml:"credentials_file,omitempty"`
}

// New returns an Options with default values. Service has no default and
// must be supplied by the operator.
func New() *Options { return &Options{} }

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options { return pointers.Clone(o) }

// GetService returns the configured Google Cloud API. It is empty when
// unset, which Validate rejects; there is no default.
func (o *Options) GetService() string {
	if o == nil {
		return ""
	}
	return o.Service
}

// Validate validates the Options.
//
// Project is deliberately not required here: on a GCE instance it is read
// from the metadata server, which is the idiomatic deployment and would be
// broken by demanding it in config. An unresolvable project surfaces as
// ErrMissingProject when the provider first builds its request URL.
func (o *Options) Validate() error {
	if o == nil {
		return errors.New("the 'gcp' block is required")
	}
	if o.Service == "" {
		return ErrMissingService
	}
	if !slices.Contains(SupportedServices(), o.Service) {
		return ErrInvalidService
	}
	return nil
}

// NewErrInvalidOptions returns an error for an invalid `gcp` options block.
// It lives here rather than in pkg/discovery/options so that the base
// options package carries no per-provider constructors: a new provider
// brings its own error with it.
func NewErrInvalidOptions(name, detail string) error {
	return derrors.NewInvalidOptions("gcp", name, detail)
}
