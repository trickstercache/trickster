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
	"os"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	gcpopts "github.com/trickstercache/trickster/v2/pkg/discovery/gcp/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// These tests run against a real GCP project and are skipped unless
// TRICKSTER_GCP_TEST=1, following the repo's TRICKSTER_DNS_TEST and
// TRICKSTER_KIND_TEST convention. They never run in CI.
//
// They exist for what a fake cannot honestly establish, since this
// provider's response types were written from the API documentation rather
// than from a captured document: that the Compute API accepts our OAuth2
// token and scope, that instances.aggregatedList is shaped the way we
// decode it, and that the filter expression and returnPartialSuccess are
// accepted as sent.
//
// Credentials come from the ambient environment through Application Default
// Credentials; these tests never handle a secret. Authenticate first with
// either of:
//
//	gcloud auth application-default login
//	export GOOGLE_APPLICATION_CREDENTIALS=/path/to/sa.json
//
// then, after ./trickster-data/gcp-live-fixture.sh up:
//
//	TRICKSTER_GCP_TEST=1 TRICKSTER_GCP_PROJECT=my-project \
//	  go test ./pkg/discovery/gcp/ -run Live -v -count=1
//
// Where ADC is not configured but gcloud is signed in, a token may be
// supplied directly instead:
//
//	TRICKSTER_GCP_ACCESS_TOKEN=$(gcloud auth print-access-token)
//
// That exercises every API-facing behavior these tests exist for -- the
// response shape, the filter syntax, pagination, the per-zone map -- but
// not credential resolution itself, which is x/oauth2/google library code
// and is covered for the credentials_file path by the unit tests.
func liveOptions(t *testing.T) *do.Options {
	t.Helper()
	if os.Getenv("TRICKSTER_GCP_TEST") != "1" {
		t.Skip("set TRICKSTER_GCP_TEST=1 to run against a real GCP project")
	}
	project := os.Getenv("TRICKSTER_GCP_PROJECT")
	if project == "" {
		// skip rather than fail: an environment with the gate on but no
		// fixture should say so plainly, not surface as a credential error
		t.Skip("set TRICKSTER_GCP_PROJECT to run the gcp live tests")
	}
	return &do.Options{
		Name:     "live-gcp",
		Provider: "gcp",
		GCP: &gcpopts.Options{Service: gcpopts.ServiceGCE,
			Project:         project,
			CredentialsFile: os.Getenv("TRICKSTER_GCP_CREDENTIALS_FILE"),
		},
		HTTP: &do.HTTPOptions{
			Interval: timeconv.Duration(30 * time.Second),
			Timeout:  timeconv.Duration(30 * time.Second),
		},
	}
}

// liveSubscription builds one subscription directly, so a test can drive a
// single call rather than waiting on the poll loop.
func liveSubscription(t *testing.T, q *do.Query, pageSize int) *subscription {
	t.Helper()
	p, err := newProvider("live-gcp", liveOptions(t))
	require.NoError(t, err)
	p.pageSize = pageSize
	if tok := os.Getenv("TRICKSTER_GCP_ACCESS_TOKEN"); tok != "" {
		p.tokens = oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: tok, TokenType: "Bearer"})
	}
	runner, err := p.newSubscription(q, func(discovery.Snapshot) {})
	require.NoError(t, err)
	return runner.(*subscription)
}

