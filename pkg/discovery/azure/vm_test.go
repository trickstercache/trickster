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
	"path/filepath"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
)

const (
	subID  = "00000000-0000-0000-0000-000000000000"
	rgPath = "/subscriptions/" + subID + "/resourceGroups/prod-rg"
	nicID  = rgPath + "/providers/Microsoft.Network/networkInterfaces/nic-1"
	pipID  = rgPath + "/providers/Microsoft.Network/publicIPAddresses/pip-1"
)

func vmID(name string) string {
	return rgPath + "/providers/Microsoft.Compute/virtualMachines/" + name
}

func testVM(name string, nicIDs ...string) virtualMachine {
	refs := make([]nicRef, len(nicIDs))
	for i, id := range nicIDs {
		refs[i] = nicRef{ID: id}
	}
	return virtualMachine{
		ID:       vmID(name),
		Name:     name,
		Location: "eastus",
		Tags:     map[string]string{"role": "prometheus", "port": "9090"},
		Properties: vmProperties{
			VMID:            "aaaaaaaa-0000-0000-0000-000000000001",
			HardwareProfile: hardwareProfile{VMSize: "Standard_B1s"},
			NetworkProfile:  networkProfile{NetworkInterfaces: refs},
		},
	}
}

func testNIC(id, privateIP string) *networkInterface {
	return &networkInterface{
		ID: id,
		Properties: nicProps{
			IPConfigurations: []ipConfiguration{{
				Properties: ipConfProps{Primary: true, PrivateIPAddress: privateIP},
			}},
		},
	}
}

func inv(vms []virtualMachine, nics ...*networkInterface) *inventory {
	i := &inventory{
		vms:       vms,
		nics:      map[string]*networkInterface{},
		publicIPs: map[string]*publicIPAddress{},
		states:    map[string]string{},
	}
	for _, n := range nics {
		i.nics[resourceKey(n.ID)] = n
	}
	return i
}

func baseMapping() mapping { return mapping{scheme: "http", port: "9090"} }

// Azure resource ids are case-insensitive, and the casing genuinely
// differs between APIs: a VM's networkProfile reference commonly spells
// the resource group differently from the way the networkInterfaces list
// spells the same group. Joining on the raw string loses those VMs
// silently, which presents as members vanishing for no visible reason.
func TestJoinIsCaseInsensitive(t *testing.T) {
	// the VM references the NIC with a differently-cased resource group,
	// exactly as ARM does in practice
	shouty := "/subscriptions/" + subID +
		"/resourceGroups/PROD-RG/providers/Microsoft.Network/networkInterfaces/nic-1"
	vm := testVM("web-1", shouty)
	i := inv([]virtualMachine{vm}, testNIC(nicID, "10.0.0.4"))

	snap, skipped := toMembers(i, baseMapping())
	require.Empty(t, skipped, "the reference differs only by case and must still join")
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.4:9090", snap[0].Address)
}

// A dangling reference is called out specifically, because the most
// likely cause is the join rather than a genuinely address-less VM.
func TestUnresolvedNICReferenceIsExplained(t *testing.T) {
	vm := testVM("web-1", nicID)
	i := inv([]virtualMachine{vm}) // no NICs at all
	_, skipped := toMembers(i, baseMapping())
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "none of the vm's network interfaces resolved")
}

func TestVMWithNoNICIsExcluded(t *testing.T) {
	vm := testVM("web-1")
	_, skipped := toMembers(inv([]virtualMachine{vm}), baseMapping())
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "no network interface")
}

// A VM with several NICs marks one primary; that one supplies the
// address, whatever order ARM returned them in.
func TestPrimaryNICWins(t *testing.T) {
	secondary := rgPath + "/providers/Microsoft.Network/networkInterfaces/nic-2"
	vm := testVM("web-1")
	vm.Properties.NetworkProfile.NetworkInterfaces = []nicRef{
		{ID: secondary},
		{ID: nicID, Properties: nicRefProps{Primary: true}},
	}
	i := inv([]virtualMachine{vm},
		testNIC(nicID, "10.0.0.4"), testNIC(secondary, "10.0.9.9"))
	snap, _ := toMembers(i, baseMapping())
	require.Equal(t, "10.0.0.4:9090", snap[0].Address,
		"the primary nic, not the first one listed")
}

// With exactly one NIC Azure commonly omits the primary flag entirely, so
// a lone NIC must still resolve.
func TestLoneNICNeedsNoPrimaryFlag(t *testing.T) {
	vm := testVM("web-1", nicID)
	require.False(t, vm.Properties.NetworkProfile.NetworkInterfaces[0].Properties.Primary)
	snap, skipped := toMembers(inv([]virtualMachine{vm}, testNIC(nicID, "10.0.0.4")),
		baseMapping())
	require.Empty(t, skipped)
	require.Len(t, snap, 1)
}

