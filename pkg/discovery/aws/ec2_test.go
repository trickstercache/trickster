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
	"os"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
)

// realResponse is an actual DescribeInstances document, captured from the
// EC2 API against six live t4g.nano instances and redacted only for the
// account id, request id and public addresses.
//
// It exists because a fake written from the API documentation would encode
// whatever the author misread, and the parser would then be tested against
// that same misreading. Two things in this document are not what the
// parameter names suggest -- the public address element is `ipAddress`, not
// `publicIpAddress`, and instances are nested two levels deep inside
// reservations -- and only a real response proves it.
func realResponse(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/describe_instances.xml")
	require.NoError(t, err)
	return b
}

func TestParseRealDescribeInstancesResponse(t *testing.T) {
	resp, err := parseDescribeInstances(realResponse(t))
	require.NoError(t, err)
	require.NotEmpty(t, resp.Reservations, "reservationSet>item did not decode")

	var instances []ec2Instance
	for _, r := range resp.Reservations {
		instances = append(instances, r.Instances...)
	}
	require.Len(t, instances, 6, "instancesSet>item did not decode")

	for _, i := range instances {
		require.NotEmpty(t, i.InstanceID)
		require.Equal(t, "running", i.State.Name, "instanceState>name did not decode")
		require.NotEmpty(t, i.PrivateIPAddress)
		require.NotEmpty(t, i.AvailabilityZone, "placement>availabilityZone did not decode")
		require.NotEmpty(t, i.VPCID)
		require.NotEmpty(t, i.SubnetID)
		require.Equal(t, "t4g.nano", i.InstanceType)
		require.Equal(t, "arm64", i.Architecture)
		require.NotEmpty(t, i.Tags, "tagSet>item did not decode")
		require.Equal(t, "9090", i.tag("trickster-port"))
		require.Equal(t, "prometheus", i.tag("service"))
	}

	// the public address lives in `ipAddress`, which is the element name
	// most likely to be guessed wrong
	require.NotEmpty(t, instances[0].PublicIPAddress,
		"the public address element is `ipAddress`, not `publicIpAddress`")
}

// The end-to-end mapping over a real document: what an operator would
// actually get in their pool.
func TestMapRealResponseToMembers(t *testing.T) {
	resp, err := parseDescribeInstances(realResponse(t))
	require.NoError(t, err)
	var instances []ec2Instance
	for _, r := range resp.Reservations {
		instances = append(instances, r.Instances...)
	}

	snap, skipped := toMembers(instances, mapping{
		scheme:      "http",
		addressType: do.AddressPrivate,
		portLabel:   "trickster-port",
	})
	require.Empty(t, skipped)
	require.Len(t, snap, 6)
	for _, m := range snap {
		require.Equal(t, discovery.Ready, m.Ready)
		require.Equal(t, "http", m.Scheme)
		require.Regexp(t, `^172\.31\.\d+\.\d+:9090$`, m.Address,
			"the private address and the port tag should compose the member address")
		require.Equal(t, "trickster-sd", m.Name, "the Name tag becomes the member name")
		require.Equal(t, "t4g.nano", m.Labels["instance_type"])
		require.Equal(t, "prometheus", m.Labels["tag_service"])
	}

	// the same instances addressed publicly
	snap, skipped = toMembers(instances, mapping{
		scheme:      "https",
		addressType: do.AddressPublic,
		port:        "443",
	})
	require.Empty(t, skipped)
	require.Len(t, snap, 6)
	require.Regexp(t, `^203\.0\.113\.\d+:443$`, snap[0].Address)
}

// Filtering by tag key is applied to a real document rather than a
// hand-built one, so a mismatch between the tag decoder and the filter
// cannot pass.
func TestTagFilterOnRealResponse(t *testing.T) {
	resp, err := parseDescribeInstances(realResponse(t))
	require.NoError(t, err)
	var instances []ec2Instance
	for _, r := range resp.Reservations {
		instances = append(instances, r.Instances...)
	}

	snap, _ := toMembers(instances, mapping{
		addressType: do.AddressPrivate, port: "9090",
		tags: []string{"service"},
	})
	require.Len(t, snap, 6, "every instance carries the service tag")

	snap, _ = toMembers(instances, mapping{
		addressType: do.AddressPrivate, port: "9090",
		tags: []string{"absent-tag"},
	})
	require.Empty(t, snap)
}
