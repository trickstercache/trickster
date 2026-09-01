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

// Package providers enumerates the supported autodiscovery providers
package providers

import (
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

const (
	// Kubernetes discovers members via the Kubernetes API
	// (endpointslices, services, or pods)
	Kubernetes = "kubernetes"
	// DNSSRV discovers members by polling DNS SRV records
	DNSSRV = "dns_srv"
	// DNSA discovers members by polling DNS A/AAAA records
	DNSA = "dns_a"
	// File discovers members from a watched local member-list file
	File = "file"
	// HTTPSD discovers members from a member list served over HTTP
	HTTPSD = "http_sd"
	// Consul discovers members from the Consul service catalog
	Consul = "consul"
	// Nomad discovers members from Nomad's native service registry
	Nomad = "nomad"
	// AWS discovers members from an AWS API, selected by aws.service
	AWS = "aws"
	// GCP discovers members from a Google Cloud API, selected by gcp.service
	GCP = "gcp"
	// Docker discovers members from the Docker Engine API
	Docker = "docker"
	// Azure discovers members from an Azure Resource Manager API,
	// selected by azure.service
	Azure = "azure"
)

var supported = sets.New([]string{
	Kubernetes, DNSSRV, DNSA, File, HTTPSD, Consul, Nomad, AWS, GCP,
	Docker, Azure,
})

// httpProviders are the providers that discover members by polling an HTTP
// endpoint, and so accept the shared 'http' options block. It is empty
// until the first such provider lands (http_sd), at which point that
// provider's name is added here alongside its entry in supported. A
// provider that polls HTTP but forgets to register here will have its
// 'http' config block rejected at startup.
var httpProviders = sets.New([]string{HTTPSD, Consul, Nomad, AWS, GCP, Docker, Azure})

// endpointDerivingProviders compute their endpoint rather than being told
// it: AWS builds one from the region and service. For these, the shared
// http block's 'endpoint' is an optional override (a VPC endpoint, a FIPS
// endpoint, a test server) rather than a required setting.
var endpointDerivingProviders = sets.New([]string{AWS, GCP, Docker, Azure})

// DerivesEndpoint returns true if the named provider computes its own
// endpoint, making the shared http block's 'endpoint' optional
func DerivesEndpoint(name string) bool {
	return endpointDerivingProviders.Contains(name)
}

// IsHTTPProvider returns true if the named provider polls an HTTP endpoint
// and therefore accepts the shared 'http' discoverer options block
func IsHTTPProvider(name string) bool {
	return httpProviders.Contains(name)
}

// IsValidProvider returns true if the provided Provider name is a supported
// autodiscovery provider
func IsValidProvider(name string) bool {
	return supported.Contains(name)
}

// SupportedProviders returns the set of supported autodiscovery provider names
func SupportedProviders() sets.Set[string] {
	return supported.Clone()
}
