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

// Package promremote models InfluxDB's Prometheus remote-read endpoint.
package promremote

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/promremote/prompb"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/proto"
)

const (
	// Path is InfluxDB's Prometheus remote-read endpoint.
	Path = "/api/v1/prom/read"

	// ContentType is the Prometheus remote-read protobuf media type.
	ContentType = "application/x-protobuf"
	// ContentEncoding is the Prometheus remote-read compression scheme.
	ContentEncoding = "snappy"

	cacheKeyQuery = "prom_remote_read_query"
)

var (
	errInvalidExtent     = errors.New("invalid Prometheus remote-read extent")
	errMissingMatcher    = errors.New("prometheus remote-read query requires at least one matcher")
	errPointSharding     = errors.New("prometheus remote-read cannot safely use point-count sharding")
	errPointPolicyStep   = errors.New("prometheus remote-read point policies require a positive query step hint")
	errSingleQuery       = errors.New("the InfluxDB Prometheus remote-read endpoint supports exactly one query")
	errUnsupportedResult = errors.New("prometheus remote-read request does not accept sample responses")
)

type canonicalMatcher struct {
	Type  prompb.LabelMatcher_Type `json:"type"`
	Name  string                   `json:"name"`
	Value string                   `json:"value"`
}

type parsedRequest struct {
	readRequest *prompb.ReadRequest
	decodeLimit int
}

// IsRequest reports whether r targets InfluxDB's Prometheus remote-read protocol.
func IsRequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodPost {
		return false
	}
	if r.URL != nil && strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), Path) {
		return true
	}
	contentType := strings.TrimSpace(strings.Split(r.Header.Get(headers.NameContentType), ";")[0])
	return strings.EqualFold(contentType, ContentType) &&
		strings.EqualFold(strings.TrimSpace(r.Header.Get(headers.NameContentEncoding)), ContentEncoding)
}

// IsParsedQuery reports whether q is a parsed Prometheus remote-read request.
func IsParsedQuery(q any) bool {
	parsed, ok := q.(*parsedRequest)
	return ok && parsed != nil && parsed.readRequest != nil
}

// ParseTimeRangeQuery converts a remote-read body into Trickster's time-range model.
func ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error,
) {
	body, err := request.GetBody(r)
	if err != nil {
		return nil, nil, false, fmt.Errorf("read Prometheus remote-read request: %w", err)
	}
	trq := &timeseries.TimeRangeQuery{OriginalBody: body, Step: time.Millisecond}

	decodeLimit := requestDecodeLimit(r)
	decoded, err := decodeSnappyBlock(body, decodeLimit)
	if err != nil {
		return trq, nil, false, fmt.Errorf("decode Prometheus remote-read request: %w", err)
	}
	readRequest := &prompb.ReadRequest{}
	if err := proto.Unmarshal(decoded, readRequest); err != nil {
		return trq, nil, false, fmt.Errorf("unmarshal Prometheus remote-read request: %w", err)
	}
	if len(readRequest.Queries) != 1 || readRequest.Queries[0] == nil {
		return trq, nil, false, errSingleQuery
	}
	if !acceptsSamples(readRequest.AcceptedResponseTypes) {
		return trq, nil, false, errUnsupportedResult
	}

	query := readRequest.Queries[0]
	// InfluxDB's storage read range is [start, end), while Trickster models
	// cached extents as inclusive. Empty ranges cannot be represented safely.
	if query.EndTimestampMs <= query.StartTimestampMs {
		return trq, nil, false, errInvalidExtent
	}
	statement, err := canonicalQuery(query)
	if err != nil {
		return trq, nil, false, err
	}

	trq.Statement = statement
	trq.Extent = timeseries.Extent{
		Start: time.UnixMilli(query.StartTimestampMs),
		End:   time.UnixMilli(query.EndTimestampMs - 1),
	}
	trq.ParsedQuery = &parsedRequest{readRequest: readRequest, decodeLimit: decodeLimit}
	trq.CacheKeyElements = map[string]string{cacheKeyQuery: statement}
	if query.Hints != nil && query.Hints.StepMs > 0 &&
		query.Hints.StepMs <= int64((1<<63-1)/time.Millisecond) {
		trq.PolicyStep = time.Duration(query.Hints.StepMs) * time.Millisecond
	}
	if rsc := request.GetResources(r); rsc != nil && rsc.BackendOptions != nil {
		if rsc.BackendOptions.MaxShardSizePoints > 0 {
			return trq, nil, false, errPointSharding
		}
		if rsc.BackendOptions.BackfillTolerancePoints > 0 && trq.PolicyStep <= 0 {
			return trq, nil, false, errPointPolicyStep
		}
	}

	rlo := &timeseries.RequestOptions{
		OutputFormat:       byte(iofmt.PromRemoteRead),
		FastForwardDisable: true,
		ProviderRequest:    readRequest,
	}
	return trq, rlo, false, nil
}

func acceptsSamples(types []prompb.ReadRequest_ResponseType) bool {
	return len(types) == 0 || slices.Contains(types, prompb.ReadRequest_SAMPLES)
}

func canonicalQuery(query *prompb.Query) (string, error) {
	if len(query.Matchers) == 0 {
		return "", errMissingMatcher
	}
	matchers := make([]canonicalMatcher, len(query.Matchers))
	for i, matcher := range query.Matchers {
		if matcher == nil {
			return "", errMissingMatcher
		}
		matchers[i] = canonicalMatcher{
			Type:  matcher.Type,
			Name:  matcher.Name,
			Value: matcher.Value,
		}
	}
	slices.SortFunc(matchers, func(a, b canonicalMatcher) int {
		if c := cmp.Compare(a.Type, b.Type); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.Value, b.Value)
	})
	b, err := json.Marshal(matchers)
	if err != nil {
		return "", fmt.Errorf("marshal canonical Prometheus remote-read query: %w", err)
	}
	return string(b), nil
}

// SetExtent rewrites a remote-read request body to the supplied inclusive extent.
func SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent,
) error {
	if r == nil || trq == nil || extent == nil {
		return errInvalidExtent
	}
	parsed, ok := trq.ParsedQuery.(*parsedRequest)
	if !ok || parsed == nil || parsed.readRequest == nil {
		return errSingleQuery
	}
	clone := proto.Clone(parsed.readRequest).(*prompb.ReadRequest)
	if len(clone.Queries) != 1 || clone.Queries[0] == nil {
		return errSingleQuery
	}
	end := extent.End.UnixMilli()
	if end == math.MaxInt64 {
		return errInvalidExtent
	}
	clone.Queries[0].StartTimestampMs = extent.Start.UnixMilli()
	clone.Queries[0].EndTimestampMs = end + 1

	encoded, err := proto.Marshal(clone)
	if err != nil {
		return fmt.Errorf("marshal Prometheus remote-read request: %w", err)
	}
	request.SetBody(r, snappy.Encode(nil, encoded))
	return nil
}