// A public address is a reference from the NIC to a separate resource, so
// it resolves only when that third list was fetched.
func TestPublicAddressResolvesThroughItsOwnResource(t *testing.T) {
	nic := testNIC(nicID, "10.0.0.4")
	nic.Properties.IPConfigurations[0].Properties.PublicIPAddress =
		&publicIPRef{ID: pipID}
	vm := testVM("web-1", nicID)

	m := baseMapping()
	m.addressType = do.AddressPublic

	// without the public ip list, the reference cannot resolve
	i := inv([]virtualMachine{vm}, nic)
	_, skipped := toMembers(i, m)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "no public ip address")

	// with it, it does -- and case-insensitively, as for nics
	i.publicIPs[resourceKey(pipID)] = &publicIPAddress{
		ID: pipID, Properties: publicIPProps{IPAddress: "20.1.2.3"}}
	snap, skipped := toMembers(i, m)
	require.Empty(t, skipped)
	require.Equal(t, "20.1.2.3:9090", snap[0].Address)
}

// Without the instance view there is nothing to map: the VM list carries
// provisioning state, not power state, and a provisioned VM may be
// stopped. Claiming Ready would assert something Azure never said.
func TestReadinessIsUnknownWithoutPowerState(t *testing.T) {
	vm := testVM("web-1", nicID)
	snap, _ := toMembers(inv([]virtualMachine{vm}, testNIC(nicID, "10.0.0.4")),
		baseMapping())
	require.Equal(t, discovery.ReadyUnknown, snap[0].Ready)
}

func TestPowerStateDrivesReadinessAndOmitsStoppedVMs(t *testing.T) {
	running := testVM("web-1", nicID)
	stopped := testVM("web-2", nicID)
	stopped.ID = vmID("web-2")

	i := inv([]virtualMachine{running, stopped}, testNIC(nicID, "10.0.0.4"))
	i.states[resourceKey(running.ID)] = powerStateRunning
	i.states[resourceKey(stopped.ID)] = "PowerState/deallocated"

	m := baseMapping()
	m.powerState = true
	snap, _ := toMembers(i, m)
	require.Len(t, snap, 1, "a stopped vm leaves membership rather than being NotReady")
	require.Equal(t, "web-1", snap[0].Name)
	require.Equal(t, discovery.Ready, snap[0].Ready)
	require.Equal(t, "running", snap[0].Labels["power_state"])
}

// The instance view carries provisioning statuses alongside power state;
// only the power-state entry is the one to read.
func TestPowerStateIsPickedOutOfTheStatuses(t *testing.T) {
	iv := &instanceView{Statuses: []instanceStatus{
		{Code: "ProvisioningState/succeeded"},
		{Code: "PowerState/running"},
	}}
	require.Equal(t, powerStateRunning, powerStateOf(iv))
	require.Empty(t, powerStateOf(nil))
	require.Empty(t, powerStateOf(&instanceView{Statuses: []instanceStatus{
		{Code: "ProvisioningState/succeeded"}}}))
}

// Azure treats tag names case-insensitively; so must the port lookup and
// the tag filter, or a VM tagged Role is invisible to a query for role.
func TestTagsAreCaseInsensitive(t *testing.T) {
	vm := testVM("web-1", nicID)
	vm.Tags = map[string]string{"Role": "prometheus", "Port": "8080"}

	m := mapping{scheme: "http", portLabel: "port", tags: []string{"role"}}
	snap, skipped := toMembers(inv([]virtualMachine{vm}, testNIC(nicID, "10.0.0.4")), m)
	require.Empty(t, skipped)
	require.Equal(t, "10.0.0.4:8080", snap[0].Address)
}

func TestTagFilterIsConjunctive(t *testing.T) {
	both := testVM("web-1", nicID)
	both.Tags = map[string]string{"role": "prom", "env": "prod"}
	one := testVM("web-2", nicID)
	one.Tags = map[string]string{"role": "prom"}

	m := baseMapping()
	m.tags = []string{"role", "env"}
	snap, _ := toMembers(inv([]virtualMachine{both, one}, testNIC(nicID, "10.0.0.4")), m)
	require.Len(t, snap, 1, "every listed tag must be present, not any")
	require.Equal(t, "web-1", snap[0].Name)
}

// A filtered-out VM must not be validated: a machine the operator
// excluded should not appear as an exclusion.
func TestFilteredOutVMsAreNotReported(t *testing.T) {
	vm := testVM("web-1") // no nic, would otherwise be excluded
	vm.Tags = map[string]string{"role": "other"}
	m := baseMapping()
	m.tags = []string{"absent"}
	snap, skipped := toMembers(inv([]virtualMachine{vm}), m)
	require.Empty(t, snap)
	require.Empty(t, skipped)
}

