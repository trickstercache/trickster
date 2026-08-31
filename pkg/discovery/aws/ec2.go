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

package aws

import (
	"encoding/xml"
	"fmt"
	"net"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
)

// ec2APIVersion is the DescribeInstances API version whose response shape
// this package decodes. It is pinned deliberately: the Query protocol's
// response schema is versioned, and letting it float would mean an AWS
// release could silently change the document under us.
const ec2APIVersion = "2016-11-15"

// EC2 instance states. AWS documents these as a closed set.
const (
	stateRunning      = "running"
	statePending      = "pending"
	stateShuttingDown = "shutting-down"
	stateStopping     = "stopping"
	stateStopped      = "stopped"
	stateTerminated   = "terminated"
)

// describeInstancesResponse is the DescribeInstances Query-protocol
// document. Only the fields Trickster maps are declared; EC2's response
// carries a great deal more, and decoding it all would make every schema
// addition upstream a potential incompatibility here.
//
// Note the doubly-nested shape: instances are grouped into reservations,
// and both levels use EC2's `<xxxSet><item>` wrapper convention.
type describeInstancesResponse struct {
	XMLName      xml.Name      `xml:"DescribeInstancesResponse"`
	Reservations []reservation `xml:"reservationSet>item"`
	NextToken    string        `xml:"nextToken"`
}

type reservation struct {
	Instances []ec2Instance `xml:"instancesSet>item"`
}

type ec2Instance struct {
	InstanceID       string           `xml:"instanceId"`
	InstanceType     string           `xml:"instanceType"`
	ImageID          string           `xml:"imageId"`
	State            ec2InstanceState `xml:"instanceState"`
	PrivateIPAddress string           `xml:"privateIpAddress"`
	// PublicIPAddress is `ipAddress` in the wire document, not
	// `publicIpAddress` as the parameter name might suggest
	PublicIPAddress  string                `xml:"ipAddress"`
	PrivateDNSName   string                `xml:"privateDnsName"`
	PublicDNSName    string                `xml:"dnsName"`
	VPCID            string                `xml:"vpcId"`
	SubnetID         string                `xml:"subnetId"`
	AvailabilityZone string                `xml:"placement>availabilityZone"`
	Architecture     string                `xml:"architecture"`
	Tags             []ec2Tag              `xml:"tagSet>item"`
	NetworkInterface []ec2NetworkInterface `xml:"networkInterfaceSet>item"`
}

// ec2InstanceState is the instance's lifecycle state.
type ec2InstanceState struct {
	Name string `xml:"name"`
}

type ec2Tag struct {
	Key   string `xml:"key"`
	Value string `xml:"value"`
}

type ec2NetworkInterface struct {
	IPv6Addresses []ec2IPv6Address `xml:"ipv6AddressesSet>item"`
}

// ec2IPv6Address is one IPv6 address on a network interface. An instance's
// IPv6 addresses live here rather than on the instance itself.
type ec2IPv6Address struct {
	Address string `xml:"ipv6Address"`
}

// ec2ErrorResponse is the Query-protocol error document, which carries a
// far more useful message than the status code alone.
type ec2ErrorResponse struct {
	XMLName   xml.Name   `xml:"Response"`
	Errors    []ec2Error `xml:"Errors>Error"`
	RequestID string     `xml:"RequestID"`
}

// ec2Error is one error in a Query-protocol error document.
type ec2Error struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// Error renders the first error AWS reported.
func (e *ec2ErrorResponse) Error() string {
	if len(e.Errors) == 0 {
		return "unspecified EC2 API error"
	}
	return e.Errors[0].Code + ": " + e.Errors[0].Message
}

// tag returns the value of the named instance tag.
func (i *ec2Instance) tag(key string) string {
	for _, t := range i.Tags {
		if t.Key == key {
			return t.Value
		}
	}
	return ""
}

// ipv6 returns the instance's first IPv6 address, which lives on a network
// interface rather than on the instance itself.
func (i *ec2Instance) ipv6() string {
	for _, ni := range i.NetworkInterface {
		for _, a := range ni.IPv6Addresses {
			if a.Address != "" {
				return a.Address
			}
		}
	}
	return ""
}

// address returns the instance address matching the requested type.
func (i *ec2Instance) address(addressType string) string {
	switch addressType {
	case do.AddressPublic:
		return i.PublicIPAddress
	case do.AddressIPv6:
		return i.ipv6()
	default:
		return i.PrivateIPAddress
	}
}

