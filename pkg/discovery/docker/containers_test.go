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
	"os"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
)

func defaultMapping() mapping { return mapping{scheme: "http"} }

// ctr builds a running container on one network with the given tcp ports
func ctr(name, ip string, ports ...containerPort) container {
	return container{
		ID:     "0123456789abcdef0123456789abcdef",
		Names:  []string{"/" + name},
		Image:  "prom/prometheus:v3.13.2",
		State:  stateRunning,
		Status: "Up 2 days",
		Ports:  ports,
		NetworkSettings: &networkSettings{
			Networks: map[string]*networkEndpoint{
				"bridge": {IPAddress: ip},
			},
		},
	}
}

func tcpPort(private, public int) containerPort {
	return containerPort{PrivatePort: private, PublicPort: public, Type: protoTCP}
}

// A container exposing exactly one TCP port needs no configured port: the
// Engine API reports endpoints, not bare hosts, so restating a port the
// daemon already gave would be make-work.
func TestSoleExposedPortNeedsNoConfig(t *testing.T) {
	snap, skipped := toMembers(
		[]container{ctr("prom", "172.18.0.2", tcpPort(9090, 0))},
		defaultMapping())
	require.Empty(t, skipped)
	require.Len(t, snap, 1)
	require.Equal(t, "172.18.0.2:9090", snap[0].Address)
}

// UDP is never a candidate. This is what makes automatic resolution
// usable: telegraf exposes three ports of which only one is tcp.
func TestUDPPortsAreNotCandidates(t *testing.T) {
	c := ctr("telegraf", "172.18.0.3",
		containerPort{PrivatePort: 8092, Type: "udp"},
		tcpPort(8094, 0),
		containerPort{PrivatePort: 8125, Type: "udp"},
	)
	snap, skipped := toMembers([]container{c}, defaultMapping())
	require.Empty(t, skipped)
	require.Len(t, snap, 1)
	require.Equal(t, "172.18.0.3:8094", snap[0].Address)
}

// Several candidates is an exclusion with a reason, never a guess.
func TestAmbiguousPortIsExcludedNotGuessed(t *testing.T) {
	c := ctr("graphite", "172.18.0.4", tcpPort(80, 0), tcpPort(2003, 0))
	snap, skipped := toMembers([]container{c}, defaultMapping())
	require.Empty(t, snap)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "2 candidate tcp ports (80, 2003)")
	require.Contains(t, skipped[0].reason, "set 'port' or 'port_label'")

	// naming one resolves it
	m := defaultMapping()
	m.port = "2003"
	snap, skipped = toMembers([]container{c}, m)
	require.Empty(t, skipped)
	require.Equal(t, "172.18.0.4:2003", snap[0].Address)
}

// The same port is reported once per host interface (0.0.0.0 and ::), so
// a singly-published port must not read as ambiguous.
func TestDuplicateInterfaceBindingsAreOnePort(t *testing.T) {
	c := ctr("grafana", "172.18.0.5",
		containerPort{IP: "0.0.0.0", PrivatePort: 3000, PublicPort: 3000, Type: protoTCP},
		containerPort{IP: "::", PrivatePort: 3000, PublicPort: 3000, Type: protoTCP},
	)
	snap, skipped := toMembers([]container{c}, defaultMapping())
	require.Empty(t, skipped)
	require.Len(t, snap, 1, "one port published on two interfaces is one port")
}

// A published port names a host and a port together; pairing one
// binding's host with another's port yields an address that does not
// exist.
func TestPublicAddressPairsHostWithItsOwnPort(t *testing.T) {
	c := ctr("multi", "172.18.0.6",
		containerPort{IP: "127.0.0.1", PrivatePort: 9090, PublicPort: 19090, Type: protoTCP},
		containerPort{IP: "0.0.0.0", PrivatePort: 3000, PublicPort: 13000, Type: protoTCP},
	)
	m := defaultMapping()
	m.addressType = do.AddressPublic
	m.port = "19090"
	snap, skipped := toMembers([]container{c}, m)
	require.Empty(t, skipped)
	require.Equal(t, "127.0.0.1:19090", snap[0].Address,
		"the loopback-only binding's host, not the wildcard one's")

	m.port = "13000"
	snap, _ = toMembers([]container{c}, m)
	require.Equal(t, "127.0.0.1:13000", snap[0].Address,
		"a wildcard binding is not dialable, so it becomes loopback")
}

// A container publishing nothing cannot serve a public address.
func TestPublicWithNoPublishedPortIsExcluded(t *testing.T) {
	c := ctr("internal", "172.18.0.7", tcpPort(6379, 0))
	m := defaultMapping()
	m.addressType = do.AddressPublic
	_, skipped := toMembers([]container{c}, m)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "publishes no tcp port")
}

