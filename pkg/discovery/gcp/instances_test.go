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
	"net/http"
	"os"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
)

// instance builds a running instance with one network interface, in the
// shape the Compute API returns.
func instance(name, status, privateIP, publicIP string) gceInstance {
	i := gceInstance{
		ID:          "100" + name,
		Name:        name,
		Status:      status,
		Zone:        "https://www.googleapis.com/compute/v1/projects/p/zones/us-central1-a",
		MachineType: "https://www.googleapis.com/compute/v1/projects/p/zones/us-central1-a/machineTypes/e2-micro",
		NetworkInterfaces: []networkInterface{{
			Network:    "https://www.googleapis.com/compute/v1/projects/p/global/networks/default",
			Subnetwork: "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/subnetworks/default",
			NetworkIP:  privateIP,
		}},
	}
	if publicIP != "" {
		i.NetworkInterfaces[0].AccessConfigs = []accessConfig{
			{Type: "ONE_TO_ONE_NAT", NatIP: publicIP},
		}
	}
	return i
}

func defaultMapping() mapping {
	return mapping{scheme: "http", addressType: do.AddressPrivate, port: "9090"}
}

func TestAddressTypes(t *testing.T) {
	i := instance("vm-1", statusRunning, "10.128.0.2", "34.1.2.3")
	i.NetworkInterfaces[0].IPv6Address = "fd20::1"
	i.NetworkInterfaces[0].IPv6AccessConfigs = []ipv6AccessConfig{
		{ExternalIPv6: "2600:1900::1"},
	}
	require.Equal(t, "10.128.0.2", i.address(do.AddressPrivate))
	require.Equal(t, "34.1.2.3", i.address(do.AddressPublic))
	require.Equal(t, "2600:1900::1", i.address(do.AddressIPv6),
		"the external IPv6 wins over the internal one")

	// an IPv6 member address must be bracketed to be a valid host:port
	snap, skipped := toMembers([]gceInstance{i},
		mapping{addressType: do.AddressIPv6, port: "9090"})
	require.Empty(t, skipped)
	require.Equal(t, "[2600:1900::1]:9090", snap[0].Address)

	// with no external IPv6 configured, the internal address is the fallback
	i.NetworkInterfaces[0].IPv6AccessConfigs = nil
	require.Equal(t, "fd20::1", i.address(do.AddressIPv6))
	snap, _ = toMembers([]gceInstance{i},
		mapping{addressType: do.AddressIPv6, port: "9090"})
	require.Equal(t, "[fd20::1]:9090", snap[0].Address)
}

// Asking for an address an instance does not have is a misconfiguration
// worth reporting, not a member with an empty host.
func TestMissingAddressIsExcluded(t *testing.T) {
	i := instance("vm-1", statusRunning, "10.128.0.2", "")
	snap, skipped := toMembers([]gceInstance{i},
		mapping{addressType: do.AddressPublic, port: "9090"})
	require.Empty(t, snap)
	require.Len(t, skipped, 1)
	require.Equal(t, "vm-1", skipped[0].name)
	require.Contains(t, skipped[0].reason, "no public address")
}

// Instances on their way out are omitted entirely rather than reported
// unready, so they drain from pools before they stop answering.
func TestDepartingInstancesAreOmitted(t *testing.T) {
	instances := []gceInstance{
		instance("run", statusRunning, "10.0.0.1", ""),
		instance("prov", statusProvisioning, "10.0.0.2", ""),
		instance("stage", statusStaging, "10.0.0.3", ""),
		instance("repair", statusRepairing, "10.0.0.4", ""),
		instance("stopping", statusStopping, "10.0.0.5", ""),
		instance("suspending", statusSuspending, "10.0.0.6", ""),
		instance("suspended", statusSuspended, "10.0.0.7", ""),
		instance("term", statusTerminated, "10.0.0.8", ""),
	}
	snap, skipped := toMembers(instances, defaultMapping())
	require.Empty(t, skipped)
	require.Len(t, snap, 4, "running plus the three not-yet-or-not-now-serving states")

	byName := map[string]discovery.ReadyState{}
	for _, m := range snap {
		byName[m.Name] = m.Ready
	}
	require.Equal(t, discovery.Ready, byName["run"])
	for _, n := range []string{"prov", "stage", "repair"} {
		require.Equal(t, discovery.NotReady, byName[n],
			"%s is a member, but not a ready one", n)
	}
}

