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
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// AWS services the relay forwards to, named as they are signed for and as their hosts begin
const (
	awsMonitoring = "monitoring"
	awsLogs       = "logs"
	awsEC2        = "ec2"
	awsTagging    = "tagging"
	awsOAM        = "oam"
)

const (
	headerAmzTarget = "X-Amz-Target"
	headerErrorType = "X-Amzn-Errortype"
	cborPathPrefix  = "/service/"
	cborOpSeparator = "/operation/"
	awsHostSuffix   = ".amazonaws.com"

	formAction  = "Action"
	formVersion = "Version"

	errCodeDenied     = "AccessDeniedException"
	errCodeValidation = "ValidationException"
)

// awsTargetPrefixes maps the service shape names that prefix X-Amz-Target and name CBOR paths
var awsTargetPrefixes = map[string]string{
	"GraniteServiceVersion20100801":     awsMonitoring,
	"Logs_20140328":                     awsLogs,
	"ResourceGroupsTaggingAPI_20170126": awsTagging,
}

// awsQueryVersions maps the Version field of an AWS Query request to its service
var awsQueryVersions = map[string]string{
	"2010-08-01": awsMonitoring,
	"2016-11-15": awsEC2,
}

// awsReadOperations are the operations Grafana's CloudWatch data source calls, per service;
// no other is relayed, so Trickster's credentials cannot change anything
var awsReadOperations = map[string]sets.Set[string]{
	awsMonitoring: sets.New([]string{
		"ListMetrics", "GetMetricData", "DescribeAlarms",
		"DescribeAlarmsForMetric", "DescribeAlarmHistory",
	}),
	awsLogs: sets.New([]string{
		"DescribeLogGroups", "GetLogGroupFields", "StartQuery",
		"GetQueryResults", "StopQuery", "GetLogEvents", "ListAnomalies",
		"ListAggregateLogGroupSummaries",
	}),
	awsEC2:     sets.New([]string{"DescribeRegions", "DescribeInstances"}),
	awsTagging: sets.New([]string{"GetResources"}),
	awsOAM:     sets.New([]string{"ListSinks", "ListAttachedLinks"}),
}

var errUnrecognizedAWSRequest = errors.New("trickster relays only AWS API requests from Grafana's CloudWatch data source")

// awsOperation identifies an AWS API request; query is set for the form-encoded AWS Query protocol.
type awsOperation struct {
	service, name string
	query         bool
}

// cloudWatchRelay forwards the AWS API calls Grafana's CloudWatch data source makes alongside
// PromQL to their services, re-signed with the backend's credentials and never cached.
type cloudWatchRelay struct {
	// region is the backend's region, or "" when it is resolved only at runtime
	region   string
	endpoint func(service, region string) *url.URL
}

func newCloudWatchRelay(o *bo.Options) *cloudWatchRelay {
	return &cloudWatchRelay{region: backendRegion(o), endpoint: awsEndpoint}
}

// backendRegion returns a cloudwatch backend's region from its sigv4 block or its origin.
func backendRegion(o *bo.Options) string {
	if o.SigV4 != nil && o.SigV4.Region != "" {
		return o.SigV4.Region
	}
	return po.OriginRegion(po.FlavorCloudWatch, o.OriginURL)
}

func awsEndpoint(service, region string) *url.URL {
	return &url.URL{Scheme: "https", Host: service + "." + region + awsHostSuffix}
}

func (cr *cloudWatchRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	op, err := identifyAWSOperation(r)
	if err != nil {
		writeAWSError(w, op.query, http.StatusBadRequest, errCodeValidation, err.Error())
		return
	}
	if ops, ok := awsReadOperations[op.service]; !ok || !ops.Contains(op.name) {
		writeAWSError(w, op.query, http.StatusForbidden, errCodeDenied,
			fmt.Sprintf("trickster does not relay %s %s", op.service, op.name))
		return
	}
	region, err := cr.targetRegion(r)
	if err != nil {
		writeAWSError(w, op.query, http.StatusBadRequest, errCodeValidation, err.Error())
		return
	}
	u := cr.endpoint(op.service, region)
	if rsc := request.GetResources(r); op.service == awsMonitoring && rsc != nil && rsc.BackendOptions != nil {
		// CloudWatch itself is the backend's origin, which may be a VPC endpoint
		u = &url.URL{Scheme: rsc.BackendOptions.Scheme, Host: rsc.BackendOptions.Host}
	}
	u.Path, u.RawQuery = r.URL.Path, r.URL.RawQuery
	r.URL = u
	r = r.WithContext(taws.WithSigningScope(r.Context(),
		taws.Scope{Service: op.service, Region: region}))
	engines.DoProxy(w, r, true)
}

