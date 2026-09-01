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

// Package options defines the API and credential settings for the azure
// autodiscovery provider.
package options

import (
	"errors"
	"slices"
	"strings"

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	"github.com/trickstercache/trickster/v2/pkg/secret"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// Azure services the 'azure' discovery provider can read from
const (
	// ServiceVM discovers Virtual Machines, joining them to their network
	// interfaces to resolve addresses
	ServiceVM = "vm"
)

// Azure clouds. The management and login endpoints both differ per cloud,
// so one setting selects both rather than making an operator keep two
// URLs consistent.
const (
	// CloudPublic is the global Azure cloud (the default)
	CloudPublic = "public"
	// CloudUSGovernment is Azure Government
	CloudUSGovernment = "usgovernment"
	// CloudChina is Azure operated by 21Vianet
	CloudChina = "china"
)

// Endpoints per cloud: the ARM management endpoint and the AAD login
// endpoint.
var cloudEndpoints = map[string][2]string{
	CloudPublic: {
		"https://management.azure.com",
		"https://login.microsoftonline.com",
	},
	CloudUSGovernment: {
		"https://management.usgovcloudapi.net",
		"https://login.microsoftonline.us",
	},
	CloudChina: {
		"https://management.chinacloudapi.cn",
		"https://login.chinacloudapi.cn",
	},
}

// DefaultComputeAPIVersion is the Microsoft.Compute API version used for
// the virtualMachines list. Azure requires an explicit api-version on
// every request; pinning it means the response shape cannot change under
// Trickster when Microsoft ships a new one.
const DefaultComputeAPIVersion = "2024-07-01"

// DefaultNetworkAPIVersion is the Microsoft.Network API version used for
// the networkInterfaces and publicIPAddresses lists
const DefaultNetworkAPIVersion = "2024-05-01"

// SupportedServices returns the azure.service values this build supports.
func SupportedServices() []string { return []string{ServiceVM} }

// SupportedClouds returns the azure.cloud values this build supports.
func SupportedClouds() []string {
	return []string{CloudPublic, CloudUSGovernment, CloudChina}
}

var (
	// ErrMissingService is returned when azure.service is not set. There is
	// deliberately no default even though only one service exists today: a
	// default added now could never be removed, and every later service
	// would be reached by opting out of a value never chosen. This matches
	// aws.service and gcp.service.
	ErrMissingService = errors.New(
		"'service' is required and must be one of " +
			strings.Join(SupportedServices(), ", "))
	// ErrInvalidService is returned when azure.service names an unsupported
	// API
	ErrInvalidService = errors.New(
		"'service' must be one of " + strings.Join(SupportedServices(), ", "))
	// ErrMissingSubscription is returned when no subscription is configured
	ErrMissingSubscription = errors.New("'subscription_id' is required")
	// ErrInvalidCloud is returned when azure.cloud names an unknown cloud
	ErrInvalidCloud = errors.New(
		"'cloud' must be one of " + strings.Join(SupportedClouds(), ", "))
	// ErrIncompleteClientCredentials is returned when a client credential
	// is half-specified
	ErrIncompleteClientCredentials = errors.New(
		"'client_id' and 'tenant_id' are both required when using " +
			"'client_secret' or 'federated_token_file'")
	// ErrConflictingCredentials is returned when more than one explicit
	// credential is supplied
	ErrConflictingCredentials = errors.New(
		"set at most one of 'client_secret' and 'federated_token_file'")
)

// Options defines the API and credential settings for a discoverer with
// the 'azure' provider.
//
// Leaving the credential fields empty selects the instance metadata
// service, which is how a managed identity authenticates on an Azure VM
// or in AKS. Prefer that over a client secret wherever the platform
// offers it; on AKS with workload identity, set FederatedTokenFile
// instead, which is a token the platform rotates rather than a secret
// Trickster holds.
type Options struct {
	// Service names the Azure API to discover from; see SupportedServices
	Service string `yaml:"service,omitempty"`
	// SubscriptionID is the subscription to enumerate
	SubscriptionID string `yaml:"subscription_id,omitempty"`
	// TenantID is the Entra ID tenant; required with a client credential
	TenantID string `yaml:"tenant_id,omitempty"`
	// ClientID identifies the service principal, or selects a
	// user-assigned managed identity when no other credential is set
	ClientID string `yaml:"client_id,omitempty"`
	// ClientSecret authenticates a service principal
	ClientSecret secret.Secret `yaml:"client_secret,omitempty"`
	// FederatedTokenFile is a projected token used to authenticate without
	// a stored secret; this is AKS workload identity, where the platform
	// writes and rotates the file
	FederatedTokenFile string `yaml:"federated_token_file,omitempty"`
	// Cloud selects the Azure cloud, which sets both the management and
	// login endpoints; see SupportedClouds
	Cloud string `yaml:"cloud,omitempty"`
	// ResourceGroup narrows enumeration to one resource group. When empty
	// the whole subscription is listed.
	ResourceGroup string `yaml:"resource_group,omitempty"`
	// PowerState requests VM power state, which costs one extra list call
	// per refresh; see the provider documentation for the tradeoff
	PowerState bool `yaml:"power_state,omitempty"`
	// ComputeAPIVersion pins the Microsoft.Compute api-version
	ComputeAPIVersion string `yaml:"compute_api_version,omitempty"`
	// NetworkAPIVersion pins the Microsoft.Network api-version
	NetworkAPIVersion string `yaml:"network_api_version,omitempty"`
}

// New returns an Options with default values. Service has no default and
// must be supplied by the operator.
func New() *Options { return &Options{} }

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options { return pointers.Clone(o) }

// GetService returns the configured Azure API. It is empty when unset,
// which Validate rejects; there is no default.
func (o *Options) GetService() string {
	if o == nil {
		return ""
	}
	return o.Service
}

// GetCloud returns the configured cloud, or the default
func (o *Options) GetCloud() string {
	if o == nil || o.Cloud == "" {
		return CloudPublic
	}
	return o.Cloud
}

// ManagementEndpoint returns the ARM endpoint for the configured cloud
func (o *Options) ManagementEndpoint() string {
	return cloudEndpoints[o.GetCloud()][0]
}

// LoginEndpoint returns the Entra ID endpoint for the configured cloud
func (o *Options) LoginEndpoint() string {
	return cloudEndpoints[o.GetCloud()][1]
}

// GetComputeAPIVersion returns the Microsoft.Compute api-version, or the
// default
func (o *Options) GetComputeAPIVersion() string {
	if o == nil || o.ComputeAPIVersion == "" {
		return DefaultComputeAPIVersion
	}
	return o.ComputeAPIVersion
}

// GetNetworkAPIVersion returns the Microsoft.Network api-version, or the
// default
func (o *Options) GetNetworkAPIVersion() string {
	if o == nil || o.NetworkAPIVersion == "" {
		return DefaultNetworkAPIVersion
	}
	return o.NetworkAPIVersion
}

// Validate validates the Options
func (o *Options) Validate() error {
	if o == nil {
		return errors.New("the 'azure' block is required")
	}
	if o.Service == "" {
		return ErrMissingService
	}
	if !slices.Contains(SupportedServices(), o.Service) {
		return ErrInvalidService
	}
	if o.SubscriptionID == "" {
		return ErrMissingSubscription
	}
	if _, ok := cloudEndpoints[o.GetCloud()]; !ok {
		return ErrInvalidCloud
	}
	if o.ClientSecret != "" && o.FederatedTokenFile != "" {
		return ErrConflictingCredentials
	}
	// a half-specified client credential authenticates as nobody; it is
	// far more likely a mistake than an intent to fall back to the
	// instance metadata service
	if (o.ClientSecret != "" || o.FederatedTokenFile != "") &&
		(o.ClientID == "" || o.TenantID == "") {
		return ErrIncompleteClientCredentials
	}
	return nil
}

// NewErrInvalidOptions returns an error for an invalid `azure` options
// block. It lives here rather than in pkg/discovery/options so that the
// base options package carries no per-provider constructors: a new
// provider brings its own error with it.
func NewErrInvalidOptions(name, detail string) error {
	return derrors.NewInvalidOptions("azure", name, detail)
}