// A status a future Compute Engine release adds must not read as healthy.
func TestUnknownStatusIsNotReady(t *testing.T) {
	require.Equal(t, discovery.NotReady, readyFor("SOME_FUTURE_STATUS"))
	require.False(t, isDeparting("SOME_FUTURE_STATUS"),
		"an unknown status is kept as not-ready rather than silently dropped")
}

// GCE has two key/value namespaces on an instance; a port can live in
// either, and deployments already using metadata should not have to move.
func TestPortLabelReadsLabelsThenMetadata(t *testing.T) {
	labeled := instance("vm-1", statusRunning, "10.0.0.1", "")
	labeled.Labels = map[string]string{"port": "8080"}

	metaOnly := instance("vm-2", statusRunning, "10.0.0.2", "")
	metaOnly.Metadata.Items = []metadataItem{{Key: "port", Value: "8081"}}

	// a label wins over metadata when both carry the key
	both := instance("vm-3", statusRunning, "10.0.0.3", "")
	both.Labels = map[string]string{"port": "8082"}
	both.Metadata.Items = []metadataItem{{Key: "port", Value: "9999"}}

	snap, skipped := toMembers([]gceInstance{labeled, metaOnly, both},
		mapping{addressType: do.AddressPrivate, portLabel: "port"})
	require.Empty(t, skipped)
	require.Equal(t, "10.0.0.1:8080", snap[0].Address)
	require.Equal(t, "10.0.0.2:8081", snap[1].Address)
	require.Equal(t, "10.0.0.3:8082", snap[2].Address)
}

// A static port is the fallback when an instance carries neither, which is
// what makes port_label safe to adopt incrementally across a fleet.
func TestPortLabelFallsBackToStaticPort(t *testing.T) {
	labeled := instance("vm-1", statusRunning, "10.0.0.1", "")
	labeled.Labels = map[string]string{"port": "8080"}
	bare := instance("vm-2", statusRunning, "10.0.0.2", "")

	snap, skipped := toMembers([]gceInstance{labeled, bare},
		mapping{addressType: do.AddressPrivate, portLabel: "port", port: "9090"})
	require.Empty(t, skipped)
	require.Equal(t, "10.0.0.1:8080", snap[0].Address)
	require.Equal(t, "10.0.0.2:9090", snap[1].Address)

	// with neither, the instance is excluded rather than failing the refresh
	snap, skipped = toMembers([]gceInstance{bare},
		mapping{addressType: do.AddressPrivate, portLabel: "port"})
	require.Empty(t, snap)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "no port")
}

func TestInvalidPortIsExcluded(t *testing.T) {
	i := instance("vm-1", statusRunning, "10.0.0.1", "")
	i.Labels = map[string]string{"port": "not-a-port"}
	_, skipped := toMembers([]gceInstance{i},
		mapping{addressType: do.AddressPrivate, portLabel: "port"})
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "not a port number")
}

// Network tags are names without values, so they filter on presence.
func TestNetworkTagFilter(t *testing.T) {
	tagged := instance("vm-1", statusRunning, "10.0.0.1", "")
	tagged.Tags.Items = []string{"http-server", "prom"}
	partial := instance("vm-2", statusRunning, "10.0.0.2", "")
	partial.Tags.Items = []string{"http-server"}
	bare := instance("vm-3", statusRunning, "10.0.0.3", "")

	m := defaultMapping()
	m.tags = []string{"http-server", "prom"}
	snap, _ := toMembers([]gceInstance{tagged, partial, bare}, m)
	require.Len(t, snap, 1, "every listed tag must be present, not any")
	require.Equal(t, "vm-1", snap[0].Name)
}

func TestReplicaGroupFromLabelOrMetadata(t *testing.T) {
	i := instance("vm-1", statusRunning, "10.0.0.1", "")
	i.Labels = map[string]string{"shard": "shard-0"}
	snap, _ := toMembers([]gceInstance{i},
		mapping{addressType: do.AddressPrivate, port: "9090", replicaGroupLabel: "shard"})
	require.Equal(t, "shard-0", snap[0].ReplicaGroup)

	// with no key configured, labels are not treated as a group
	snap, _ = toMembers([]gceInstance{i}, defaultMapping())
	require.Empty(t, snap[0].ReplicaGroup)
}