// A container on several networks must be told which to use rather than
// having a map iteration pick one, which would differ between polls.
func TestMultipleNetworksRequireAChoice(t *testing.T) {
	c := ctr("multi-net", "172.18.0.8", tcpPort(9090, 0))
	c.NetworkSettings.Networks["backend"] = &networkEndpoint{IPAddress: "10.5.0.2"}
	_, skipped := toMembers([]container{c}, defaultMapping())
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "several networks (backend, bridge)")

	m := defaultMapping()
	m.network = "backend"
	snap, skipped := toMembers([]container{c}, m)
	require.Empty(t, skipped)
	require.Equal(t, "10.5.0.2:9090", snap[0].Address)

	m.network = "absent"
	_, skipped = toMembers([]container{c}, m)
	require.Contains(t, skipped[0].reason, "not attached to network absent")
}

// Health is only in the Status string; these are the exact strings a real
// daemon writes (verified against Docker Engine API 1.55).
func TestReadinessFromStatusString(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   discovery.ReadyState
	}{
		{"Up 2 days (healthy)", discovery.Ready},
		{"Up About a minute (unhealthy)", discovery.NotReady},
		{"Up About a minute (health: starting)", discovery.NotReady},
		{"Up 2 days", discovery.ReadyUnknown},
	} {
		t.Run(tc.status, func(t *testing.T) {
			c := ctr("x", "172.18.0.9", tcpPort(9090, 0))
			c.Status = tc.status
			snap, _ := toMembers([]container{c}, defaultMapping())
			require.Len(t, snap, 1)
			require.Equal(t, tc.want, snap[0].Ready)
		})
	}
}

// A container with no HEALTHCHECK is ReadyUnknown, never Ready: the
// daemon knows the process started, not that it is serving.
func TestNoHealthcheckIsUnknownNotReady(t *testing.T) {
	c := ctr("x", "172.18.0.9", tcpPort(9090, 0))
	snap, _ := toMembers([]container{c}, defaultMapping())
	require.Equal(t, discovery.ReadyUnknown, snap[0].Ready)
	require.NotEqual(t, discovery.Ready, snap[0].Ready)
}

// Non-running containers leave membership entirely rather than being
// carried as NotReady, so they drain from pools as they stop.
func TestNonRunningContainersAreOmittedSilently(t *testing.T) {
	stopped := ctr("gone", "172.18.0.9", tcpPort(9090, 0))
	stopped.State = "exited"
	stopped.Status = "Exited (0) 2 days ago"
	snap, skipped := toMembers([]container{stopped}, defaultMapping())
	require.Empty(t, snap)
	require.Empty(t, skipped,
		"a stopped container is ordinary, not a misconfiguration to report")
}

func TestLabels(t *testing.T) {
	c := ctr("prom", "172.18.0.2", tcpPort(9090, 0))
	c.Labels = map[string]string{
		"com.docker.compose.service": "prometheus",
		"container_name":             "spoofed",
	}
	snap, _ := toMembers([]container{c}, defaultMapping())
	l := snap[0].Labels
	require.Equal(t, "prom", l["container_name"])
	require.Equal(t, "spoofed", l["label_container_name"],
		"container labels are prefixed so they cannot shadow ours")
	require.Equal(t, "prometheus", l["label_com.docker.compose.service"])
	require.Equal(t, "172.18.0.2", l["private_ip"])
	require.Equal(t, "bridge", l["network"])
	require.Equal(t, "0123456789ab", l["container_id"], "short id, as docker shows it")
	require.Equal(t, stateRunning, l["state"])
}

func TestPortLabelWinsOverStaticPort(t *testing.T) {
	c := ctr("prom", "172.18.0.2", tcpPort(9090, 0))
	c.Labels = map[string]string{"tk-port": "8080"}
	m := defaultMapping()
	m.port = "9090"
	m.portLabel = "tk-port"
	snap, _ := toMembers([]container{c}, m)
	require.Equal(t, "172.18.0.2:8080", snap[0].Address)

	// a container without the label falls back to the static port
	bare := ctr("bare", "172.18.0.3", tcpPort(9090, 0))
	snap, _ = toMembers([]container{bare}, m)
	require.Equal(t, "172.18.0.3:9090", snap[0].Address)
}

func TestInvalidPortLabelIsExcluded(t *testing.T) {
	c := ctr("prom", "172.18.0.2", tcpPort(9090, 0))
	c.Labels = map[string]string{"tk-port": "not-a-port"}
	m := defaultMapping()
	m.portLabel = "tk-port"
	_, skipped := toMembers([]container{c}, m)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, `is not a valid port`)
}

