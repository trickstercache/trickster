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

package promremote

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/promremote/prompb"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/proto"
)

func encodeReadRequest(t *testing.T, readRequest *prompb.ReadRequest) []byte {
	t.Helper()
	b, err := proto.Marshal(readRequest)
	if err != nil {
		t.Fatal(err)
	}
	return snappy.Encode(nil, b)
}

func decodeReadRequest(t *testing.T, body []byte) *prompb.ReadRequest {
	t.Helper()
	b, err := snappy.Decode(nil, body)
	if err != nil {
		t.Fatal(err)
	}
	readRequest := &prompb.ReadRequest{}
	if err := proto.Unmarshal(b, readRequest); err != nil {
		t.Fatal(err)
	}
	return readRequest
}

func newReadRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost,
		"http://example.com/api/v1/prom/read?db=metrics&rp=autogen", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set(headers.NameContentType, ContentType)
	r.Header.Set(headers.NameContentEncoding, ContentEncoding)
	return r
}

func sampleReadRequest(start, end, step int64) *prompb.ReadRequest {
	return &prompb.ReadRequest{
		Queries: []*prompb.Query{{
			StartTimestampMs: start,
			EndTimestampMs:   end,
			Matchers: []*prompb.LabelMatcher{
				{Type: prompb.LabelMatcher_EQ, Name: "job", Value: "api"},
				{Type: prompb.LabelMatcher_RE, Name: "__name__", Value: "http_.+"},
			},
			Hints: &prompb.ReadHints{StepMs: step, Func: "rate"},
		}},
		AcceptedResponseTypes: []prompb.ReadRequest_ResponseType{prompb.ReadRequest_SAMPLES},
	}
}

func TestParseTimeRangeQuery(t *testing.T) {
	const (
		start = int64(1_700_000_000_001)
		end   = int64(1_700_000_060_001)
		step  = int64(15_000)
	)
	body := encodeReadRequest(t, sampleReadRequest(start, end, step))
	r := newReadRequest(t, body)

	trq, rlo, canObjectCache, err := ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	if canObjectCache {
		t.Fatal("remote read must not fall back to object caching")
	}
	if trq.Extent.Start.UnixMilli() != start || trq.Extent.End.UnixMilli() != end-1 {
		t.Fatalf("extent = %d..%d", trq.Extent.Start.UnixMilli(), trq.Extent.End.UnixMilli())
	}
	if trq.Step != time.Millisecond {
		t.Fatalf("step = %s", trq.Step)
	}
	if trq.PolicyStep != 15*time.Second {
		t.Fatalf("policy step = %s", trq.PolicyStep)
	}
	if !bytes.Equal(trq.OriginalBody, body) {
		t.Fatal("original body was not preserved")
	}
	if !IsParsedQuery(trq.ParsedQuery) {
		t.Fatalf("parsed query type = %T", trq.ParsedQuery)
	}
	if rlo == nil || iofmt.Format(rlo.OutputFormat) != iofmt.PromRemoteRead ||
		!rlo.FastForwardDisable {
		t.Fatalf("request options = %#v", rlo)
	}
	if trq.CacheKeyElements[cacheKeyQuery] != trq.Statement || trq.Statement == "" {
		t.Fatalf("canonical query = %q", trq.Statement)
	}

	gotBody, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatal("parsing did not reset the request body")
	}
}

func TestParseTimeRangeQueryCanonicalizesMatchers(t *testing.T) {
	first := sampleReadRequest(100, 200, 15)
	second := sampleReadRequest(300, 400, 30)
	second.Queries[0].Matchers[0], second.Queries[0].Matchers[1] =
		second.Queries[0].Matchers[1], second.Queries[0].Matchers[0]

	trq1, _, _, err := ParseTimeRangeQuery(newReadRequest(t, encodeReadRequest(t, first)))
	if err != nil {
		t.Fatal(err)
	}
	trq2, _, _, err := ParseTimeRangeQuery(newReadRequest(t, encodeReadRequest(t, second)))
	if err != nil {
		t.Fatal(err)
	}
	if trq1.Statement != trq2.Statement {
		t.Fatalf("equivalent matchers produced different keys:\n%s\n%s", trq1.Statement, trq2.Statement)
	}
}