// Resource URLs are the API's identifiers; an operator reads the last
// segment, and a member label full of URLs is unreadable.
func TestMemberLabelsUseShortNames(t *testing.T) {
	i := instance("vm-1", statusRunning, "10.128.0.2", "34.1.2.3")
	i.Labels = map[string]string{"env": "prod"}
	i.Tags.Items = []string{"http-server", "prom"}

	snap, _ := toMembers([]gceInstance{i}, defaultMapping())
	l := snap[0].Labels
	require.Equal(t, "us-central1-a", l["zone"])
	require.Equal(t, "e2-micro", l["machine_type"])
	require.Equal(t, "default", l["network"])
	require.Equal(t, "default", l["subnetwork"])
	require.Equal(t, "10.128.0.2", l["private_ip"])
	require.Equal(t, "34.1.2.3", l["public_ip"])
	require.Equal(t, ",http-server,prom,", l["tags"],
		"separators bracket the list so a tag match is unambiguous")
	require.Equal(t, "prod", l["label_env"],
		"instance labels are prefixed so they cannot shadow assigned ones")
}

// A user label named like an assigned one must not shadow it.
func TestUserLabelsCannotShadow(t *testing.T) {
	i := instance("vm-1", statusRunning, "10.128.0.2", "")
	i.Labels = map[string]string{"zone": "not-the-zone", "status": "lies"}
	snap, _ := toMembers([]gceInstance{i}, defaultMapping())
	require.Equal(t, "us-central1-a", snap[0].Labels["zone"])
	require.Equal(t, statusRunning, snap[0].Labels["status"])
	require.Equal(t, "not-the-zone", snap[0].Labels["label_zone"])
}

// The aggregated response is keyed by zone, and a zone with no matching
// instances appears carrying a warning rather than an empty list.
func TestAggregatedListFlattensZonesDeterministically(t *testing.T) {
	body := `{
	  "items": {
	    "zones/us-central1-b": {"instances": [
	      {"name":"b1","status":"RUNNING","networkInterfaces":[{"networkIP":"10.0.2.1"}]}
	    ]},
	    "zones/us-central1-a": {"instances": [
	      {"name":"a1","status":"RUNNING","networkInterfaces":[{"networkIP":"10.0.1.1"}]},
	      {"name":"a2","status":"RUNNING","networkInterfaces":[{"networkIP":"10.0.1.2"}]}
	    ]},
	    "zones/us-central1-c": {"warning": {"code":"NO_RESULTS_ON_PAGE"}}
	  }
	}`
	resp, err := parseAggregatedList([]byte(body))
	require.NoError(t, err)
	got := resp.instancesOf()
	require.Len(t, got, 3, "a warning-only zone contributes nothing")

	// zone order is sorted, not Go's map iteration, so successive identical
	// responses produce identical snapshots
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	require.Equal(t, []string{"a1", "a2", "b1"}, names)
	for range 20 {
		again, err := parseAggregatedList([]byte(body))
		require.NoError(t, err)
		second := again.instancesOf()
		require.Equal(t, names,
			[]string{second[0].Name, second[1].Name, second[2].Name},
			"zone iteration order must be stable")
	}
}

func TestParseAggregatedListRejectsMalformedJSON(t *testing.T) {
	_, err := parseAggregatedList([]byte("{{{"))
	require.Error(t, err)
}

// The Google API error document names the failure far more usefully than
// the status code alone.
func TestAPIErrorMessageIsSurfaced(t *testing.T) {
	err := apiError(http.StatusForbidden, []byte(`{"error":{
	  "code": 403,
	  "message": "Required 'compute.instances.list' permission for 'projects/p'",
	  "status": "PERMISSION_DENIED"}}`))
	require.Contains(t, err.Error(), "PERMISSION_DENIED")
	require.Contains(t, err.Error(), "compute.instances.list")

	// a message with no status still surfaces
	err = apiError(http.StatusBadRequest, []byte(`{"error":{"message":"bad filter"}}`))
	require.Contains(t, err.Error(), "bad filter")

	// and a non-JSON body still yields the status
	err = apiError(http.StatusBadGateway, []byte("gateway down"))
	require.Contains(t, err.Error(), "502")
}

