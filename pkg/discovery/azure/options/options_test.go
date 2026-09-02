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

package options

import (
	"errors"
	"strings"
	"testing"

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func valid() *Options {
	return &Options{Service: ServiceVM, SubscriptionID: "sub-1"}
}

func TestSupportedServicesAndClouds(t *testing.T) {
	require.Equal(t, []string{ServiceVM}, SupportedServices())
	require.Equal(t,
		[]string{CloudPublic, CloudUSGovernment, CloudChina}, SupportedClouds())
}

// Required despite having one legal value, matching aws.service and
// gcp.service: a default could never be taken back once configs rely on it.
func TestValidateRequiresService(t *testing.T) {
	require.Empty(t, New().Service, "there is no default service")

	err := (&Options{SubscriptionID: "sub-1"}).Validate()
	require.ErrorIs(t, err, ErrMissingService)
	require.Contains(t, err.Error(), ServiceVM)

	for _, bad := range []string{"virtualmachines", "compute", "VM", "vmss"} {
		require.ErrorIs(t,
			(&Options{Service: bad, SubscriptionID: "sub-1"}).Validate(),
			ErrInvalidService, "%q must not be accepted", bad)
	}
	require.EqualError(t, (*Options)(nil).Validate(), "the 'azure' block is required")
}

// Unlike gcp's project, a subscription cannot be inferred, so it is
// required rather than resolved later.
func TestValidateRequiresSubscription(t *testing.T) {
	require.ErrorIs(t, (&Options{Service: ServiceVM}).Validate(),
		ErrMissingSubscription)
	require.NoError(t, valid().Validate())
}

func TestValidateRejectsAnUnknownCloud(t *testing.T) {
	for _, c := range SupportedClouds() {
		o := valid()
		o.Cloud = c
		require.NoError(t, o.Validate(), "%s is advertised as supported", c)
	}
	o := valid()
	o.Cloud = "germany"
	require.ErrorIs(t, o.Validate(), ErrInvalidCloud)
}

// A half-specified client credential authenticates as nobody; it is far
// more likely a mistake than an intent to fall back to managed identity.
func TestValidateRejectsHalfSpecifiedCredentials(t *testing.T) {
	for name, mutate := range map[string]func(*Options){
		"secret without client_id": func(o *Options) {
			o.ClientSecret, o.TenantID = "shh", "t"
		},
		"secret without tenant_id": func(o *Options) {
			o.ClientSecret, o.ClientID = "shh", "c"
		},
		"federated without client_id": func(o *Options) {
			o.FederatedTokenFile, o.TenantID = "/tok", "t"
		},
		"federated without tenant_id": func(o *Options) {
			o.FederatedTokenFile, o.ClientID = "/tok", "c"
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := valid()
			mutate(o)
			require.ErrorIs(t, o.Validate(), ErrIncompleteClientCredentials)
		})
	}
}

// Two explicit credentials is ambiguous rather than additive.
func TestValidateRejectsConflictingCredentials(t *testing.T) {
	o := valid()
	o.TenantID, o.ClientID = "t", "c"
	o.ClientSecret, o.FederatedTokenFile = "shh", "/tok"
	require.ErrorIs(t, o.Validate(), ErrConflictingCredentials)
}

func TestValidateAcceptsEachCompleteCredentialMode(t *testing.T) {
	// managed identity: no credential fields at all
	require.NoError(t, valid().Validate())

	// user-assigned managed identity: client_id alone selects the identity
	mi := valid()
	mi.ClientID = "c"
	require.NoError(t, mi.Validate())

	sp := valid()
	sp.TenantID, sp.ClientID, sp.ClientSecret = "t", "c", "shh"
	require.NoError(t, sp.Validate())

	wi := valid()
	wi.TenantID, wi.ClientID, wi.FederatedTokenFile = "t", "c", "/tok"
	require.NoError(t, wi.Validate())
}

func TestGetService(t *testing.T) {
	require.Empty(t, (*Options)(nil).GetService())
	require.Empty(t, (&Options{}).GetService())
	require.Equal(t, ServiceVM, (&Options{Service: ServiceVM}).GetService())
}

func TestGetCloudDefaultsToPublic(t *testing.T) {
	require.Equal(t, CloudPublic, (*Options)(nil).GetCloud())
	require.Equal(t, CloudPublic, (&Options{}).GetCloud())
	require.Equal(t, CloudChina, (&Options{Cloud: CloudChina}).GetCloud())
}

