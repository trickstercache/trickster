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

package docker

import (
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
)

// Container states reported by the Engine API. Only 'running' can serve
// traffic; the rest are omitted from membership entirely rather than
// carried as NotReady, so containers drain from pools as they stop.
const (
	stateRunning = "running"
)

// tcp is the only port type this provider considers. A UDP port cannot
// serve an HTTP backend, and excluding it is what makes automatic port
// resolution usable in practice: a container publishing one TCP port
// beside two UDP ports is unambiguous once UDP is out of the picture.
const protoTCP = "tcp"

// localhost is the address a wildcard host binding resolves to
const localhost = "127.0.0.1"

// Health substrings as they appear in the Engine API's Status field.
//
// The list endpoint reports health nowhere else. GET /containers/json
// carries no Health object -- that lives only on the per-container
// inspect, which would make readiness cost one request per container per
// poll. The Status string is what the list gives, and these three
// substrings are what Docker writes into it.
const (
	statusHealthy   = "(healthy)"
	statusUnhealthy = "(unhealthy)"
	statusStarting  = "(health: starting)"
)

// container is one entry of GET /containers/json. Only the fields
// Trickster maps are declared; the endpoint returns a good deal more.
type container struct {
	ID              string            `json:"Id"`
	Names           []string          `json:"Names"`
	Image           string            `json:"Image"`
	State           string            `json:"State"`
	Status          string            `json:"Status"`
	Labels          map[string]string `json:"Labels"`
	Ports           []containerPort   `json:"Ports"`
	NetworkSettings *networkSettings  `json:"NetworkSettings"`
}

// containerPort is one published or exposed port. PublicPort is absent
// when the port is not published to the host.
type containerPort struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

// networkSettings carries the container's per-network attachments
type networkSettings struct {
	Networks map[string]*networkEndpoint `json:"Networks"`
}

// networkEndpoint is the container's attachment to one network
type networkEndpoint struct {
	NetworkID         string `json:"NetworkID"`
	IPAddress         string `json:"IPAddress"`
	GlobalIPv6Address string `json:"GlobalIPv6Address"`
}

// mapping is the per-query state the member mapping needs
type mapping struct {
	scheme            string
	network           string
	addressType       string
	port              string
	portLabel         string
	replicaGroupLabel string
}

// excluded records a container that could not become a member, and why
type excluded struct {
	name   string
	reason string
}

// parseContainers decodes a GET /containers/json response
func parseContainers(body []byte) ([]container, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, nil
	}
	var out []container
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, fmt.Errorf("decoding containers response: %w", err)
	}
	return out, nil
}

// toMembers maps containers to members, returning the members and the
// containers that could not become one. A container that cannot yield an
// address or a port is excluded and reported rather than failing the
// refresh: a container inventory routinely holds things that are simply
// not labeled for discovery.
func toMembers(cs []container, m mapping) (discovery.Snapshot, []excluded) {
	out := make(discovery.Snapshot, 0, len(cs))
	var skipped []excluded
	for i := range cs {
		c := &cs[i]
		if c.State != stateRunning {
			// not an exclusion worth reporting: a stopped container is
			// ordinary, and reporting each one would bury the real
			// misconfigurations in noise
			continue
		}
		member, reason := c.toMember(m)
		if reason != "" {
			skipped = append(skipped, excluded{name: c.name(), reason: reason})
			continue
		}
		out = append(out, member)
	}
	// containers arrive newest-first, which reorders as containers are
	// replaced; sorting keeps a stable snapshot so the Emitter's
	// no-change suppression is not defeated by ordering alone
	slices.SortFunc(out, func(a, b discovery.Member) int {
		return strings.Compare(a.Address, b.Address)
	})
	return out, skipped
}

