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
)

var supported = sets.New([]string{Kubernetes, DNSSRV, DNSA, File})

// IsValidProvider returns true if the provided Provider name is a supported
// autodiscovery provider
func IsValidProvider(name string) bool {
	return supported.Contains(name)
}

// SupportedProviders returns the set of supported autodiscovery provider names
func SupportedProviders() sets.Set[string] {
	return supported.Clone()
}