// One setting selects both endpoints, because keeping two URLs consistent
// by hand is exactly the kind of thing that silently half-works.
func TestCloudSelectsBothEndpoints(t *testing.T) {
	for _, tc := range []struct{ cloud, mgmt, login string }{
		{CloudPublic, "https://management.azure.com", "https://login.microsoftonline.com"},
		{CloudUSGovernment, "https://management.usgovcloudapi.net", "https://login.microsoftonline.us"},
		{CloudChina, "https://management.chinacloudapi.cn", "https://login.chinacloudapi.cn"},
	} {
		t.Run(tc.cloud, func(t *testing.T) {
			o := &Options{Cloud: tc.cloud}
			require.Equal(t, tc.mgmt, o.ManagementEndpoint())
			require.Equal(t, tc.login, o.LoginEndpoint())
		})
	}
	// an unset cloud resolves to the public one on both
	require.Equal(t, (&Options{Cloud: CloudPublic}).ManagementEndpoint(),
		(&Options{}).ManagementEndpoint())
	require.Equal(t, (&Options{Cloud: CloudPublic}).LoginEndpoint(),
		(&Options{}).LoginEndpoint())
}

// Every supported cloud must have both endpoints, or selecting it would
// produce an empty URL rather than an error.
func TestEverySupportedCloudHasBothEndpoints(t *testing.T) {
	for _, c := range SupportedClouds() {
		o := &Options{Cloud: c}
		require.True(t, strings.HasPrefix(o.ManagementEndpoint(), "https://"),
			"%s has no management endpoint", c)
		require.True(t, strings.HasPrefix(o.LoginEndpoint(), "https://"),
			"%s has no login endpoint", c)
	}
	require.Len(t, cloudEndpoints, len(SupportedClouds()),
		"a cloud was added to one list but not the other")
}

// ARM requires an explicit api-version on every request, so both are pinned
// rather than omitted.
func TestAPIVersionsDefaultAndOverride(t *testing.T) {
	require.Equal(t, DefaultComputeAPIVersion, (*Options)(nil).GetComputeAPIVersion())
	require.Equal(t, DefaultNetworkAPIVersion, (*Options)(nil).GetNetworkAPIVersion())
	require.Equal(t, DefaultComputeAPIVersion, New().GetComputeAPIVersion())
	require.Equal(t, DefaultNetworkAPIVersion, New().GetNetworkAPIVersion())

	o := &Options{ComputeAPIVersion: "2023-01-01", NetworkAPIVersion: "2023-02-01"}
	require.Equal(t, "2023-01-01", o.GetComputeAPIVersion())
	require.Equal(t, "2023-02-01", o.GetNetworkAPIVersion())

	require.NotEqual(t, DefaultComputeAPIVersion, DefaultNetworkAPIVersion,
		"compute and network are versioned independently by Azure")
}

func TestCloneIsIndependent(t *testing.T) {
	o := valid()
	o.TenantID, o.ClientID, o.ClientSecret = "t", "c", "shh"
	o.ResourceGroup, o.PowerState = "rg", true

	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	c.SubscriptionID = "sub-2"
	c.PowerState = false
	require.Equal(t, "sub-1", o.SubscriptionID)
	require.True(t, o.PowerState)
}

func TestClientSecretIsRedactedOnMarshal(t *testing.T) {
	o := valid()
	o.TenantID, o.ClientID, o.ClientSecret = "t", "client-id-value", "super-secret"
	out, err := yaml.Marshal(o)
	require.NoError(t, err)
	require.NotContains(t, string(out), "super-secret")
	require.Contains(t, string(out), "client-id-value",
		"only the secret is redacted; the identifiers stay legible")
}

func TestYAMLRoundTrip(t *testing.T) {
	const doc = `
service: vm
subscription_id: sub-1
tenant_id: t
client_id: c
client_secret: shh
cloud: usgovernment
resource_group: prod-rg
power_state: true
compute_api_version: "2023-01-01"
network_api_version: "2023-02-01"
`
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(doc), &o))
	require.Equal(t, ServiceVM, o.Service)
	require.Equal(t, "sub-1", o.SubscriptionID)
	require.Equal(t, "shh", string(o.ClientSecret),
		"unmarshaling keeps the value; only output is redacted")
	require.Equal(t, CloudUSGovernment, o.GetCloud())
	require.Equal(t, "prod-rg", o.ResourceGroup)
	require.True(t, o.PowerState)
	require.Equal(t, "2023-01-01", o.GetComputeAPIVersion())
	require.NoError(t, o.Validate())
}

func TestNewErrInvalidOptions(t *testing.T) {
	err := NewErrInvalidOptions("fleet", "'subscription_id' is required")
	require.EqualError(t, err,
		`invalid azure options for discoverer "fleet": 'subscription_id' is required`)

	var target *derrors.InvalidDiscoveryOptionsError
	require.True(t, errors.As(err, &target))
}
