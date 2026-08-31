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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	awsopts "github.com/trickstercache/trickster/v2/pkg/discovery/aws/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// instanceXML renders one instance in the shape the real API uses, which
// testdata/describe_instances.xml pins. It is used only for the states and
// failure modes a live fixture cannot cheaply produce -- a terminated
// instance, an untagged one, an API error.
func instanceXML(id, state, privateIP, publicIP string, tags map[string]string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `<item>
      <instanceId>%s</instanceId>
      <instanceType>t4g.nano</instanceType>
      <instanceState><code>0</code><name>%s</name></instanceState>
      <privateIpAddress>%s</privateIpAddress>`, id, state, privateIP)
	if publicIP != "" {
		fmt.Fprintf(&sb, `<ipAddress>%s</ipAddress>`, publicIP)
	}
	sb.WriteString(`<placement><availabilityZone>us-east-1a</availabilityZone></placement>
      <vpcId>vpc-1</vpcId><subnetId>subnet-1</subnetId><architecture>arm64</architecture>`)
	if len(tags) > 0 {
		sb.WriteString(`<tagSet>`)
		for k, v := range tags {
			fmt.Fprintf(&sb, `<item><key>%s</key><value>%s</value></item>`, k, v)
		}
		sb.WriteString(`</tagSet>`)
	}
	sb.WriteString(`</item>`)
	return sb.String()
}