// name returns the container's primary name without the API's leading
// slash, falling back to a short ID when it is unnamed.
func (c *container) name() string {
	if len(c.Names) > 0 && c.Names[0] != "" {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return shortID(c.ID)
}

// toMember maps one running container, returning the reason it could not
// become a member when it cannot.
func (c *container) toMember(m mapping) (discovery.Member, string) {
	host, port, reason := c.hostPort(m)
	if reason != "" {
		return discovery.Member{}, reason
	}
	member := discovery.Member{
		Name:    c.name(),
		Address: net.JoinHostPort(host, strconv.Itoa(port)),
		Scheme:  m.scheme,
		Ready:   c.ready(),
		Labels:  c.memberLabels(m),
	}
	if m.replicaGroupLabel != "" {
		member.ReplicaGroup = c.Labels[m.replicaGroupLabel]
	}
	return member, ""
}

// hostPort resolves the member's address and port together.
//
// They are resolved as a pair rather than separately because for a
// published port they are one fact: a container publishing 9090 on
// 127.0.0.1 and 3000 on 0.0.0.0 has two bindings, and pairing either
// host with the other's port yields an address that does not exist.
func (c *container) hostPort(m mapping) (string, int, string) {
	want, reason := c.configuredPort(m)
	if reason != "" {
		return "", 0, reason
	}
	if m.addressType == do.AddressPublic {
		return c.publishedHostPort(want)
	}
	ep, reason := c.endpoint(m.network)
	if reason != "" {
		return "", 0, reason
	}
	host := ep.IPAddress
	if m.addressType == do.AddressIPv6 {
		host = ep.GlobalIPv6Address
	}
	if host == "" {
		return "", 0, "container has no " + addressKind(m.addressType) +
			" address on network " + c.networkName(m.network)
	}
	if want != 0 {
		return host, want, ""
	}
	// no port configured: the container's own exposed port serves, when
	// exactly one is a candidate
	port, reason := c.solePort(false)
	if reason != "" {
		return "", 0, reason
	}
	return host, port, ""
}

// publishedHostPort resolves a host binding. When a port was configured
// the binding that publishes it supplies the host; otherwise the
// container's single published port supplies both.
func (c *container) publishedHostPort(want int) (string, int, string) {
	if want == 0 {
		port, reason := c.solePort(true)
		if reason != "" {
			return "", 0, reason
		}
		want = port
	}
	for _, p := range c.Ports {
		if p.Type != protoTCP || p.PublicPort != want {
			continue
		}
		return hostOf(p.IP), want, ""
	}
	// the operator named a port the container does not publish. Their
	// instruction still stands -- they may be reaching it by another
	// route -- but the host can only be the loopback default.
	return localhost, want, ""
}

// hostOf normalizes a binding's host address. Docker reports the wildcard
// bindings 0.0.0.0 and :: for a port published on every interface;
// neither is dialable, so both become the loopback address, which is what
// a caller on the host actually uses.
func hostOf(ip string) string {
	switch ip {
	case "", "0.0.0.0", "::":
		return localhost
	}
	return ip
}

// addressKind names an address type for an operator-facing message
func addressKind(addressType string) string {
	if addressType == do.AddressIPv6 {
		return "global IPv6"
	}
	return "IP"
}

// networkName reports the network the mapping selected, for a message
func (c *container) networkName(network string) string {
	if network != "" {
		return network
	}
	return "(its only network)"
}

// endpoint returns the container's attachment to the selected network.
// With no network configured a container on exactly one network resolves
// unambiguously; one on several must be told which, rather than having a
// map iteration pick for it.
func (c *container) endpoint(network string) (*networkEndpoint, string) {
	if c.NetworkSettings == nil || len(c.NetworkSettings.Networks) == 0 {
		return nil, "container is attached to no network"
	}
	nets := c.NetworkSettings.Networks
	if network != "" {
		ep, ok := nets[network]
		if !ok || ep == nil {
			return nil, "container is not attached to network " + network
		}
		return ep, ""
	}
	if len(nets) > 1 {
		names := slices.Sorted(maps.Keys(nets))
		return nil, "container is on several networks (" +
			strings.Join(names, ", ") + "); set 'network' to choose one"
	}
	for _, ep := range nets {
		if ep == nil {
			return nil, "container has no endpoint on its network"
		}
		return ep, ""
	}
	return nil, "container is attached to no network"
}

// configuredPort returns the port the query names, by label first and
// then statically, or zero when it names none.
//
// Unlike the cloud providers, docker is not required to be told a port.
// The Engine API returns endpoints rather than bare hosts, so asking an
// operator to restate a port the daemon already reported would be
// make-work. Ambiguity is still refused rather than guessed.
func (c *container) configuredPort(m mapping) (int, string) {
	if m.portLabel != "" {
		if v := c.Labels[m.portLabel]; v != "" {
			port, err := strconv.Atoi(v)
			if err != nil || !validPort(port) {
				return 0, fmt.Sprintf("label %s=%q is not a valid port",
					m.portLabel, v)
			}
			return port, ""
		}
	}
	if m.port != "" {
		port, err := strconv.Atoi(m.port)
		if err != nil || !validPort(port) {
			return 0, "configured 'port' is not a valid port"
		}
		return port, ""
	}
	return 0, ""
}

// solePort returns the container's only candidate TCP port, or the reason
// it has none or several.
func (c *container) solePort(public bool) (int, string) {
	seen := make([]int, 0, len(c.Ports))
	for _, p := range c.Ports {
		if p.Type != protoTCP {
			continue
		}
		port := p.PrivatePort
		if public {
			port = p.PublicPort
		}
		// the same port is reported once per host interface (0.0.0.0 and
		// ::), so the list is deduplicated rather than counted
		if port != 0 && !slices.Contains(seen, port) {
			seen = append(seen, port)
		}
	}
	switch len(seen) {
	case 0:
		if public {
			return 0, "container publishes no tcp port to the host"
		}
		return 0, "container exposes no tcp port"
	case 1:
		return seen[0], ""
	}
	slices.Sort(seen)
	return 0, fmt.Sprintf(
		"container has %d candidate tcp ports (%s); set 'port' or 'port_label'",
		len(seen), joinInts(seen))
}

// ready maps the container's health onto readiness.
//
// A container with no HEALTHCHECK reports no health at all, which is
// ReadyUnknown rather than Ready: the daemon knows the process started,
// not that it is serving. Trickster's own health checks cover that case,
// and claiming Ready here would assert something Docker never said.
func (c *container) ready() discovery.ReadyState {
	switch {
	case strings.Contains(c.Status, statusHealthy):
		return discovery.Ready
	case strings.Contains(c.Status, statusUnhealthy),
		strings.Contains(c.Status, statusStarting):
		return discovery.NotReady
	}
	return discovery.ReadyUnknown
}

// memberLabels builds the member's label set. Container labels are
// carried under a label_ prefix so that a user-defined label cannot
// shadow a Trickster-assigned one.
func (c *container) memberLabels(m mapping) map[string]string {
	out := make(map[string]string, len(c.Labels)+6)
	setIf(out, "container_id", shortID(c.ID))
	setIf(out, "container_name", c.name())
	setIf(out, "image", c.Image)
	setIf(out, "state", c.State)
	if ep, reason := c.endpoint(m.network); reason == "" {
		setIf(out, "private_ip", ep.IPAddress)
	}
	if c.NetworkSettings != nil && len(c.NetworkSettings.Networks) > 0 {
		if m.network != "" {
			setIf(out, "network", m.network)
		} else {
			names := slices.Sorted(maps.Keys(c.NetworkSettings.Networks))
			setIf(out, "network", names[0])
		}
	}
	for k, v := range c.Labels {
		setIf(out, "label_"+k, v)
	}
	return out
}

// shortID abbreviates a container ID the way Docker's own tooling does
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// setIf assigns only non-empty values, so absent fields are missing
// labels rather than blank ones
func setIf(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// validPort reports whether n is a usable TCP port number
func validPort(n int) bool { return n > 0 && n < 65536 }

// joinInts renders a port list for an operator-facing message
func joinInts(ns []int) string {
	var sb strings.Builder
	for i, n := range ns {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(strconv.Itoa(n))
	}
	return sb.String()
}