func TestPortLabelWinsOverStaticPort(t *testing.T) {
	vm := testVM("web-1", nicID)
	vm.Tags = map[string]string{"port": "8080"}
	m := baseMapping()
	m.portLabel = "port"
	snap, _ := toMembers(inv([]virtualMachine{vm}, testNIC(nicID, "10.0.0.4")), m)
	require.Equal(t, "10.0.0.4:8080", snap[0].Address)

	// a vm without the tag falls back to the static port
	bare := testVM("web-2", nicID)
	bare.Tags = nil
	snap, _ = toMembers(inv([]virtualMachine{bare}, testNIC(nicID, "10.0.0.4")), m)
	require.Equal(t, "10.0.0.4:9090", snap[0].Address)
}

func TestInvalidPortTagIsExcluded(t *testing.T) {
	vm := testVM("web-1", nicID)
	vm.Tags = map[string]string{"port": "nope"}
	m := baseMapping()
	m.portLabel = "port"
	_, skipped := toMembers(inv([]virtualMachine{vm}, testNIC(nicID, "10.0.0.4")), m)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "is not a valid port")
}

func TestNoPortAtAllIsExcluded(t *testing.T) {
	vm := testVM("web-1", nicID)
	_, skipped := toMembers(inv([]virtualMachine{vm}, testNIC(nicID, "10.0.0.4")),
		mapping{scheme: "http"})
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "no port")
}

func TestLabels(t *testing.T) {
	vm := testVM("web-1", nicID)
	vm.Tags = map[string]string{"role": "prometheus", "vm_name": "spoofed"}
	snap, _ := toMembers(inv([]virtualMachine{vm}, testNIC(nicID, "10.0.0.4")),
		baseMapping())
	l := snap[0].Labels
	require.Equal(t, "web-1", l["vm_name"])
	require.Equal(t, "spoofed", l["tag_vm_name"],
		"vm tags are prefixed so they cannot shadow ours")
	require.Equal(t, "prometheus", l["tag_role"])
	require.Equal(t, "eastus", l["location"])
	require.Equal(t, "Standard_B1s", l["vm_size"])
	require.Equal(t, "prod-rg", l["resource_group"])
	require.Equal(t, "10.0.0.4", l["private_ip"])
	require.NotContains(t, l, "power_state", "not requested, so not asserted")
}

func TestResourceGroupOf(t *testing.T) {
	require.Equal(t, "prod-rg", resourceGroupOf(vmID("web-1")))
	// ARM varies the casing of the segment name itself
	require.Equal(t, "g", resourceGroupOf("/subscriptions/s/resourcegroups/g/providers/x"))
	require.Empty(t, resourceGroupOf("/subscriptions/s/providers/x"))
	require.Empty(t, resourceGroupOf(""))
}

func TestReplicaGroupComesFromATag(t *testing.T) {
	vm := testVM("web-1", nicID)
	vm.Tags = map[string]string{"shard": "a", "port": "9090"}
	m := baseMapping()
	m.replicaGroupLabel = "shard"
	snap, _ := toMembers(inv([]virtualMachine{vm}, testNIC(nicID, "10.0.0.4")), m)
	require.Equal(t, "a", snap[0].ReplicaGroup)
}

// ARM's list order is not contractual; an unstable order would defeat the
// Emitter's no-change suppression.
func TestMembersAreSortedByAddress(t *testing.T) {
	nic2 := rgPath + "/providers/Microsoft.Network/networkInterfaces/nic-2"
	nic3 := rgPath + "/providers/Microsoft.Network/networkInterfaces/nic-3"
	vms := []virtualMachine{
		testVM("c", nic3), testVM("a", nicID), testVM("b", nic2),
	}
	i := inv(vms, testNIC(nicID, "10.0.0.10"),
		testNIC(nic2, "10.0.0.20"), testNIC(nic3, "10.0.0.30"))
	snap, _ := toMembers(i, baseMapping())
	require.Equal(t, []string{
		"10.0.0.10:9090", "10.0.0.20:9090", "10.0.0.30:9090",
	}, addressesOf(snap))
}

func TestParseListEmptyIsAuthoritative(t *testing.T) {
	for _, empty := range []string{"", "  "} {
		lr, err := parseList[virtualMachine]([]byte(empty))
		require.NoError(t, err)
		require.Empty(t, lr.Value)
	}
	lr, err := parseList[virtualMachine]([]byte(`{"value":[]}`))
	require.NoError(t, err)
	require.Empty(t, lr.Value)
}

func TestParseListRejectsMalformedJSON(t *testing.T) {
	_, err := parseList[virtualMachine]([]byte("{{{"))
	require.Error(t, err)
}