// targetRegion returns the region to relay to: the backend's, which a client signing for another
// region is refused, or the client's when the backend's is known only at runtime.
func (cr *cloudWatchRelay) targetRegion(r *http.Request) (string, error) {
	cs, signed := taws.ClientScope(r.Context())
	switch {
	case cr.region != "" && signed && cs.Region != cr.region:
		return "", regionMismatch(cs.Region, cr.region)
	case cr.region != "":
		return cr.region, nil
	case signed && taws.ValidRegion(cs.Region):
		return cs.Region, nil
	}
	return "", errors.New("the backend's region is unknown; set sigv4.region")
}

func regionMismatch(client, backend string) error {
	return fmt.Errorf("this trickster backend serves region %s, not %s", backend, client)
}

// cloudWatchRegionCheck refuses a request signed for a region other than the backend's, which
// would otherwise be answered silently from the backend's region.
func cloudWatchRegionCheck(region string) func(*http.Request) error {
	return func(r *http.Request) error {
		if cs, ok := taws.ClientScope(r.Context()); ok && region != "" && cs.Region != region {
			return regionMismatch(cs.Region, region)
		}
		return nil
	}
}

// identifyAWSOperation names the service and operation of an AWS API request, from its CBOR path,
// its X-Amz-Target header, its AWS Query form fields, or its OAM REST path.
func identifyAWSOperation(r *http.Request) (awsOperation, error) {
	query := headers.ProvidesURLEncodedForm(r)
	if r.Method != http.MethodPost {
		return awsOperation{query: query}, errUnrecognizedAWSRequest
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, cborPathPrefix); ok {
		id, name, ok := strings.Cut(rest, cborOpSeparator)
		if svc := awsTargetPrefixes[id]; ok && svc != "" && name != "" {
			return awsOperation{service: svc, name: name}, nil
		}
		return awsOperation{}, errUnrecognizedAWSRequest
	}
	if target := r.Header.Get(headerAmzTarget); target != "" {
		prefix, name, _ := strings.Cut(target, ".")
		if svc := awsTargetPrefixes[prefix]; svc != "" && name != "" {
			return awsOperation{service: svc, name: name}, nil
		}
		return awsOperation{}, errUnrecognizedAWSRequest
	}
	if query {
		v, _, _ := params.GetRequestValues(r)
		if svc := awsQueryVersions[v.Get(formVersion)]; svc != "" && v.Get(formAction) != "" {
			return awsOperation{service: svc, name: v.Get(formAction), query: true}, nil
		}
		return awsOperation{query: true}, errUnrecognizedAWSRequest
	}
	if name := strings.TrimPrefix(r.URL.Path, "/"); awsReadOperations[awsOAM].Contains(name) {
		return awsOperation{service: awsOAM, name: name}, nil
	}
	return awsOperation{}, errUnrecognizedAWSRequest
}

// awsQueryError is the XML error body of the AWS Query protocol.
type awsQueryError struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Type    string   `xml:"Error>Type"`
	Code    string   `xml:"Error>Code"`
	Message string   `xml:"Error>Message"`
}

// writeAWSError answers with an error an AWS SDK can read: XML for the Query protocol, otherwise
// JSON with the code in X-Amzn-Errortype.
func writeAWSError(w http.ResponseWriter, query bool, status int, code, msg string) {
	var body []byte
	if query {
		body, _ = xml.Marshal(awsQueryError{Type: "Sender", Code: code, Message: msg})
		w.Header().Set(headers.NameContentType, "text/xml")
	} else {
		body, _ = json.Marshal(map[string]string{"__type": code, "message": msg})
		w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
		w.Header().Set(headerErrorType, code)
	}
	w.WriteHeader(status)
	w.Write(body)
}