func TestLastSegment(t *testing.T) {
	require.Equal(t, "us-central1-a",
		lastSegment("https://www.googleapis.com/compute/v1/projects/p/zones/us-central1-a"))
	require.Equal(t, "plain", lastSegment("plain"))
	require.Empty(t, lastSegment(""))
}

// realResponse is an actual instances.aggregatedList document, captured
// from the Compute API against four live e2-micro instances across two
// zones and redacted for the project identity and public addresses. The
// warning-only zones are trimmed from 130 to four: a hundred more identical
// warnings prove nothing, and a readable fixture is worth more than an
// exhaustive one.
//
// It exists because this provider's response types were written from the
// API documentation rather than from a captured document, which is exactly
// the situation where a hand-written fake encodes the author's misreading
// and the parser is then tested against it.
func realResponse(t *testing.T) *aggregatedList {
	t.Helper()
	b, err := os.ReadFile("testdata/aggregated_instances.json")
	require.NoError(t, err)
	resp, err := parseAggregatedList(b)
	require.NoError(t, err)
	return resp
}

func TestParseRealAggregatedListResponse(t *testing.T) {
	resp := realResponse(t)

	// the response covers every zone in the project, and the great majority
	// carry a warning rather than an instance list
	var withInstances int
	for _, z := range resp.Items {
		if len(z.Instances) > 0 {
			withInstances++
		}
	}
	require.Equal(t, 2, withInstances)
	require.Greater(t, len(resp.Items), withInstances,
		"zones with no matching instances still appear, carrying a warning")

	instances := resp.instancesOf()
	require.Len(t, instances, 4, "a warning-only zone contributes nothing")

	var withPublic, withLabels, withMetadata, withTags int
	for _, i := range instances {
		require.NotEmpty(t, i.ID)
		require.NotEmpty(t, i.Name)
		require.Equal(t, statusRunning, i.Status)
		require.NotEmpty(t, i.Zone)
		require.NotEmpty(t, i.MachineType)
		require.NotEmpty(t, i.NetworkInterfaces[0].NetworkIP,
			"networkIP is the element the private address lives in")
		if i.address(do.AddressPublic) != "" {
			withPublic++
		}
		if len(i.Labels) > 0 {
			withLabels++
		}
		if len(i.Metadata.Items) > 0 {
			withMetadata++
		}
		if len(i.Tags.Items) > 0 {
			withTags++
		}
	}
	require.Equal(t, 3, withPublic,
		"accessConfigs[].natIP is the element the public address lives in")
	require.Equal(t, 4, withLabels)
	require.Equal(t, 4, withTags)
	// only the instance created with explicit metadata carries any: GCE
	// omits metadata.items entirely rather than sending an empty list, so
	// the label-then-metadata fallback has to tolerate its absence
	require.Equal(t, 1, withMetadata)
}

// The end-to-end mapping over a real document: what an operator would
// actually get in their pool.
func TestMapRealResponseToMembers(t *testing.T) {
	instances := realResponse(t).instancesOf()

	snap, skipped := toMembers(instances, mapping{
		scheme: "http", addressType: do.AddressPrivate, portLabel: "trickster-port",
	})
	require.Empty(t, skipped)
	require.Len(t, snap, 4)
	for _, m := range snap {
		require.Equal(t, discovery.Ready, m.Ready)
		require.Regexp(t, `^10\.128\.0\.\d+:909[01]$`, m.Address,
			"the private address and the port label or metadata key compose the address")
		require.Contains(t, []string{"us-central1-a", "us-central1-b"},
			m.Labels["zone"], "the zone URL should be shortened to its name")
		require.Equal(t, "e2-micro", m.Labels["machine_type"])
		require.Equal(t, "prometheus", m.Labels["label_service"])
	}

	// one fixture instance carries its port in metadata rather than a
	// label, so both resolution paths are exercised on real data
	var fromMetadata int
	for _, i := range instances {
		if _, ok := i.Labels["trickster-port"]; !ok && i.label("trickster-port") != "" {
			fromMetadata++
		}
	}
	require.Equal(t, 1, fromMetadata,
		"the label-then-metadata fallback should be exercised by the fixture")

	// and one has no external address, so requesting public excludes it
	// with a reason rather than dropping it silently
	snap, skipped = toMembers(instances, mapping{
		scheme: "http", addressType: do.AddressPublic, portLabel: "trickster-port",
	})
	require.Len(t, snap, 3)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "no public address")
}