func TestParseTimeRangeQueryUnsupportedBodies(t *testing.T) {
	tests := map[string][]byte{
		"not snappy": []byte("not-snappy"),
		"no query":   encodeReadRequest(t, &prompb.ReadRequest{}),
		"two queries": encodeReadRequest(t, &prompb.ReadRequest{Queries: []*prompb.Query{
			{Matchers: []*prompb.LabelMatcher{{Name: "a"}}},
			{Matchers: []*prompb.LabelMatcher{{Name: "b"}}},
		}}),
		"no matcher": encodeReadRequest(t, &prompb.ReadRequest{Queries: []*prompb.Query{{}}}),
		"invalid extent": encodeReadRequest(t, &prompb.ReadRequest{Queries: []*prompb.Query{{
			StartTimestampMs: 2,
			EndTimestampMs:   1,
			Matchers:         []*prompb.LabelMatcher{{Name: "a"}},
		}}}),
		"empty extent": encodeReadRequest(t, &prompb.ReadRequest{Queries: []*prompb.Query{{
			StartTimestampMs: 1,
			EndTimestampMs:   1,
			Matchers:         []*prompb.LabelMatcher{{Name: "a"}},
		}}}),
		"chunks only": encodeReadRequest(t, &prompb.ReadRequest{
			Queries: []*prompb.Query{{Matchers: []*prompb.LabelMatcher{{Name: "a"}}}},
			AcceptedResponseTypes: []prompb.ReadRequest_ResponseType{
				prompb.ReadRequest_STREAMED_XOR_CHUNKS,
			},
		}),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			trq, _, _, err := ParseTimeRangeQuery(newReadRequest(t, body))
			if err == nil {
				t.Fatal("expected an error")
			}
			if trq != nil && !bytes.Equal(trq.OriginalBody, body) {
				t.Fatal("error path did not preserve the original body")
			}
		})
	}
}

func TestParseTimeRangeQueryDecodeLimit(t *testing.T) {
	body := binary.AppendUvarint(nil, 17)
	r := newReadRequest(t, body)
	r = request.SetResources(r, request.NewResources(
		&bo.Options{MaxCaptureBytes: 16}, nil, nil, nil, nil, nil,
	))

	trq, _, _, err := ParseTimeRangeQuery(r)
	if !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("error = %v", err)
	}
	if trq == nil || !bytes.Equal(trq.OriginalBody, body) {
		t.Fatal("oversized body was not preserved for proxy fallback")
	}
}

func TestParseTimeRangeQueryPointPolicyFallbacks(t *testing.T) {
	tests := map[string]struct {
		options *bo.Options
		step    int64
		want    error
	}{
		"point sharding": {
			options: &bo.Options{MaxShardSizePoints: 100},
			step:    15_000,
			want:    errPointSharding,
		},
		"backfill points without hints": {
			options: &bo.Options{BackfillTolerancePoints: 2},
			want:    errPointPolicyStep,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			r := newReadRequest(t, encodeReadRequest(t, sampleReadRequest(100, 200, test.step)))
			r = request.SetResources(r, request.NewResources(
				test.options, nil, nil, nil, nil, nil,
			))
			trq, _, _, err := ParseTimeRangeQuery(r)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
			if trq == nil || len(trq.OriginalBody) == 0 {
				t.Fatal("fallback did not preserve the original body")
			}
		})
	}
}

func TestSetExtent(t *testing.T) {
	original := sampleReadRequest(100, 200, 15)
	body := encodeReadRequest(t, original)
	r := newReadRequest(t, body)
	trq, _, _, err := ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	extent := &timeseries.Extent{Start: time.UnixMilli(125), End: time.UnixMilli(175)}
	if err := SetExtent(r, trq, extent); err != nil {
		t.Fatal(err)
	}
	rewrittenBody, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := decodeReadRequest(t, rewrittenBody)
	if rewritten.Queries[0].StartTimestampMs != 125 || rewritten.Queries[0].EndTimestampMs != 176 {
		t.Fatalf("rewritten extent = %d..%d", rewritten.Queries[0].StartTimestampMs,
			rewritten.Queries[0].EndTimestampMs)
	}
	parsed := trq.ParsedQuery.(*parsedRequest).readRequest
	if parsed.Queries[0].StartTimestampMs != 100 || parsed.Queries[0].EndTimestampMs != 200 {
		t.Fatal("extent rewrite mutated the parsed request")
	}
	if r.ContentLength != int64(len(rewrittenBody)) {
		t.Fatalf("content length = %d, body = %d", r.ContentLength, len(rewrittenBody))
	}
}

func TestIsRequest(t *testing.T) {
	standard, _ := http.NewRequest(http.MethodPost, "http://example.com/base"+Path, nil)
	if !IsRequest(standard) {
		t.Fatal("standard path was not detected")
	}
	custom, _ := http.NewRequest(http.MethodPost, "http://example.com/custom", nil)
	custom.Header.Set(headers.NameContentType, ContentType+"; proto=prometheus.ReadRequest")
	custom.Header.Set(headers.NameContentEncoding, ContentEncoding)
	if !IsRequest(custom) {
		t.Fatal("protocol headers were not detected")
	}
	get, _ := http.NewRequest(http.MethodGet, "http://example.com"+Path, nil)
	if IsRequest(get) {
		t.Fatal("GET must not be detected as remote read")
	}
}