func addressesOf(s discovery.Snapshot) []string {
	out := make([]string, len(s))
	for i, m := range s {
		out[i] = m.Address
	}
	return out
}

// --- the captured real responses -----------------------------------------

// The fixtures below are redacted ARM responses captured from a live
// subscription (Microsoft.Compute 2024-07-01, Microsoft.Network
// 2024-05-01). They keep the types honest about the shape ARM actually
// sends, as opposed to the shape the documentation describes.
func realInventory(t *testing.T) *inventory {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		require.NoError(t, err)
		return b
	}
	vms, err := parseList[virtualMachine](read("virtual_machines.json"))
	require.NoError(t, err)
	nics, err := parseList[networkInterface](read("network_interfaces.json"))
	require.NoError(t, err)
	pips, err := parseList[publicIPAddress](read("public_ips.json"))
	require.NoError(t, err)

	i := &inventory{
		vms:       vms.Value,
		nics:      map[string]*networkInterface{},
		publicIPs: map[string]*publicIPAddress{},
		states:    map[string]string{},
	}
	for n := range nics.Value {
		i.nics[resourceKey(nics.Value[n].ID)] = &nics.Value[n]
	}
	for n := range pips.Value {
		i.publicIPs[resourceKey(pips.Value[n].ID)] = &pips.Value[n]
	}
	return i
}

func TestParseRealResponses(t *testing.T) {
	i := realInventory(t)
	require.Len(t, i.vms, 4)
	require.Len(t, i.nics, 4)
	require.Len(t, i.publicIPs, 1)

	for _, vm := range i.vms {
		require.NotEmpty(t, vm.ID)
		require.NotEmpty(t, vm.Name)
		require.NotEmpty(t, vm.Location)
		require.NotEmpty(t, vm.Properties.HardwareProfile.VMSize)
		require.NotEmpty(t, vm.Properties.NetworkProfile.NetworkInterfaces,
			"every vm must reference at least one nic")
	}
}

// The whole point of the two-call design: real VMs carry no address, and
// the join is what produces one.
func TestRealResponsesJoinToAddresses(t *testing.T) {
	i := realInventory(t)
	m := baseMapping()
	m.portLabel = "port"

	snap, skipped := toMembers(i, m)
	// tk-disco-4 carries no port tag, and the static port in baseMapping
	// covers it, so all four map
	require.Len(t, snap, 4)
	require.Len(t, skipped, 0)

	byName := map[string]string{}
	for _, mem := range snap {
		byName[mem.Name] = mem.Address
	}
	require.Equal(t, "10.0.0.4:9090", byName["tk-disco-1"])
	require.Equal(t, "10.0.0.5:9091", byName["tk-disco-2"],
		"the port comes from each vm's own tag")
	require.Equal(t, "10.0.0.6:9092", byName["tk-disco-3"])
	require.Equal(t, "10.0.0.7:9090", byName["tk-disco-4"],
		"no port tag, so the static port applies")
}

// Only one VM in the fixture has a public address, so this pins the
// full reference chain: vm -> nic -> ipConfiguration -> publicIPAddress.
func TestRealResponsesResolvePublicAddresses(t *testing.T) {
	i := realInventory(t)
	m := baseMapping()
	m.addressType = do.AddressPublic

	snap, skipped := toMembers(i, m)
	require.Len(t, snap, 1, "only one vm was created with a public address")
	require.Equal(t, "tk-disco-2", snap[0].Name)
	require.Equal(t, "203.0.113.10:9090", snap[0].Address)
	require.Len(t, skipped, 3)
	for _, e := range skipped {
		require.Contains(t, e.reason, "no public ip address")
	}
}

// A tag filter against real tags, including the case-insensitivity Azure
// itself applies.
func TestRealResponsesTagFilter(t *testing.T) {
	i := realInventory(t)
	m := baseMapping()
	m.tags = []string{"shard"}
	snap, _ := toMembers(i, m)
	require.Len(t, snap, 2, "only two vms carry a shard tag")

	m.tags = []string{"SHARD"}
	snap, _ = toMembers(i, m)
	require.Len(t, snap, 2, "azure treats tag names case-insensitively")
}

func TestRealResponsesLabels(t *testing.T) {
	i := realInventory(t)
	snap, _ := toMembers(i, baseMapping())
	var one discovery.Member
	for _, m := range snap {
		if m.Name == "tk-disco-1" {
			one = m
		}
	}
	require.Equal(t, "eastus", one.Labels["location"])
	require.Equal(t, "trickster-disco-rg", one.Labels["resource_group"])
	require.Equal(t, "10.0.0.4", one.Labels["private_ip"])
	require.Equal(t, "prometheus", one.Labels["tag_role"])
	require.NotEmpty(t, one.Labels["vm_size"])
	require.NotEmpty(t, one.Labels["vm_id"])
}
