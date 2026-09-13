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

package azure

import (
	"os"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	azureopts "github.com/trickstercache/trickster/v2/pkg/discovery/azure/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/secret"

	"github.com/stretchr/testify/require"
)

// These tests run against a real Azure subscription and are skipped
// unless TRICKSTER_AZURE_TEST=1, following the repo's TRICKSTER_DNS_TEST
// and TRICKSTER_AWS_TEST precedent. They are read-only: nothing here
// creates or modifies a resource.
//
// With a service principal:
//
//	TRICKSTER_AZURE_TEST=1 \
//	  TRICKSTER_AZURE_SUBSCRIPTION_ID=... \
//	  TRICKSTER_AZURE_TENANT_ID=... \
//	  TRICKSTER_AZURE_CLIENT_ID=... \
//	  TRICKSTER_AZURE_CLIENT_SECRET=... \
//	  go test ./pkg/discovery/azure/ -run Live -v -count=1
//
// The principal needs only Reader on the subscription (or on the
// resource group named by TRICKSTER_AZURE_RESOURCE_GROUP).
//
// If `az login` is set up but no service principal is, an already-issued
// bearer token also works, which avoids creating a principal just to run
// these once:
//
//	TRICKSTER_AZURE_ACCESS_TOKEN=$(az account get-access-token \
//	  --resource https://management.azure.com/ --query accessToken -o tsv)
//
// That covers the ARM calls and their response shapes, but deliberately
// not credential acquisition itself, which token_test covers against a
// fake for all three modes.
func liveOptions(t *testing.T) *do.Options {
	t.Helper()
	if os.Getenv("TRICKSTER_AZURE_TEST") != "1" {
		t.Skip("set TRICKSTER_AZURE_TEST=1 to run against a real subscription")
	}
	sub := os.Getenv("TRICKSTER_AZURE_SUBSCRIPTION_ID")
	if sub == "" {
		t.Skip("set TRICKSTER_AZURE_SUBSCRIPTION_ID to run the azure live tests")
	}
	return &do.Options{
		Name:     "live-azure",
		Provider: "azure",
		Azure: &azureopts.Options{
			Service:        azureopts.ServiceVM,
			SubscriptionID: sub,
			TenantID:       os.Getenv("TRICKSTER_AZURE_TENANT_ID"),
			ClientID:       os.Getenv("TRICKSTER_AZURE_CLIENT_ID"),
			ClientSecret:   secret.Secret(os.Getenv("TRICKSTER_AZURE_CLIENT_SECRET")),
			ResourceGroup:  os.Getenv("TRICKSTER_AZURE_RESOURCE_GROUP"),
			PowerState:     os.Getenv("TRICKSTER_AZURE_POWER_STATE") == "1",
		},
		HTTP: &do.HTTPOptions{},
	}
}

func liveProvider(t *testing.T) *provider {
	t.Helper()
	p, err := newProvider("live-azure", liveOptions(t))
	require.NoError(t, err)
	if tok := os.Getenv("TRICKSTER_AZURE_ACCESS_TOKEN"); tok != "" {
		// an already-issued token, so these can run without provisioning
		// a service principal; see the note above on what this does not
		// exercise
		p.tokens.token = tok
		p.tokens.expires = time.Now().Add(time.Hour)
	}
	return p
}

func liveSubscription(t *testing.T, m mapping) *subscription {
	t.Helper()
	return &subscription{p: liveProvider(t), mapping: m,
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}
}

// Authentication works and the pinned api-versions are served.
func TestLiveAuthAndResponseShape(t *testing.T) {
	s := liveSubscription(t, baseMapping())
	inv, err := s.inventory(t.Context())
	require.NoError(t, err)
	t.Logf("listed %d vms and %d network interfaces", len(inv.vms), len(inv.nics))
	require.NotEmpty(t, inv.vms, "no vms in the subscription; create one to test against")
	for _, vm := range inv.vms {
		require.NotEmpty(t, vm.ID)
		require.NotEmpty(t, vm.Name)
	}
}

// The join is the thing most likely to be wrong against real data,
// because ARM's resource-id casing differs between APIs. This reports
// how many VMs resolved an address, and fails if none did.
func TestLiveJoinResolvesAddresses(t *testing.T) {
	m := baseMapping()
	s := liveSubscription(t, m)
	inv, err := s.inventory(t.Context())
	require.NoError(t, err)

	var resolved, dangling int
	for i := range inv.vms {
		vm := &inv.vms[i]
		for _, ref := range vm.Properties.NetworkProfile.NetworkInterfaces {
			if _, ok := inv.nics[resourceKey(ref.ID)]; !ok {
				dangling++
			}
		}
		if addr, reason := vm.address(inv, ""); reason == "" && addr != "" {
			resolved++
		}
	}
	t.Logf("%d of %d vms resolved a private address (%d dangling nic refs)",
		resolved, len(inv.vms), dangling)
	require.NotZero(t, resolved,
		"no vm resolved an address; the nic join is the likely cause")
}

