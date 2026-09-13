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
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
)

// powerStateRunning is the instance-view status code for a running VM.
// Azure reports power state as a status code string rather than a field.
const powerStateRunning = "PowerState/running"

// powerStatePrefix identifies power-state entries among the instance
// view's statuses, which also carry provisioning states
const powerStatePrefix = "PowerState/"

// listResponse is the envelope every ARM list returns. Paging is by
// nextLink, which is a complete URL rather than a token to append.
type listResponse[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"nextLink"`
}

// virtualMachine is one Microsoft.Compute/virtualMachines entry
type virtualMachine struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags"`
	Properties vmProperties      `json:"properties"`
}

// vmProperties carries the fields Trickster maps
type vmProperties struct {
	VMID            string          `json:"vmId"`
	NetworkProfile  networkProfile  `json:"networkProfile"`
	HardwareProfile hardwareProfile `json:"hardwareProfile"`
	// InstanceView is present only on a statusOnly list
	InstanceView *instanceView `json:"instanceView"`
}

type hardwareProfile struct {
	VMSize string `json:"vmSize"`
}

// networkProfile references the VM's NICs by resource id. The VM carries
// no addresses of its own, which is why a second list and a join are
// unavoidable.
type networkProfile struct {
	NetworkInterfaces []nicRef `json:"networkInterfaces"`
}

type nicRef struct {
	ID         string      `json:"id"`
	Properties nicRefProps `json:"properties"`
}

type nicRefProps struct {
	Primary bool `json:"primary"`
}

// instanceView carries the statuses a statusOnly list returns
type instanceView struct {
	Statuses []instanceStatus `json:"statuses"`
}

type instanceStatus struct {
	Code string `json:"code"`
}

// networkInterface is one Microsoft.Network/networkInterfaces entry
type networkInterface struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Properties nicProps `json:"properties"`
}

type nicProps struct {
	Primary          bool              `json:"primary"`
	IPConfigurations []ipConfiguration `json:"ipConfigurations"`
}

type ipConfiguration struct {
	Name       string      `json:"name"`
	Properties ipConfProps `json:"properties"`
}

type ipConfProps struct {
	Primary          bool   `json:"primary"`
	PrivateIPAddress string `json:"privateIPAddress"`
	// PublicIPAddress is a reference, not an address: resolving it needs a
	// third list call
	PublicIPAddress *publicIPRef `json:"publicIPAddress"`
}

type publicIPRef struct {
	ID string `json:"id"`
}

// publicIPAddress is one Microsoft.Network/publicIPAddresses entry
type publicIPAddress struct {
	ID         string        `json:"id"`
	Properties publicIPProps `json:"properties"`
}

type publicIPProps struct {
	IPAddress string `json:"ipAddress"`
}

// mapping is the per-query state the member mapping needs
type mapping struct {
	scheme            string
	addressType       string
	port              string
	portLabel         string
	replicaGroupLabel string
	tags              []string
	// powerState is true when the instance view was requested, which is
	// what makes readiness anything other than unknown
	powerState bool
}

// excluded records a VM that could not become a member, and why
type excluded struct {
	name   string
	reason string
}

// inventory is one refresh's worth of joined resources
type inventory struct {
	vms []virtualMachine
	// nics and publicIPs are keyed by lowercased resource id; see resourceKey
	nics      map[string]*networkInterface
	publicIPs map[string]*publicIPAddress
	// states is the power state per lowercased VM id, when requested
	states map[string]string
}

// resourceKey normalizes an ARM resource id for use as a join key.
//
// This is not incidental tidying. Azure resource ids are case-insensitive,
// and the casing genuinely differs between APIs -- a VM's
// networkProfile reference commonly spells the resource group differently
// from the way the networkInterfaces list spells the same group. Joining
// on the raw string silently loses those VMs, which presents as members
// disappearing for no visible reason.
func resourceKey(id string) string { return strings.ToLower(strings.TrimSpace(id)) }

// parseList decodes one page of an ARM list response
func parseList[T any](body []byte) (*listResponse[T], error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return &listResponse[T]{}, nil
	}
	var out listResponse[T]
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, fmt.Errorf("decoding azure list response: %w", err)
	}
	return &out, nil
}