func responseXML(nextToken string, instances ...string) string {
	var token string
	if nextToken != "" {
		token = "<nextToken>" + nextToken + "</nextToken>"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
  <requestId>req-1</requestId>
  <reservationSet><item><instancesSet>` +
		strings.Join(instances, "") +
		`</instancesSet></item></reservationSet>` + token + `</DescribeInstancesResponse>`
}

// fakeEC2 serves canned responses and records the form each request sent.
type fakeEC2 struct {
	*httptest.Server
	mtx      sync.Mutex
	pages    []string
	status   int
	body     string
	requests []map[string][]string
	hits     atomic.Int64
}

func newFakeEC2(t *testing.T, pages ...string) *fakeEC2 {
	t.Helper()
	f := &fakeEC2{pages: pages, status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		require.NoError(t, r.ParseForm())
		f.mtx.Lock()
		f.requests = append(f.requests, r.PostForm)
		status, body, pages := f.status, f.body, f.pages
		f.mtx.Unlock()

		// AWS rejects an unsigned request; assert we always sign
		if r.Header.Get("Authorization") == "" {
			t.Error("request was not signed")
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			w.Write([]byte(body))
			return
		}
		// serve pages in order, keyed by the NextToken the client echoes
		idx := 0
		if tok := r.PostForm.Get("NextToken"); tok != "" {
			fmt.Sscanf(tok, "page-%d", &idx)
		}
		if idx >= len(pages) {
			w.Write([]byte(responseXML("")))
			return
		}
		w.Write([]byte(pages[idx]))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeEC2) setError(status int, body string) {
	f.mtx.Lock()
	f.status, f.body = status, body
	f.mtx.Unlock()
}

func (f *fakeEC2) lastForm(t *testing.T) map[string][]string {
	t.Helper()
	f.mtx.Lock()
	defer f.mtx.Unlock()
	require.NotEmpty(t, f.requests)
	return f.requests[len(f.requests)-1]
}

func fakeOptions(endpoint string) *do.Options {
	return &do.Options{
		Name:     "test-aws",
		Provider: "aws",
		AWS: &awsopts.Options{
			Service: awsopts.ServiceEC2, Region: "us-east-1",
			AccessKey: "AKIDEXAMPLE",
			SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		},
		HTTP: &do.HTTPOptions{
			Endpoint: endpoint,
			Interval: timeconv.Duration(25 * time.Millisecond),
			Timeout:  timeconv.Duration(5 * time.Second),
		},
	}
}

func fakeSubscription(t *testing.T, endpoint string, q *do.Query) *subscription {
	t.Helper()
	p, err := newProvider("test-aws", fakeOptions(endpoint))
	require.NoError(t, err)
	runner, err := p.newSubscription(q, func(discovery.Snapshot) {})
	require.NoError(t, err)
	return runner.(*subscription)
}

func TestNewValidatesService(t *testing.T) {
	_, err := New("test", nil)
	require.Error(t, err)

	o := fakeOptions("http://example.com")
	o.AWS.Service = "lambda"
	_, err = New("test", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")

	// an unset service defaults to ec2 rather than failing
	o = fakeOptions("http://example.com")
	o.AWS.Service = ""
	d, err := New("test", o)
	require.NoError(t, err)
	require.NotNil(t, d)
}

// The endpoint is derived from region and service, so a region is required
// unless an override supplies the endpoint outright.
func TestEndpointResolution(t *testing.T) {
	got, err := resolveEndpoint("", "ec2", "eu-west-2")
	require.NoError(t, err)
	require.Equal(t, "https://ec2.eu-west-2.amazonaws.com", got)

	got, err = resolveEndpoint("https://vpce-1234.ec2.us-east-1.vpce.amazonaws.com", "ec2", "")
	require.NoError(t, err)
	require.Equal(t, "https://vpce-1234.ec2.us-east-1.vpce.amazonaws.com", got,
		"an override wins and does not need a region")

	_, err = resolveEndpoint("", "ec2", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "region")

	_, err = resolveEndpoint("not-absolute", "ec2", "us-east-1")
	require.Error(t, err)
}

// Instances on their way out are omitted entirely rather than reported
// not-ready, so they drain from pools before they stop answering.
func TestDepartingInstancesAreOmitted(t *testing.T) {
	f := newFakeEC2(t, responseXML("",
		instanceXML("i-run", "running", "10.0.0.1", "", nil),
		instanceXML("i-pending", "pending", "10.0.0.2", "", nil),
		instanceXML("i-stopping", "stopping", "10.0.0.3", "", nil),
		instanceXML("i-stopped", "stopped", "10.0.0.4", "", nil),
		instanceXML("i-shutting", "shutting-down", "10.0.0.5", "", nil),
		instanceXML("i-term", "terminated", "10.0.0.6", "", nil),
	))
	s := fakeSubscription(t, f.URL, &do.Query{Port: "9090"})
	instances, err := s.describeInstances(t.Context())
	require.NoError(t, err)
	require.Len(t, instances, 6)

	snap, skipped := toMembers(instances, s.mapping)
	require.Empty(t, skipped)
	require.Len(t, snap, 2, "only running and pending instances become members")

	byName := map[string]discovery.ReadyState{}
	for _, m := range snap {
		byName[m.Name] = m.Ready
	}
	require.Equal(t, discovery.Ready, byName["i-run"])
	require.Equal(t, discovery.NotReady, byName["i-pending"],
		"a pending instance is a member, but not a ready one")
}

// An EC2 release that adds a state must not have it read as healthy.
func TestUnknownStateIsNotReady(t *testing.T) {
	require.Equal(t, discovery.NotReady, readyFor("some-future-state"))
	require.False(t, isDeparting("some-future-state"),
		"an unknown state is kept as not-ready rather than silently dropped")
}

// An inventory routinely contains hosts that are simply not tagged yet.
// Excluding them individually keeps a working pool working, where failing
// the refresh would drain it because of one unrelated instance.
func TestUntaggedInstancesAreExcludedNotFatal(t *testing.T) {
	f := newFakeEC2(t, responseXML("",
		instanceXML("i-good", "running", "10.0.0.1", "", map[string]string{"port": "9090"}),
		instanceXML("i-noport", "running", "10.0.0.2", "", nil),
	))
	s := fakeSubscription(t, f.URL, &do.Query{PortLabel: "port"})
	instances, err := s.describeInstances(t.Context())
	require.NoError(t, err)

	snap, skipped := toMembers(instances, s.mapping)
	require.Len(t, snap, 1, "the tagged instance still becomes a member")
	require.Equal(t, "10.0.0.1:9090", snap[0].Address)
	require.Len(t, skipped, 1)
	require.Equal(t, "i-noport", skipped[0].instanceID)
	require.Contains(t, skipped[0].reason, "no port")
}

// A static port is the fallback when an instance lacks the tag, which is
// what makes port_label safe to adopt incrementally.
func TestPortLabelFallsBackToStaticPort(t *testing.T) {
	instances := []ec2Instance{
		{InstanceID: "i-1", PrivateIPAddress: "10.0.0.1",
			Tags: []ec2Tag{{Key: "port", Value: "8080"}}},
		{InstanceID: "i-2", PrivateIPAddress: "10.0.0.2"},
	}
	for i := range instances {
		instances[i].State.Name = "running"
	}
	snap, skipped := toMembers(instances, mapping{
		addressType: do.AddressPrivate, portLabel: "port", port: "9090",
	})
	require.Empty(t, skipped)
	require.Len(t, snap, 2)
	require.Equal(t, "10.0.0.1:8080", snap[0].Address, "the tag wins where present")
	require.Equal(t, "10.0.0.2:9090", snap[1].Address, "the static port is the fallback")
}

func TestInvalidPortTagIsExcluded(t *testing.T) {
	instances := []ec2Instance{{
		InstanceID: "i-1", PrivateIPAddress: "10.0.0.1",
		Tags: []ec2Tag{{Key: "port", Value: "not-a-port"}},
	}}
	instances[0].State.Name = "running"
	snap, skipped := toMembers(instances, mapping{
		addressType: do.AddressPrivate, portLabel: "port",
	})
	require.Empty(t, snap)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "not a port number")
}

// Asking for an address an instance does not have is a misconfiguration
// worth reporting, not a member with an empty host.
func TestMissingAddressIsExcluded(t *testing.T) {
	f := newFakeEC2(t, responseXML("",
		instanceXML("i-private-only", "running", "10.0.0.1", "", nil),
	))
	s := fakeSubscription(t, f.URL,
		&do.Query{Port: "9090", AddressType: do.AddressPublic})
	instances, err := s.describeInstances(t.Context())
	require.NoError(t, err)
	snap, skipped := toMembers(instances, s.mapping)
	require.Empty(t, snap)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "no public address")
}

func TestAddressTypes(t *testing.T) {
	i := ec2Instance{
		PrivateIPAddress: "10.0.0.1",
		PublicIPAddress:  "203.0.113.1",
		NetworkInterface: []ec2NetworkInterface{
			{IPv6Addresses: []ec2IPv6Address{{Address: "2600:1f18::1"}}},
		},
	}
	require.Equal(t, "10.0.0.1", i.address(do.AddressPrivate))
	require.Equal(t, "203.0.113.1", i.address(do.AddressPublic))
	require.Equal(t, "2600:1f18::1", i.address(do.AddressIPv6))

	// an IPv6 member address must be bracketed, or it is not a valid host:port
	i.State.Name = "running"
	snap, skipped := toMembers([]ec2Instance{i},
		mapping{addressType: do.AddressIPv6, port: "9090"})
	require.Empty(t, skipped)
	require.Equal(t, "[2600:1f18::1]:9090", snap[0].Address)
}

// Pagination accumulates: a partial page set is a partial membership, and
// emitting one would drain the pool of everything later pages held.
func TestPaginationAccumulates(t *testing.T) {
	f := newFakeEC2(t,
		responseXML("page-1", instanceXML("i-1", "running", "10.0.0.1", "", nil)),
		responseXML("page-2", instanceXML("i-2", "running", "10.0.0.2", "", nil)),
		responseXML("", instanceXML("i-3", "running", "10.0.0.3", "", nil)),
	)
	s := fakeSubscription(t, f.URL, &do.Query{Port: "9090"})
	instances, err := s.describeInstances(t.Context())
	require.NoError(t, err)
	require.Len(t, instances, 3)
	require.EqualValues(t, 3, f.hits.Load())
}

// A NextToken that never clears must not spin forever inside one poll.
func TestPaginationIsBounded(t *testing.T) {
	looping := responseXML("page-0", instanceXML("i-1", "running", "10.0.0.1", "", nil))
	f := newFakeEC2(t, looping)
	// serve the same page forever, always pointing back at itself
	f.mtx.Lock()
	f.pages = []string{looping}
	f.mtx.Unlock()

	s := fakeSubscription(t, f.URL, &do.Query{Port: "9090"})
	_, err := s.describeInstances(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not terminate")
	require.LessOrEqual(t, f.hits.Load(), int64(maxPages))
}

// The Query-protocol error document carries a far more useful message than
// the status code alone; surfacing it is the difference between "http 403"
// and "UnauthorizedOperation: You are not authorized".
func TestAPIErrorMessageIsSurfaced(t *testing.T) {
	f := newFakeEC2(t, responseXML(""))
	f.setError(http.StatusForbidden, `<?xml version="1.0"?>
<Response><Errors><Error>
  <Code>UnauthorizedOperation</Code>
  <Message>You are not authorized to perform this operation.</Message>
</Error></Errors><RequestID>req-1</RequestID></Response>`)
	s := fakeSubscription(t, f.URL, &do.Query{Port: "9090"})
	_, err := s.describeInstances(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "UnauthorizedOperation")
	require.Contains(t, err.Error(), "not authorized")

	// a non-XML error body still yields the status
	f.setError(http.StatusBadGateway, "gateway down")
	_, err = s.describeInstances(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "502")
}

// Filter.N.Name / Filter.N.Value.M encoding, one-indexed and deterministic.
func TestFilterEncoding(t *testing.T) {
	f := newFakeEC2(t, responseXML(""))
	s := fakeSubscription(t, f.URL, &do.Query{
		Port: "9090",
		Filters: map[string][]string{
			"tag:service":         {"prometheus", "thanos"},
			"instance-state-name": {"running"},
		},
	})
	_, err := s.describeInstances(t.Context())
	require.NoError(t, err)

	form := f.lastForm(t)
	require.Equal(t, "DescribeInstances", form["Action"][0])
	require.Equal(t, ec2APIVersion, form["Version"][0])
	// names are sorted, so instance-state-name precedes tag:service
	require.Equal(t, "instance-state-name", form["Filter.1.Name"][0])
	require.Equal(t, "running", form["Filter.1.Value.1"][0])
	require.Equal(t, "tag:service", form["Filter.2.Name"][0])
	require.Equal(t, "prometheus", form["Filter.2.Value.1"][0])
	require.Equal(t, "thanos", form["Filter.2.Value.2"][0])
}

// A failed refresh keeps the last-good membership and is counted.
func TestFailureKeepsLastGoodAndRecovers(t *testing.T) {
	before := testutil.ToFloat64(
		metrics.DiscoveryRefreshErrors.WithLabelValues("test-aws", "aws"))
	f := newFakeEC2(t, responseXML("",
		instanceXML("i-1", "running", "10.0.0.1", "", nil)))

	p, err := newProvider("test-aws", fakeOptions(f.URL))
	require.NoError(t, err)
	got := make(chan discovery.Snapshot, 8)
	runner, err := p.newSubscription(&do.Query{Port: "9090"},
		func(s discovery.Snapshot) { got <- s })
	require.NoError(t, err)
	runner.Launch(t.Context())
	defer runner.Stop()

	select {
	case snap := <-got:
		require.Len(t, snap, 1)
	case <-time.After(10 * time.Second):
		t.Fatal("no initial snapshot")
	}

	f.setError(http.StatusInternalServerError, "")
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(
			metrics.DiscoveryRefreshErrors.WithLabelValues("test-aws", "aws")) > before
	}, 10*time.Second, 10*time.Millisecond)
	require.Empty(t, got, "a failing API must not replace the membership")
}

func TestSummarizeIsStableAndBounded(t *testing.T) {
	var many []excluded
	for i := range 25 {
		many = append(many, excluded{fmt.Sprintf("i-%02d", i), "no port"})
	}
	got := summarize(many)
	require.Contains(t, got, "and 15 more")
	require.Equal(t, got, summarize(many), "the summary must be stable")

	// order of input must not change the output, so repeated identical
	// exclusions can be suppressed rather than logged every poll
	reversed := make([]excluded, len(many))
	for i, e := range many {
		reversed[len(many)-1-i] = e
	}
	require.Equal(t, got, summarize(reversed))
}