func TestReplicaGroupComesFromALabel(t *testing.T) {
	c := ctr("prom", "172.18.0.2", tcpPort(9090, 0))
	c.Labels = map[string]string{"shard": "a"}
	m := defaultMapping()
	m.replicaGroupLabel = "shard"
	snap, _ := toMembers([]container{c}, m)
	require.Equal(t, "a", snap[0].ReplicaGroup)
}

// Containers arrive newest-first, which reorders as they are replaced;
// an unstable order would defeat the Emitter's no-change suppression.
func TestMembersAreSortedByAddress(t *testing.T) {
	cs := []container{
		ctr("c", "172.18.0.30", tcpPort(9090, 0)),
		ctr("a", "172.18.0.10", tcpPort(9090, 0)),
		ctr("b", "172.18.0.20", tcpPort(9090, 0)),
	}
	snap, _ := toMembers(cs, defaultMapping())
	require.Equal(t, []string{
		"172.18.0.10:9090", "172.18.0.20:9090", "172.18.0.30:9090",
	}, addressesOf(snap))
}

func TestUnnamedContainerFallsBackToShortID(t *testing.T) {
	c := ctr("x", "172.18.0.2", tcpPort(9090, 0))
	c.Names = nil
	snap, _ := toMembers([]container{c}, defaultMapping())
	require.Equal(t, "0123456789ab", snap[0].Name)
}

func TestParseContainersEmptyIsAuthoritative(t *testing.T) {
	for _, empty := range []string{"", "  ", "[]"} {
		cs, err := parseContainers([]byte(empty))
		require.NoError(t, err)
		require.Empty(t, cs)
	}
}

func TestParseContainersRejectsMalformedJSON(t *testing.T) {
	_, err := parseContainers([]byte("{{{"))
	require.Error(t, err)
}

func addressesOf(s discovery.Snapshot) []string {
	out := make([]string, len(s))
	for i, m := range s {
		out[i] = m.Address
	}
	return out
}

// --- the captured real response ------------------------------------------

// realResponse is a redacted GET /containers/json?all=1 captured from a
// real Docker daemon (Engine API 1.55). It is what keeps the types honest
// about the shape the daemon actually sends, as opposed to the shape the
// documentation describes.
func realResponse(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/containers.json")
	require.NoError(t, err)
	return b
}

func TestParseRealResponse(t *testing.T) {
	cs, err := parseContainers(realResponse(t))
	require.NoError(t, err)
	require.Len(t, cs, 7)

	byName := map[string]*container{}
	for i := range cs {
		byName[cs[i].name()] = &cs[i]
	}
	require.Contains(t, byName, "environment-prometheus-1")
	require.Contains(t, byName, "environment-graphite-1")

	// the health strings a real daemon writes
	require.Equal(t, discovery.Ready, byName["environment-graphite-1"].ready())
	require.Equal(t, discovery.NotReady, byName["tk-disco-unhealthy"].ready())
	require.Equal(t, discovery.NotReady, byName["tk-disco-starting"].ready())
	require.Equal(t, discovery.ReadyUnknown,
		byName["environment-prometheus-1"].ready(),
		"no HEALTHCHECK declared: the daemon reports no health at all")

	// GET /containers/json carries no Health object; if a future API
	// version adds one this assertion is the prompt to use it
	require.NotContains(t, string(realResponse(t)), `"Health"`)
}

func TestMapRealResponseToMembers(t *testing.T) {
	cs, err := parseContainers(realResponse(t))
	require.NoError(t, err)

	snap, skipped := toMembers(cs, defaultMapping())

	// prometheus (9090) and the two throwaways (6379) resolve on their
	// sole tcp port; telegraf resolves because its other ports are udp
	got := map[string]string{}
	for _, m := range snap {
		got[m.Name] = m.Address
	}
	require.Contains(t, got, "environment-prometheus-1")
	require.Contains(t, got, "environment-telegraf-1")
	require.True(t, strings.HasSuffix(got["environment-prometheus-1"], ":9090"))
	require.True(t, strings.HasSuffix(got["environment-telegraf-1"], ":8094"),
		"the one tcp port among three")

	// graphite (80, 2003) and jaeger (7 tcp ports) are genuinely ambiguous
	reasons := map[string]string{}
	for _, e := range skipped {
		reasons[e.name] = e.reason
	}
	require.Contains(t, reasons["environment-graphite-1"], "candidate tcp ports")
	require.Contains(t, reasons["environment-jaeger-1"], "candidate tcp ports")

	// the exited container is neither a member nor an exclusion
	require.NotContains(t, got, "environment-graphite_seed-1")
	require.NotContains(t, reasons, "environment-graphite_seed-1")
}