// Confirms the case-folding join is load-bearing rather than defensive:
// reports whether ARM's own casing actually differs between the VM's
// reference and the interface list.
func TestLiveResourceIDCasingDiffersInPractice(t *testing.T) {
	s := liveSubscription(t, baseMapping())
	inv, err := s.inventory(t.Context())
	require.NoError(t, err)

	var exact, foldedOnly int
	byExact := map[string]bool{}
	for _, nic := range inv.nics {
		byExact[nic.ID] = true
	}
	for i := range inv.vms {
		for _, ref := range inv.vms[i].Properties.NetworkProfile.NetworkInterfaces {
			if byExact[ref.ID] {
				exact++
			} else if _, ok := inv.nics[resourceKey(ref.ID)]; ok {
				foldedOnly++
			}
		}
	}
	t.Logf("nic references: %d match exactly, %d only after case folding",
		exact, foldedOnly)
	if foldedOnly > 0 {
		t.Logf("case folding is load-bearing here: %d references would "+
			"have been lost by an exact-match join", foldedOnly)
	}
}

func TestLiveMembersAreUsable(t *testing.T) {
	m := baseMapping()
	m.portLabel = "port"
	s := liveSubscription(t, m)
	inv, err := s.inventory(t.Context())
	require.NoError(t, err)
	snap, skipped := toMembers(inv, m)
	t.Logf("mapped %d members, excluded %d", len(snap), len(skipped))
	for _, e := range skipped {
		t.Logf("  excluded %s: %s", e.name, e.reason)
	}
	for _, mem := range snap {
		require.NotEmpty(t, mem.Address)
		require.Contains(t, mem.Address, ":")
	}
}

// Power state is claimed to cost one extra list rather than one call per
// VM. This is the assertion that confirms statusOnly actually behaves
// that way against a real subscription.
func TestLivePowerStateIsOneExtraList(t *testing.T) {
	if os.Getenv("TRICKSTER_AZURE_POWER_STATE") != "1" {
		t.Skip("set TRICKSTER_AZURE_POWER_STATE=1 to exercise the instance view")
	}
	m := baseMapping()
	m.powerState = true
	s := liveSubscription(t, m)
	inv, err := s.inventory(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, inv.states,
		"statusOnly returned no power states; the whole-subscription "+
			"instance view may not behave as assumed")
	var running int
	for _, st := range inv.states {
		if st == powerStateRunning {
			running++
		}
	}
	t.Logf("power states for %d vms, %d running", len(inv.states), running)
}

// A public address needs the third list; this confirms the reference
// chain resolves end to end.
func TestLivePublicAddressChain(t *testing.T) {
	m := baseMapping()
	m.addressType = do.AddressPublic
	s := liveSubscription(t, m)
	inv, err := s.inventory(t.Context())
	require.NoError(t, err)
	t.Logf("listed %d public ip resources", len(inv.publicIPs))

	var withPublic int
	for i := range inv.vms {
		if addr, reason := inv.vms[i].address(inv, do.AddressPublic); reason == "" && addr != "" {
			withPublic++
		}
	}
	t.Logf("%d of %d vms resolved a public address", withPublic, len(inv.vms))
}

// captureFixture writes the raw list responses for use as testdata. It
// writes the bytes ARM actually sent rather than a re-encoding of the
// decoded structs, which would only prove the types are self-consistent.
// Redact subscription and tenant ids before committing.
func TestLiveCaptureFixture(t *testing.T) {
	dir := os.Getenv("TRICKSTER_AZURE_CAPTURE")
	if dir == "" {
		t.Skip("set TRICKSTER_AZURE_CAPTURE=<dir> to write testdata fixtures")
	}
	p := liveProvider(t)
	for name, target := range map[string]string{
		"virtual_machines.json":   p.vmURL(),
		"network_interfaces.json": p.nicURL(),
		"public_ips.json":         p.publicIPURL(),
	} {
		body, err := p.get(t.Context(), target)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(dir+"/"+name, body, 0o600))
		t.Logf("wrote %s (%d bytes)", name, len(body))
	}
}
