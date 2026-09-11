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

package gcp

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

// GCE instance statuses. Compute Engine documents these as a closed set.
const (
	statusProvisioning = "PROVISIONING"
	statusStaging      = "STAGING"
	statusRunning      = "RUNNING"
	statusStopping     = "STOPPING"
	statusSuspending   = "SUSPENDING"
	statusSuspended    = "SUSPENDED"
	statusRepairing    = "REPAIRING"
	statusTerminated   = "TERMINATED"
)

// aggregatedList is the instances.aggregatedList response. Its items map is
// keyed by "zones/<zone>", and a zone with no matching instances appears
// with a warning rather than an empty list.
type aggregatedList struct {
	Items         map[string]zoneInstances `json:"items"`
	NextPageToken string                   `json:"nextPageToken"`
}

type zoneInstances struct {
	Instances []gceInstance `json:"instances"`
}

// gceInstance is one Compute Engine instance. Only the fields Trickster
// maps are declared; the full resource is large, and decoding all of it
// would make every schema addition upstream a potential incompatibility.
type gceInstance struct {
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	Status            string             `json:"status"`
	Zone              string             `json:"zone"`
	MachineType       string             `json:"machineType"`
	NetworkInterfaces []networkInterface `json:"networkInterfaces"`
	Labels            map[string]string  `json:"labels"`
	Tags              instanceTags       `json:"tags"`
	Metadata          instanceMetadata   `json:"metadata"`
}

type networkInterface struct {
	Network       string         `json:"network"`
	Subnetwork    string         `json:"subnetwork"`
	NetworkIP     string         `json:"networkIP"`
	IPv6Address   string         `json:"ipv6Address"`
	AccessConfigs []accessConfig `json:"accessConfigs"`
	// IPv6AccessConfigs carries the external IPv6, where IPv6Address is the
	// internal one
	IPv6AccessConfigs []ipv6AccessConfig `json:"ipv6AccessConfigs"`
}

type accessConfig struct {
	Type  string `json:"type"`
	NatIP string `json:"natIP"`
}

type ipv6AccessConfig struct {
	ExternalIPv6 string `json:"externalIpv6"`
}

// instanceTags are GCE network tags: a list of names with no values, used
// for firewall targeting.
type instanceTags struct {
	Items []string `json:"items"`
}

// instanceMetadata is the instance's key/value metadata.
type instanceMetadata struct {
	Items []metadataItem `json:"items"`
}

type metadataItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// errorResponse is the Google API error document, which carries a more
// useful message than the status code alone.
type errorResponse struct {
	Error apiErrorDetail `json:"error"`
}

// apiErrorDetail is the body of a Google API error.
type apiErrorDetail struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// apiError renders a Google API error document.
func apiError(status int, payload []byte) error {
	var e errorResponse
	if err := json.Unmarshal(payload, &e); err == nil && e.Error.Message != "" {
		if e.Error.Status != "" {
			return fmt.Errorf("GCE API error (http %d): %s: %s",
				status, e.Error.Status, e.Error.Message)
		}
		return fmt.Errorf("GCE API error (http %d): %s", status, e.Error.Message)
	}
	return fmt.Errorf("GCE API returned http %d", status)
}

// label returns the value of the named instance label, falling back to
// instance metadata.
//
// Both are key/value namespaces on the instance and either is a reasonable
// home for something like a port. Labels are checked first because they are
// the idiomatic place for selection attributes; metadata is the fallback so
// that deployments already carrying the value there do not have to move it.
func (i *gceInstance) label(key string) string {
	if v, ok := i.Labels[key]; ok && v != "" {
		return v
	}
	for _, m := range i.Metadata.Items {
		if m.Key == key && m.Value != "" {
			return m.Value
		}
	}
	return ""
}

// address returns the instance address matching the requested type.
func (i *gceInstance) address(addressType string) string {
	for _, ni := range i.NetworkInterfaces {
		switch addressType {
		case do.AddressPublic:
			for _, ac := range ni.AccessConfigs {
				if ac.NatIP != "" {
					return ac.NatIP
				}
			}
		case do.AddressIPv6:
			for _, ac := range ni.IPv6AccessConfigs {
				if ac.ExternalIPv6 != "" {
					return ac.ExternalIPv6
				}
			}
			if ni.IPv6Address != "" {
				return ni.IPv6Address
			}
		default:
			if ni.NetworkIP != "" {
				return ni.NetworkIP
			}
		}
	}
	return ""
}

// hasAllTags reports whether the instance carries every required network tag.
func (i *gceInstance) hasAllTags(required []string) bool {
	for _, tag := range required {
		if !slices.Contains(i.Tags.Items, tag) {
			return false
		}
	}
	return true
}

// readyFor maps an instance status onto member readiness.
//
// Statuses meaning the instance is going away are not mapped here: they are
// omitted from the snapshot entirely, so members drain from pools before
// they stop answering.
func readyFor(status string) discovery.ReadyState {
	if status == statusRunning {
		return discovery.Ready
	}
	// PROVISIONING and STAGING are not serving yet, REPAIRING is not
	// serving now, and a status a future Compute Engine release adds is not
	// assumed healthy either
	return discovery.NotReady
}

