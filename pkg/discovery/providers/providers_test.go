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

package providers

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// all is every exported provider name. A provider added to the package
// without being added here fails TestEveryProviderNameIsSupported, which is
// the point: the sets below are hand-maintained, and this is what keeps a
// new provider from being silently absent from one of them.
var all = []string{
	Kubernetes, DNSSRV, DNSA, File, HTTPSD, Consul, Nomad, AWS, GCP,
	Docker, Azure,
}

func TestEveryProviderNameIsSupported(t *testing.T) {
	require.Len(t, all, len(supported),
		"a provider constant was added without adding it to `supported`, "+
			"or vice versa")
	for _, p := range all {
		require.True(t, IsValidProvider(p), "%s is not in `supported`", p)
	}
}

func TestProviderNamesAreDistinctAndNonEmpty(t *testing.T) {
	seen := make(map[string]bool, len(all))
	for _, p := range all {
		require.NotEmpty(t, p)
		require.False(t, seen[p], "duplicate provider name %q", p)
		seen[p] = true
	}
}

func TestIsValidProviderRejectsUnknownNames(t *testing.T) {
	for _, name := range []string{
		"", "not_a_real_provider", "KUBERNETES", "dns", "ec2", "gce", "vm",
	} {
		require.False(t, IsValidProvider(name),
			"%q must not be accepted as a provider", name)
	}
}

// 'dns' is not a provider: the two DNS providers are named for the record
// type they read, because a query written for one is meaningless to the
// other.
func TestDNSProvidersAreSeparate(t *testing.T) {
	require.NotEqual(t, DNSSRV, DNSA)
	require.True(t, IsValidProvider(DNSSRV))
	require.True(t, IsValidProvider(DNSA))
	require.False(t, IsValidProvider("dns"))
}

// A provider that polls HTTP but is missing from httpProviders has its
// 'http' config block rejected at startup, so the set has to stay a subset
// of the supported ones.
func TestHTTPProvidersAreAllSupported(t *testing.T) {
	for p := range httpProviders {
		require.True(t, IsValidProvider(p),
			"%s is an http provider but not a supported provider", p)
	}
}

// Deriving an endpoint only means anything for a provider that has one, so
// every endpoint-deriving provider must also be an HTTP provider.
func TestEndpointDerivingProvidersAreHTTPProviders(t *testing.T) {
	for p := range endpointDerivingProviders {
		require.True(t, IsHTTPProvider(p),
			"%s derives an endpoint but is not an http provider", p)
	}
}

func TestIsHTTPProvider(t *testing.T) {
	for _, p := range []string{HTTPSD, Consul, Nomad, AWS, GCP, Docker, Azure} {
		require.True(t, IsHTTPProvider(p), "%s should be an http provider", p)
	}
	// these discover without polling an HTTP endpoint, so the shared http
	// block is not theirs to accept
	for _, p := range []string{Kubernetes, DNSSRV, DNSA, File} {
		require.False(t, IsHTTPProvider(p), "%s should not accept an http block", p)
	}
	require.False(t, IsHTTPProvider("not_a_real_provider"))
}

func TestDerivesEndpoint(t *testing.T) {
	// the clouds and docker compute their endpoint from configuration
	// (region/service, cloud, the well-known socket), so http.endpoint is an
	// optional override for them
	for _, p := range []string{AWS, GCP, Azure, Docker} {
		require.True(t, DerivesEndpoint(p), "%s should derive its endpoint", p)
	}
	// these must be told where to poll
	for _, p := range []string{HTTPSD, Consul, Nomad} {
		require.False(t, DerivesEndpoint(p), "%s requires an endpoint", p)
	}
	require.False(t, DerivesEndpoint("not_a_real_provider"))
}

// SupportedProviders hands out a copy; a caller mutating it must not be
// able to add or remove a provider for everyone else.
func TestSupportedProvidersReturnsACopy(t *testing.T) {
	got := SupportedProviders()
	require.Len(t, got, len(supported))

	got.Set("not_a_real_provider")
	require.False(t, IsValidProvider("not_a_real_provider"),
		"mutating the returned set must not affect the package's own")

	got.Remove(Kubernetes)
	require.True(t, IsValidProvider(Kubernetes),
		"removing from the returned set must not affect the package's own")
}