// readyFor maps an instance state onto member readiness.
//
// States that mean the instance is going away are not mapped here at all:
// they are omitted from the snapshot entirely, so members drain out of
// pools before they stop answering, which is the same rule the kubernetes
// provider applies to terminating endpoints.
func readyFor(state string) discovery.ReadyState {
	if state == stateRunning {
		return discovery.Ready
	}
	// pending instances are not ready yet, and an unrecognized state is not
	// assumed healthy either, so an EC2 release that adds one cannot
	// silently put unusable members into pools
	return discovery.NotReady
}

// isDeparting reports whether an instance should be omitted from the
// snapshot rather than reported as not-ready.
func isDeparting(state string) bool {
	switch state {
	case stateShuttingDown, stateStopping, stateStopped, stateTerminated:
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
	// tags, when non-empty, keeps only instances carrying every listed tag
	// key. Value matching belongs in filters, which EC2 evaluates
	// server-side.
	tags []string
}

// excluded records an instance that could not become a member, so the
// provider can report it rather than silently shrinking the pool.
type excluded struct {
	instanceID string
	reason     string
}

// toMembers maps a decoded response onto members, returning both the
// members and the instances that could not become one.
//
// Unlike the service-registry providers, an unusable instance here is not a
// parse failure. Consul and Nomad return a registry, where every entry is
// by definition an instance of the service, so a malformed one means the
// API is broken. EC2 returns an inventory: a filter selects a set of hosts,
// and one of them lacking a port tag or a public address is an ordinary
// operational mistake -- someone launched an instance without tagging it.
// Failing the whole refresh for that would drain a working pool because of
// one unrelated instance.
func toMembers(instances []ec2Instance, m mapping) (discovery.Snapshot, []excluded) {
	out := make(discovery.Snapshot, 0, len(instances))
	var skipped []excluded
	for i := range instances {
		inst := &instances[i]
		if isDeparting(inst.State.Name) {
			continue
		}
		if !inst.hasAllTags(m.tags) {
			continue
		}
		member, reason := inst.toMember(m)
		if reason != "" {
			skipped = append(skipped, excluded{inst.InstanceID, reason})
			continue
		}
		out = append(out, member)
	}
	return out, skipped
}

// hasAllTags reports whether the instance carries every required tag key.
func (i *ec2Instance) hasAllTags(required []string) bool {
	for _, key := range required {
		if i.tag(key) == "" {
			return false
		}
	}
	return true
}

// toMember maps one instance onto a member, or returns why it cannot.
func (i *ec2Instance) toMember(m mapping) (discovery.Member, string) {
	address := i.address(m.addressType)
	if address == "" {
		return discovery.Member{}, "no " + m.addressType + " address"
	}
	port := m.port
	if m.portLabel != "" {
		if v := i.tag(m.portLabel); v != "" {
			port = v
		}
	}
	if port == "" {
		return discovery.Member{}, "no port: tag " + m.portLabel + " is absent and no static port is configured"
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return discovery.Member{}, "port " + port + " is not a port number"
	}
	name := i.tag("Name")
	if name == "" {
		name = i.InstanceID
	}
	var replicaGroup string
	if m.replicaGroupLabel != "" {
		replicaGroup = i.tag(m.replicaGroupLabel)
	}
	return discovery.Member{
		Name:         name,
		Scheme:       m.scheme,
		Address:      net.JoinHostPort(address, port),
		ReplicaGroup: replicaGroup,
		Ready:        readyFor(i.State.Name),
		Labels:       i.labels(),
	}, ""
}

// labels carries instance metadata onto the member for observability.
// Instance tags are prefixed so an operator-defined tag cannot shadow a
// Trickster-assigned label.
func (i *ec2Instance) labels() map[string]string {
	out := map[string]string{
		"instance_id":       i.InstanceID,
		"instance_type":     i.InstanceType,
		"instance_state":    i.State.Name,
		"image_id":          i.ImageID,
		"availability_zone": i.AvailabilityZone,
		"vpc_id":            i.VPCID,
		"subnet_id":         i.SubnetID,
		"architecture":      i.Architecture,
		"private_ip":        i.PrivateIPAddress,
		"public_ip":         i.PublicIPAddress,
		"private_dns":       i.PrivateDNSName,
		"public_dns":        i.PublicDNSName,
	}
	for _, t := range i.Tags {
		if t.Key != "" {
			out["tag_"+t.Key] = t.Value
		}
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

// parseDescribeInstances decodes one DescribeInstances response page.
func parseDescribeInstances(body []byte) (*describeInstancesResponse, error) {
	var out describeInstancesResponse
	if err := xml.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding DescribeInstances response: %w", err)
	}
	return &out, nil
}