// isDeparting reports whether an instance should be omitted from the
// snapshot rather than reported not-ready.
func isDeparting(status string) bool {
	switch status {
	case statusStopping, statusSuspending, statusSuspended, statusTerminated:
		return true
	default:
		return false
	}
}

// mapping carries the query-derived choices that turn instances into
// members.
type mapping struct {
	scheme            string
	addressType       string
	port              string
	portLabel         string
	replicaGroupLabel string
	tags              []string
}

// excluded records an instance that could not become a member, so the
// provider can report it rather than silently shrinking the pool.
type excluded struct {
	name   string
	reason string
}

// toMembers maps decoded instances onto members, returning both the members
// and the instances that could not become one.
//
// As with EC2, an unusable instance is excluded rather than fatal: a
// compute inventory routinely contains hosts that are simply not labeled
// yet, and failing the whole refresh would drain a working pool because of
// one unrelated instance.
func toMembers(instances []gceInstance, m mapping) (discovery.Snapshot, []excluded) {
	out := make(discovery.Snapshot, 0, len(instances))
	var skipped []excluded
	for i := range instances {
		inst := &instances[i]
		if isDeparting(inst.Status) {
			continue
		}
		if !inst.hasAllTags(m.tags) {
			continue
		}
		member, reason := inst.toMember(m)
		if reason != "" {
			skipped = append(skipped, excluded{inst.Name, reason})
			continue
		}
		out = append(out, member)
	}
	return out, skipped
}

// toMember maps one instance onto a member, or returns why it cannot.
func (i *gceInstance) toMember(m mapping) (discovery.Member, string) {
	address := i.address(m.addressType)
	if address == "" {
		return discovery.Member{}, "no " + m.addressType + " address"
	}
	port := m.port
	if m.portLabel != "" {
		if v := i.label(m.portLabel); v != "" {
			port = v
		}
	}
	if port == "" {
		return discovery.Member{}, "no port: label or metadata key " + m.portLabel +
			" is absent and no static port is configured"
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return discovery.Member{}, "port " + port + " is not a port number"
	}
	name := i.Name
	if name == "" {
		name = i.ID
	}
	var replicaGroup string
	if m.replicaGroupLabel != "" {
		replicaGroup = i.label(m.replicaGroupLabel)
	}
	return discovery.Member{
		Name:         name,
		Scheme:       m.scheme,
		Address:      net.JoinHostPort(address, port),
		ReplicaGroup: replicaGroup,
		Ready:        readyFor(i.Status),
		Labels:       i.memberLabels(),
	}, ""
}

// memberLabels carries instance metadata onto the member for observability.
// Instance labels and network tags are prefixed so a user-defined key
// cannot shadow a Trickster-assigned one.
func (i *gceInstance) memberLabels() map[string]string {
	out := map[string]string{
		"instance_id":   i.ID,
		"instance_name": i.Name,
		"status":        i.Status,
		"zone":          lastSegment(i.Zone),
		"machine_type":  lastSegment(i.MachineType),
	}
	if len(i.NetworkInterfaces) > 0 {
		ni := i.NetworkInterfaces[0]
		out["private_ip"] = ni.NetworkIP
		out["network"] = lastSegment(ni.Network)
		out["subnetwork"] = lastSegment(ni.Subnetwork)
		for _, ac := range ni.AccessConfigs {
			if ac.NatIP != "" {
				out["public_ip"] = ac.NatIP
				break
			}
		}
	}
	if len(i.Tags.Items) > 0 {
		// bracketing separators make a tag match unambiguous for anything
		// that later filters on this string
		out["tags"] = "," + strings.Join(i.Tags.Items, ",") + ","
	}
	for k, v := range i.Labels {
		out["label_"+k] = v
	}
	for k, v := range out {
		if v == "" {
			delete(out, k)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// lastSegment returns the final path element of a GCE resource URL, which
// is the part an operator reads: a full selfLink is a URL, not a name.
func lastSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// sortedZones returns the aggregated map's zone keys in a deterministic
// order, so that a snapshot's member order does not depend on Go's map
// iteration and successive identical responses compare equal.
func sortedZones(items map[string]zoneInstances) []string {
	out := make([]string, 0, len(items))
	for k := range items {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// parseAggregatedList decodes one instances.aggregatedList page.
func parseAggregatedList(body []byte) (*aggregatedList, error) {
	var out aggregatedList
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding instances.aggregatedList response: %w", err)
	}
	return &out, nil
}

// instancesOf flattens an aggregated page's per-zone map. Zones with no
// matching instances appear in the map carrying a warning rather than an
// empty list, and simply contribute nothing.
func (a *aggregatedList) instancesOf() []gceInstance {
	var out []gceInstance
	for _, zone := range sortedZones(a.Items) {
		out = append(out, a.Items[zone].Instances...)
	}
	return out
}
