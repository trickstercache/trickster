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

package prometheus

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"github.com/stretchr/testify/require"
)

const (
	formListMetrics     = "Action=ListMetrics&Version=2010-08-01&Namespace=AWS%2FBilling"
	formDescribeRegions = "Action=DescribeRegions&Version=2016-11-15"
	jsonEmpty           = "{}"
)

func awsRequest(method, path, body string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(method, "http://trickster"+path, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func form() map[string]string {
	return map[string]string{headers.NameContentType: headers.ValueXFormURLEncoded}
}

func target(t string) map[string]string {
	return map[string]string{headerAmzTarget: t, headers.NameContentType: "application/x-amz-json-1.1"}
}

func TestIdentifyAWSOperation(t *testing.T) {
	tests := []struct {
		name string
		r    *http.Request
		want awsOperation
		err  bool
	}{
		{
			"query cloudwatch", awsRequest(http.MethodPost, "/", formListMetrics, form()),
			awsOperation{service: awsMonitoring, name: "ListMetrics", query: true},
			false,
		},
		{
			"query ec2", awsRequest(http.MethodPost, "/", formDescribeRegions, form()),
			awsOperation{service: awsEC2, name: "DescribeRegions", query: true},
			false,
		},
		{
			"json cloudwatch", awsRequest(http.MethodPost, "/", jsonEmpty,
				target("GraniteServiceVersion20100801.GetMetricData")),
			awsOperation{service: awsMonitoring, name: "GetMetricData"},
			false,
		},
		{
			"json logs", awsRequest(http.MethodPost, "/", jsonEmpty, target("Logs_20140328.DescribeLogGroups")),
			awsOperation{service: awsLogs, name: "DescribeLogGroups"},
			false,
		},
		{
			"json tagging", awsRequest(http.MethodPost, "/", jsonEmpty,
				target("ResourceGroupsTaggingAPI_20170126.GetResources")),
			awsOperation{service: awsTagging, name: "GetResources"},
			false,
		},
		{
			"cbor cloudwatch", awsRequest(http.MethodPost,
				"/service/GraniteServiceVersion20100801/operation/ListMetrics", "", nil),
			awsOperation{service: awsMonitoring, name: "ListMetrics"},
			false,
		},
		{
			"oam rest", awsRequest(http.MethodPost, "/ListSinks", jsonEmpty, nil),
			awsOperation{service: awsOAM, name: "ListSinks"},
			false,
		},
		{"get", awsRequest(http.MethodGet, "/", "", nil), awsOperation{}, true},
		{
			"unknown target", awsRequest(http.MethodPost, "/", jsonEmpty, target("DynamoDB_20120810.Scan")),
			awsOperation{},
			true,
		},
		{"unknown query version", awsRequest(http.MethodPost, "/", "Action=ListQueues&Version=2012-11-05",
			form()), awsOperation{query: true}, true},
		{
			"unknown cbor service", awsRequest(http.MethodPost, "/service/Other/operation/Get", "", nil),
			awsOperation{},
			true,
		},
		{"unknown rest path", awsRequest(http.MethodPost, "/CreateSink", jsonEmpty, nil), awsOperation{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := identifyAWSOperation(tc.r)
			require.Equal(t, tc.err, err != nil, err)
			require.Equal(t, tc.want, got)
		})
	}
	// identifying a Query request leaves its body for the relay to forward
	r := awsRequest(http.MethodPost, "/", formListMetrics, form())
	_, err := identifyAWSOperation(r)
	require.NoError(t, err)
	b, _ := io.ReadAll(r.Body)
	require.Equal(t, formListMetrics, string(b))
}

// awsOrigin records what each relayed request looked like when it arrived.
type awsOrigin struct {
	mtx    sync.Mutex
	scopes []taws.Scope
	bodies []string
	paths  []string
}

func (a *awsOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s, _ := taws.ParseScope(r.Header.Get(headers.NameAuthorization))
	a.mtx.Lock()
	a.scopes, a.bodies, a.paths = append(a.scopes, s), append(a.bodies, string(b)), append(a.paths, r.URL.Path)
	a.mtx.Unlock()
	w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	w.Write([]byte(`{"relayed":true}`))
}

// relayClient returns a cloudwatch-flavored client signing with static keys for us-east-1, whose
// origin and every relayed service resolve to srv.
func relayClient(t *testing.T, srv *httptest.Server) (*Client, *bo.Options) {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	o := bo.New()
	o.Name, o.OriginURL, o.Scheme, o.Host = "cw", srv.URL, u.Scheme, u.Host
	o.Prometheus = &po.Options{Flavor: po.FlavorCloudWatch}
	o.SigV4 = &taws.Options{
		Region: "us-east-1", AccessKey: "AKIATRICKSTER", SecretKey: "s",
		Service: po.CloudWatchSigningService,
	}
	b, err := NewClient("cw", o, nil, nil, nil, nil)
	require.NoError(t, err)
	c := b.(*Client)
	o.HTTPClient = c.HTTPClient()
	c.hooks.CatchAll.(*cloudWatchRelay).endpoint = func(string, string) *url.URL {
		return &url.URL{Scheme: u.Scheme, Host: u.Host}
	}
	return c, o
}

func serveRelay(c *Client, o *bo.Options, r *http.Request, client *taws.Scope) *httptest.ResponseRecorder {
	if client != nil {
		r = r.WithContext(taws.WithClientScope(r.Context(), *client))
	}
	pc := bo.New().FastForwardPath
	for _, p := range c.DefaultPathConfigs(o) {
		if p.Path == rootPath {
			pc = p
		}
	}
	r = request.SetResources(r, request.NewResources(o, pc, nil, nil, c, nil))
	w := httptest.NewRecorder()
	c.HandlerLookup()[handlerCatchAll].ServeHTTP(w, r)
	return w
}

func TestCloudWatchRelayForwardsReadCalls(t *testing.T) {
	origin := &awsOrigin{}
	srv := httptest.NewServer(origin)
	defer srv.Close()
	c, o := relayClient(t, srv)
	usEast1 := &taws.Scope{Service: awsLogs, Region: "us-east-1"}

	for _, tc := range []struct {
		r       *http.Request
		service string
	}{
		{awsRequest(http.MethodPost, "/", formListMetrics, form()), awsMonitoring},
		{awsRequest(http.MethodPost, "/", `{"limit":1}`, target("Logs_20140328.DescribeLogGroups")), awsLogs},
		{awsRequest(http.MethodPost, "/", formDescribeRegions, form()), awsEC2},
		{awsRequest(http.MethodPost, "/ListSinks", jsonEmpty, nil), awsOAM},
	} {
		w := serveRelay(c, o, tc.r, usEast1)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, `{"relayed":true}`, w.Body.String())
		last := len(origin.scopes) - 1
		require.Equal(t, taws.Scope{Service: tc.service, Region: "us-east-1"}, origin.scopes[last],
			"each call is re-signed for its own service")
	}
	require.Equal(t, formListMetrics, origin.bodies[0], "the body is forwarded intact")
	require.Equal(t, "/ListSinks", origin.paths[3])
}

func TestCloudWatchRelayRefusesWritesAndOtherRegions(t *testing.T) {
	origin := &awsOrigin{}
	srv := httptest.NewServer(origin)
	defer srv.Close()
	c, o := relayClient(t, srv)

	w := serveRelay(c, o, awsRequest(http.MethodPost, "/", jsonEmpty,
		target("GraniteServiceVersion20100801.PutMetricData")), nil)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, errCodeDenied, w.Header().Get(headerErrorType))

	w = serveRelay(c, o, awsRequest(http.MethodPost, "/",
		"Action=PutMetricData&Version=2010-08-01", form()), nil)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "<Code>"+errCodeDenied+"</Code>", "a Query client gets XML")

	w = serveRelay(c, o, awsRequest(http.MethodPost, "/", formListMetrics, form()),
		&taws.Scope{Service: awsMonitoring, Region: "us-west-2"})
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "serves region us-east-1")

	w = serveRelay(c, o, awsRequest(http.MethodGet, "/", "", nil), nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Empty(t, origin.scopes, "no refused call reaches AWS")
}

// A PromQL request signed for another region is refused rather than answered from the backend's.
func TestCloudWatchPromQLRefusesOtherRegions(t *testing.T) {
	c := flavorClient(t, po.FlavorCloudWatch)
	r := httptest.NewRequest(http.MethodGet, "http://trickster/api/v1/query_range?query=up", nil)
	r = r.WithContext(taws.WithClientScope(context.Background(),
		taws.Scope{Service: awsMonitoring, Region: "eu-west-1"}))
	w := httptest.NewRecorder()
	c.HandlerLookup()[mnQueryRange].ServeHTTP(w, r)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "serves region us-east-1, not eu-west-1")
}