// toMembers maps the joined inventory to members, returning the members
// and the VMs that could not become one. A VM that cannot yield an
// address or a port is excluded and reported rather than failing the
// refresh: a subscription routinely holds machines that are simply not
// tagged for discovery.
func toMembers(inv *inventory, m mapping) (discovery.Snapshot, []excluded) {
	out := make(discovery.Snapshot, 0, len(inv.vms))
	var skipped []excluded
	for i := range inv.vms {
		vm := &inv.vms[i]
		if !vm.hasAllTags(m.tags) {
			continue
		}
		if m.powerState && !vm.isRunning(inv.states) {
			// a stopped VM leaves membership entirely rather than being
			// carried as NotReady, so machines drain from pools as they
			// stop; this is only knowable when the instance view was
			// requested
			continue
		}
		member, reason := vm.toMember(inv, m)
		if reason != "" {
			skipped = append(skipped, excluded{name: vm.Name, reason: reason})
			continue
		}
		out = append(out, member)
	}
	// ARM's list order is not contractual; sorting keeps a stable snapshot
	// so the Emitter's no-change suppression is not defeated by ordering
	slices.SortFunc(out, func(a, b discovery.Member) int {
		return strings.Compare(a.Address, b.Address)
	})
	return out, skipped
}

// hasAllTags reports whether the VM carries every required tag name. Tag
// names are matched case-insensitively, as Azure treats them.
func (vm *virtualMachine) hasAllTags(required []string) bool {
	if len(required) == 0 {
		return true
	}
	for _, want := range required {
		var found bool
		for name := range vm.Tags {
			if strings.EqualFold(name, want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// isRunning reports whether the VM's power state is running
func (vm *virtualMachine) isRunning(states map[string]string) bool {
	return states[resourceKey(vm.ID)] == powerStateRunning
}

// powerStateOf extracts the power state from an instance view, which
// carries provisioning statuses alongside it
func powerStateOf(iv *instanceView) string {
	if iv == nil {
		return ""
	}
	for _, s := range iv.Statuses {
		if strings.HasPrefix(s.Code, powerStatePrefix) {
			return s.Code
		}
	}
	return ""
}

// toMember maps one VM, returning the reason it could not become a member
func (vm *virtualMachine) toMember(inv *inventory, m mapping) (discovery.Member, string) {
	addr, reason := vm.address(inv, m.addressType)
	if reason != "" {
		return discovery.Member{}, reason
	}
	port, reason := vm.resolvePort(m)
	if reason != "" {
		return discovery.Member{}, reason
	}
	member := discovery.Member{
		Name:    vm.Name,
		Address: net.JoinHostPort(addr, strconv.Itoa(port)),
		Scheme:  m.scheme,
		Ready:   vm.ready(inv, m),
		Labels:  vm.memberLabels(inv, m),
	}
	if m.replicaGroupLabel != "" {
		member.ReplicaGroup = vm.tag(m.replicaGroupLabel)
	}
	return member, ""
}

// address resolves the VM's address through its network interfaces.
func (vm *virtualMachine) address(inv *inventory, addressType string) (string, string) {
	nics := vm.Properties.NetworkProfile.NetworkInterfaces
	if len(nics) == 0 {
		return "", "vm has no network interface"
	}
	// a VM with several NICs marks one primary; with exactly one, Azure
	// commonly omits the flag entirely, so a lone NIC is the primary
	ordered := make([]nicRef, len(nics))
	copy(ordered, nics)
	slices.SortStableFunc(ordered, func(a, b nicRef) int {
		switch {
		case a.Properties.Primary == b.Properties.Primary:
			return 0
		case a.Properties.Primary:
			return -1
		default:
			return 1
		}
	})
	var unresolved int
	for _, ref := range ordered {
		nic, ok := inv.nics[resourceKey(ref.ID)]
		if !ok {
			unresolved++
			continue
		}
		if addr := addressOfNIC(nic, inv, addressType); addr != "" {
			return addr, ""
		}
	}
	if unresolved == len(ordered) {
		// every NIC reference dangled. This is the case the case-folding
		// join exists to prevent, so it is called out specifically rather
		// than reported as a missing address.
		return "", "none of the vm's network interfaces resolved in the " +
			"interface list; it may be in another subscription"
	}
	if addressType == do.AddressPublic {
		return "", "vm has no public ip address"
	}
	return "", "vm has no private ip address"
}

// addressOfNIC returns the requested address from a NIC's ip
// configurations, preferring the primary configuration.
func addressOfNIC(nic *networkInterface, inv *inventory, addressType string) string {
	confs := nic.Properties.IPConfigurations
	ordered := make([]ipConfiguration, len(confs))
	copy(ordered, confs)
	slices.SortStableFunc(ordered, func(a, b ipConfiguration) int {
		switch {
		case a.Properties.Primary == b.Properties.Primary:
			return 0
		case a.Properties.Primary:
			return -1
		default:
			return 1
		}
	})
	for _, c := range ordered {
		if addressType == do.AddressPublic {
			if c.Properties.PublicIPAddress == nil {
				continue
			}
			pip, ok := inv.publicIPs[resourceKey(c.Properties.PublicIPAddress.ID)]
			if ok && pip.Properties.IPAddress != "" {
				return pip.Properties.IPAddress
			}
			continue
		}
		if c.Properties.PrivateIPAddress != "" {
			return c.Properties.PrivateIPAddress
		}
	}
	return ""
}

// resolvePort determines the member's port: a tag first, then the static
// port. Azure returns hosts rather than endpoints, so one of the two is
// required; Query validation enforces that.
func (vm *virtualMachine) resolvePort(m mapping) (int, string) {
	if m.portLabel != "" {
		if v := vm.tag(m.portLabel); v != "" {
			port, err := strconv.Atoi(v)
			if err != nil || port < 1 || port > 65535 {
				return 0, fmt.Sprintf("tag %s=%q is not a valid port",
					m.portLabel, v)
			}
			return port, ""
		}
	}
	if m.port != "" {
		port, err := strconv.Atoi(m.port)
		if err != nil || port < 1 || port > 65535 {
			return 0, "configured 'port' is not a valid port"
		}
		return port, ""
	}
	return 0, "no port: set 'port', or 'port_label' naming a tag on the vm"
}

// tag reads a VM tag case-insensitively, as Azure treats tag names
func (vm *virtualMachine) tag(name string) string {
	if v, ok := vm.Tags[name]; ok {
		return v
	}
	for k, v := range vm.Tags {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// ready maps power state onto readiness.
//
// Without the instance view there is nothing to map: the VM list carries
// provisioning state, not power state, and a provisioned VM may be
// stopped. Reporting Ready on that basis would assert something Azure
// never said, so readiness is unknown unless power_state was requested.
func (vm *virtualMachine) ready(inv *inventory, m mapping) discovery.ReadyState {
	if !m.powerState {
		return discovery.ReadyUnknown
	}
	if vm.isRunning(inv.states) {
		return discovery.Ready
	}
	return discovery.NotReady
}

// memberLabels builds the member's label set. VM tags are carried under a
// tag_ prefix so a user-defined tag cannot shadow a Trickster-assigned
// label.
func (vm *virtualMachine) memberLabels(inv *inventory, m mapping) map[string]string {
	out := make(map[string]string, len(vm.Tags)+8)
	setIf(out, "vm_name", vm.Name)
	setIf(out, "vm_id", vm.Properties.VMID)
	setIf(out, "location", vm.Location)
	setIf(out, "vm_size", vm.Properties.HardwareProfile.VMSize)
	setIf(out, "resource_group", resourceGroupOf(vm.ID))
	setIf(out, "private_ip", vm.addressOrEmpty(inv, do.AddressPrivate))
	setIf(out, "public_ip", vm.addressOrEmpty(inv, do.AddressPublic))
	if m.powerState {
		setIf(out, "power_state",
			strings.TrimPrefix(inv.states[resourceKey(vm.ID)], powerStatePrefix))
	}
	for k, v := range vm.Tags {
		setIf(out, "tag_"+k, v)
	}
	return out
}

// addressOrEmpty resolves an address for labeling, where absence is
// ordinary rather than an error
func (vm *virtualMachine) addressOrEmpty(inv *inventory, addressType string) string {
	addr, _ := vm.address(inv, addressType)
	return addr
}

// resourceGroupOf extracts the resource group from an ARM resource id,
// which has the form
// /subscriptions/{s}/resourceGroups/{g}/providers/...
func resourceGroupOf(id string) string {
	parts := strings.Split(id, "/")
	for i, p := range parts {
		if strings.EqualFold(p, "resourceGroups") && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// setIf assigns only non-empty values, so absent fields are missing
// labels rather than blank ones
func setIf(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}
