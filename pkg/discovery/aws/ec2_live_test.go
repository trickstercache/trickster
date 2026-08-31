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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	awsopts "github.com/trickstercache/trickster/v2/pkg/discovery/aws/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

// These tests run against a real AWS account and are skipped unless
// TRICKSTER_EC2_TEST=1, following the repo's TRICKSTER_DNS_TEST and
// TRICKSTER_KIND_TEST convention. They never run in CI.
//
// They exist for the three things a fake cannot honestly verify: that AWS
// accepts our SigV4 signature, that the response document is shaped the way
// we decode it, and that Filter.N and NextToken are encoded the way EC2
// expects. Everything else is covered by the fake, which is what CI runs.
//
// Credentials come from the ambient environment through the standard chain;
// this test never handles a secret. Configure the profile and region with
// TRICKSTER_EC2_PROFILE (default: the chain's own) and TRICKSTER_EC2_REGION
// (default: us-east-1), and run:
//
//	TRICKSTER_EC2_TEST=1 TRICKSTER_EC2_PROFILE=myprofile \
//	  go test ./pkg/discovery/aws/ -run Live -v -count=1
//
// The fixture account has six running t4g.nano instances tagged
// service=prometheus and trickster-port=9090.
func liveOptions(t *testing.T) *do.Options {
	t.Helper()
	if os.Getenv("TRICKSTER_EC2_TEST") != "1" {
		t.Skip("set TRICKSTER_EC2_TEST=1 to run against a real AWS account")
	}
	region := os.Getenv("TRICKSTER_EC2_REGION")
	if region == "" {
		region = "us-east-1"
	}
	return &do.Options{
		Name:     "live-ec2",
		Provider: "aws",
		AWS: &awsopts.Options{
			Service: awsopts.ServiceEC2,
			Region:  region,
			Profile: os.Getenv("TRICKSTER_EC2_PROFILE"),
		},
		HTTP: &do.HTTPOptions{
			Interval: timeconv.Duration(30 * time.Second),
			Timeout:  timeconv.Duration(30 * time.Second),
		},
	}
}

// liveSubscription builds one subscription directly, so a test can drive a
// single poll rather than waiting on the loop. pageSize forces pagination.
func liveSubscription(t *testing.T, q *do.Query, pageSize int) *subscription {
	t.Helper()
	p, err := newProvider("live-ec2", liveOptions(t))
	require.NoError(t, err)
	p.pageSize = pageSize
	runner, err := p.newSubscription(q, func(discovery.Snapshot) {})
	require.NoError(t, err)
	return runner.(*subscription)
}

// TestLiveSigningAndResponseShape is the whole point of the live suite: if
// AWS accepts the signature and the document decodes, the two risks a fake
// cannot cover are retired.
func TestLiveSigningAndResponseShape(t *testing.T) {
	s := liveSubscription(t, &do.Query{PortLabel: "trickster-port"}, 0)
	instances, err := s.describeInstances(t.Context())
	require.NoError(t, err, "AWS rejected the request or the response did not decode")
	require.NotEmpty(t, instances, "no instances returned; is the fixture account populated?")

	for _, i := range instances {
		require.NotEmpty(t, i.InstanceID)
		require.NotEmpty(t, i.State.Name)
		require.NotEmpty(t, i.PrivateIPAddress)
		require.NotEmpty(t, i.AvailabilityZone)
	}
	t.Logf("described %d instances", len(instances))
}

// Server-side Filter.N.Name / Filter.N.Value.M encoding is fiddly, and its
// failure mode is silent: a malformed filter returns everything rather than
// erroring, so this asserts the filter actually narrowed the result.
func TestLiveServerSideFilters(t *testing.T) {
	all := liveSubscription(t, &do.Query{PortLabel: "trickster-port"}, 0)
	total, err := all.describeInstances(t.Context())
	require.NoError(t, err)

	matching := liveSubscription(t, &do.Query{
		PortLabel: "trickster-port",
		Filters:   map[string][]string{"tag:service": {"prometheus"}},
	}, 0)
	got, err := matching.describeInstances(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, got, "the tag filter matched nothing; is the fixture tagged?")

	none := liveSubscription(t, &do.Query{
		PortLabel: "trickster-port",
		Filters:   map[string][]string{"tag:service": {"definitely-not-a-real-value"}},
	}, 0)
	empty, err := none.describeInstances(t.Context())
	require.NoError(t, err)
	require.Empty(t, empty,
		"a filter matching nothing returned instances, so the filter was ignored")
	require.LessOrEqual(t, len(got), len(total))
}

// NextToken paging, against the real API rather than a fake that echoes our
// own assumptions. MaxResults has a floor of 5, so six instances is the
// smallest fixture that produces a second page.
func TestLivePagination(t *testing.T) {
	unpaged := liveSubscription(t, &do.Query{PortLabel: "trickster-port"}, 0)
	all, err := unpaged.describeInstances(t.Context())
	require.NoError(t, err)
	require.Greater(t, len(all), 5,
		"pagination needs more than 5 instances to produce a second page")

	paged := liveSubscription(t, &do.Query{PortLabel: "trickster-port"}, 5)
	got, err := paged.describeInstances(t.Context())
	require.NoError(t, err)
	require.Len(t, got, len(all),
		"paging lost or duplicated instances relative to a single-page read")
}

// The end-to-end result: what would actually land in an ALB pool.
func TestLiveMembersAreUsable(t *testing.T) {
	s := liveSubscription(t, &do.Query{
		PortLabel:   "trickster-port",
		AddressType: do.AddressPrivate,
	}, 0)
	instances, err := s.describeInstances(t.Context())
	require.NoError(t, err)

	snap, skipped := toMembers(instances, s.mapping)
	for _, e := range skipped {
		t.Logf("excluded %s: %s", e.instanceID, e.reason)
	}
	require.NotEmpty(t, snap)
	for _, m := range snap {
		require.NotEmpty(t, m.Address)
		require.Contains(t, m.Address, ":9090",
			"the port should come from the trickster-port tag")
		require.Equal(t, discovery.Ready, m.Ready)
	}
	t.Logf("mapped %d members, excluded %d", len(snap), len(skipped))
}