// The point of the live suite: if the API accepts the token and the
// document decodes into the fields we rely on, the risk of having written
// the response types from documentation is retired.
func TestLiveAuthAndResponseShape(t *testing.T) {
	s := liveSubscription(t, &do.Query{PortLabel: "trickster-port"}, 0)
	instances, err := s.listInstances(t.Context())
	require.NoError(t, err,
		"the Compute API rejected the request or the response did not decode")
	require.NotEmpty(t, instances, "no instances; is the fixture project populated?")

	var withPublic, withLabels, withMetadata, withTags int
	for _, i := range instances {
		require.NotEmpty(t, i.Name, "name did not decode")
		require.NotEmpty(t, i.Status, "status did not decode")
		require.NotEmpty(t, i.Zone, "zone did not decode")
		require.NotEmpty(t, i.MachineType, "machineType did not decode")
		require.NotEmpty(t, i.NetworkInterfaces,
			"networkInterfaces did not decode")
		require.NotEmpty(t, i.NetworkInterfaces[0].NetworkIP,
			"networkIP is the element name the private address lives in")
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
	// aggregatedList is sent without a fields mask precisely so that these
	// arrive; if a mask is ever added, these assertions are what catch it
	// dropping something
	require.Positive(t, withLabels, "instance labels did not decode")
	require.Positive(t, withMetadata, "instance metadata did not decode")
	require.Positive(t, withTags, "network tags did not decode")
	require.Positive(t, withPublic,
		"accessConfigs[].natIP is the element name the public address lives in")
	t.Logf("listed %d instances (%d with a public address)", len(instances), withPublic)
}

// The aggregated response is a map keyed by zone. Flattening it is the part
// of the parser most likely to be wrong, and a single-zone fixture would
// not exercise it.
func TestLiveAggregatedAcrossZones(t *testing.T) {
	s := liveSubscription(t, &do.Query{PortLabel: "trickster-port"}, 0)
	project, err := s.p.projectID(t.Context())
	require.NoError(t, err)
	resp, err := s.listPage(t.Context(), project, "")
	require.NoError(t, err)

	var zonesWithInstances int
	for _, z := range resp.Items {
		if len(z.Instances) > 0 {
			zonesWithInstances++
		}
	}
	require.GreaterOrEqual(t, zonesWithInstances, 2,
		"the fixture should span two zones so the per-zone map is exercised")
	require.Greater(t, len(resp.Items), zonesWithInstances,
		"zones with no matching instances should still appear, carrying a warning")
}

// GCE filter syntax is particular, and a filter the API does not understand
// is an error rather than a silent full result -- but a filter that parses
// and matches nothing looks identical to a broken one, so assert both
// directions.
func TestLiveServerSideFilter(t *testing.T) {
	all, err := liveSubscription(t, &do.Query{PortLabel: "trickster-port"}, 0).
		listInstances(t.Context())
	require.NoError(t, err)

	matching, err := liveSubscription(t, &do.Query{
		PortLabel: "trickster-port",
		Filter:    `labels.service = "prometheus"`,
	}, 0).listInstances(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, matching, "the label filter matched nothing; is the fixture labeled?")
	require.LessOrEqual(t, len(matching), len(all))

	none, err := liveSubscription(t, &do.Query{
		PortLabel: "trickster-port",
		Filter:    `labels.service = "definitely-not-a-real-value"`,
	}, 0).listInstances(t.Context())
	require.NoError(t, err)
	require.Empty(t, none,
		"a filter matching nothing returned instances, so the filter was ignored")
}

func TestLivePagination(t *testing.T) {
	unpaged, err := liveSubscription(t, &do.Query{PortLabel: "trickster-port"}, 0).
		listInstances(t.Context())
	require.NoError(t, err)
	require.Greater(t, len(unpaged), 1,
		"pagination needs more than one instance to produce a second page")

	paged, err := liveSubscription(t, &do.Query{PortLabel: "trickster-port"}, 1).
		listInstances(t.Context())
	require.NoError(t, err)
	require.Len(t, paged, len(unpaged),
		"paging lost or duplicated instances relative to a single-page read")
}

// port_label reads an instance label first and instance metadata second;
// the fixture carries one instance of each so both paths are real.
func TestLiveLabelThenMetadataPortResolution(t *testing.T) {
	s := liveSubscription(t, &do.Query{PortLabel: "trickster-port"}, 0)
	instances, err := s.listInstances(t.Context())
	require.NoError(t, err)

	var fromLabel, fromMetadata int
	for _, i := range instances {
		if _, ok := i.Labels["trickster-port"]; ok {
			fromLabel++
			continue
		}
		for _, m := range i.Metadata.Items {
			if m.Key == "trickster-port" {
				fromMetadata++
			}
		}
	}
	require.Positive(t, fromLabel, "no instance carried the port as a label")
	require.Positive(t, fromMetadata, "no instance carried the port as metadata")
}

// The end-to-end result: what would actually land in an ALB pool.
func TestLiveMembersAreUsable(t *testing.T) {
	s := liveSubscription(t, &do.Query{
		PortLabel:   "trickster-port",
		AddressType: do.AddressPrivate,
	}, 0)
	instances, err := s.listInstances(t.Context())
	require.NoError(t, err)

	snap, skipped := toMembers(instances, s.mapping)
	for _, e := range skipped {
		t.Logf("excluded %s: %s", e.name, e.reason)
	}
	require.NotEmpty(t, snap)
	for _, m := range snap {
		require.NotEmpty(t, m.Address)
		require.Equal(t, discovery.Ready, m.Ready)
	}
	t.Logf("mapped %d members, excluded %d", len(snap), len(skipped))
}

// An instance with no external address must be excluded with a reason when
// public addressing is requested, not silently absent.
func TestLiveMissingPublicAddressIsExcluded(t *testing.T) {
	s := liveSubscription(t, &do.Query{
		PortLabel:   "trickster-port",
		AddressType: do.AddressPublic,
	}, 0)
	instances, err := s.listInstances(t.Context())
	require.NoError(t, err)

	_, skipped := toMembers(instances, s.mapping)
	require.NotEmpty(t, skipped,
		"the fixture includes an instance created with --no-address")
	require.Contains(t, skipped[0].reason, "no public address")
}

// Capture the real document for use as a CI fixture, so the verified shape
// outlives the project. Writes only when TRICKSTER_GCP_CAPTURE is set.
func TestLiveCaptureFixture(t *testing.T) {
	path := os.Getenv("TRICKSTER_GCP_CAPTURE")
	if path == "" {
		t.Skip("set TRICKSTER_GCP_CAPTURE=<path> to write a testdata fixture")
	}
	s := liveSubscription(t, &do.Query{PortLabel: "trickster-port"}, 0)
	project, err := s.p.projectID(t.Context())
	require.NoError(t, err)
	// the undecoded body: a fixture round-tripped through this package's
	// own types would only ever prove those types self-consistent
	raw, err := s.fetchPage(t.Context(), project, "")
	require.NoError(t, err)
	var pretty any
	require.NoError(t, json.Unmarshal(raw, &pretty))
	b, err := json.MarshalIndent(pretty, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o600))
	t.Logf("wrote %s (%d bytes)", path, len(b))
}
