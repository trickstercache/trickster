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

package influxdb

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/promremote"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/promremote/prompb"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/evictionmethods"
	"github.com/trickstercache/trickster/v2/pkg/cache/filesystem"
	"github.com/trickstercache/trickster/v2/pkg/cache/manager"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"

	"github.com/klauspost/compress/snappy"
	"google.golang.org/protobuf/proto"
)

type promReadTransport struct {
	mu              sync.Mutex
	extents         [][2]int64
	acceptEncodings []string
	bodies          [][]byte
	sampleStepMs    int64
	forceGzip       bool
	omitEncoding    bool
}

func (p *promReadTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	decoded, err := snappy.Decode(nil, body)
	if err != nil {
		return nil, err
	}
	readRequest := &prompb.ReadRequest{}
	if err := proto.Unmarshal(decoded, readRequest); err != nil {
		return nil, err
	}
	if len(readRequest.Queries) != 1 {
		return &http.Response{
			Status:     "400 Bad Request",
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString("one query required")),
			Request:    r,
		}, nil
	}
	query := readRequest.Queries[0]
	p.mu.Lock()
	p.extents = append(p.extents, [2]int64{query.StartTimestampMs, query.EndTimestampMs})
	p.acceptEncodings = append(p.acceptEncodings, r.Header.Get(headers.NameAcceptEncoding))
	p.bodies = append(p.bodies, body)
	p.mu.Unlock()

	// InfluxDB's storage ReadFilter treats the end timestamp as exclusive.
	step := max(p.sampleStepMs, 1)
	samples := make([]*prompb.Sample, 0, (query.EndTimestampMs-query.StartTimestampMs)/step+1)
	for timestamp := query.StartTimestampMs; timestamp < query.EndTimestampMs; timestamp += step {
		samples = append(samples, &prompb.Sample{
			Timestamp: timestamp,
			Value:     float64(timestamp),
		})
	}
	readResponse := &prompb.ReadResponse{Results: []*prompb.QueryResult{{
		Timeseries: []*prompb.TimeSeries{{
			Labels: []*prompb.Label{
				{Name: "__name__", Value: "requests_total"},
				{Name: "job", Value: "api"},
			},
			Samples: samples,
		}},
	}}}
	encoded, err := proto.Marshal(readResponse)
	if err != nil {
		return nil, err
	}
	compressed := snappy.Encode(nil, encoded)
	header := make(http.Header)
	header.Set(headers.NameContentType, promremote.ContentType)
	header.Set(headers.NameContentEncoding, promremote.ContentEncoding)
	// InfluxDB applies its HTTP gzip filter after encoding the Snappy response.
	if p.forceGzip || strings.Contains(r.Header.Get(headers.NameAcceptEncoding), "gzip") {
		var buf bytes.Buffer
		writer := gzip.NewWriter(&buf)
		if _, err := writer.Write(compressed); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		compressed = buf.Bytes()
		header.Set(headers.NameContentEncoding, "gzip")
	}
	if p.omitEncoding {
		header.Del(headers.NameContentEncoding)
	}
	header.Set(headers.NameContentLength, strconv.Itoa(len(compressed)))
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(compressed)),
		ContentLength: int64(len(compressed)),
		Request:       r,
	}, nil
}

func (p *promReadTransport) requestedExtents() [][2]int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][2]int64, len(p.extents))
	copy(out, p.extents)
	return out
}

func encodePromReadRequest(t *testing.T, start, end int64, queries int) []byte {
	t.Helper()
	return encodePromReadRequestWithStep(t, start, end, queries, 15_000)
}

func encodePromReadRequestWithStep(t *testing.T, start, end int64, queries int, stepMs int64) []byte {
	t.Helper()
	readRequest := &prompb.ReadRequest{AcceptedResponseTypes: []prompb.ReadRequest_ResponseType{
		prompb.ReadRequest_SAMPLES,
	}}
	for range queries {
		readRequest.Queries = append(readRequest.Queries, &prompb.Query{
			StartTimestampMs: start,
			EndTimestampMs:   end,
			Matchers: []*prompb.LabelMatcher{{
				Type: prompb.LabelMatcher_EQ, Name: "__name__", Value: "requests_total",
			}},
			Hints: &prompb.ReadHints{StepMs: stepMs},
		})
	}
	encoded, err := proto.Marshal(readRequest)
	if err != nil {
		t.Fatal(err)
	}
	return snappy.Encode(nil, encoded)
}

func decodePromReadResponse(t *testing.T, body []byte) *prompb.ReadResponse {
	t.Helper()
	decoded, err := snappy.Decode(nil, body)
	if err != nil {
		t.Fatal(err)
	}
	response := &prompb.ReadResponse{}
	if err := proto.Unmarshal(decoded, response); err != nil {
		t.Fatal(err)
	}
	return response
}

func newPromReadHandlerTest(t *testing.T) (*Client, *request.Resources, *promReadTransport) {
	t.Helper()
	prototype, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts, _, seed, _, err := tu.NewTestInstance("", prototype.DefaultPathConfigs,
		http.StatusOK, "", nil, providers.InfluxDB, promremote.Path, "error")
	if err != nil {
		t.Fatal(err)
	}
	rsc := request.GetResources(seed)
	clientBackend, err := NewClient("test", rsc.BackendOptions, nil, rsc.CacheClient, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := clientBackend.(*Client)
	transport := &promReadTransport{}
	client.HTTPClient().Transport = transport
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = client.HTTPClient()

	t.Cleanup(func() {
		_ = rsc.CacheClient.Close()
		ts.CloseClientConnections()
		ts.Close()
	})
	return client, rsc, transport
}

func runPromReadHandler(t *testing.T, client *Client, rsc *request.Resources,
	body []byte, noCache ...bool,
) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost,
		"http://trickster.local"+promremote.Path+"?db=metrics&rp=autogen", bytes.NewReader(body))
	r.Header.Set(headers.NameContentType, promremote.ContentType)
	r.Header.Set(headers.NameContentEncoding, promremote.ContentEncoding)
	if len(noCache) > 0 && noCache[0] {
		r.Header.Set(headers.NameCacheControl, "no-cache")
	}
	r = request.SetResources(r, rsc.Clone())
	w := httptest.NewRecorder()
	client.QueryHandler(w, r)
	return w
}

func TestPromRemoteReadDeltaCache(t *testing.T) {
	client, rsc, transport := newPromReadHandlerTest(t)
	start := time.Now().Add(-time.Minute).Truncate(time.Millisecond).UnixMilli()

	first := runPromReadHandler(t, client, rsc, encodePromReadRequest(t, start, start+3, 1))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	if got := decodePromReadResponse(t, first.Body.Bytes()).Results[0].Timeseries[0].Samples; len(got) != 3 {
		t.Fatalf("first sample count = %d", len(got))
	}

	second := runPromReadHandler(t, client, rsc, encodePromReadRequest(t, start, start+4, 1))
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", second.Code, second.Body.String())
	}
	if second.Header().Get(headers.NameContentType) != promremote.ContentType ||
		second.Header().Get(headers.NameContentEncoding) != promremote.ContentEncoding {
		t.Fatalf("second headers = %#v", second.Header())
	}
	if got := decodePromReadResponse(t, second.Body.Bytes()).Results[0].Timeseries[0].Samples; len(got) != 4 {
		t.Fatalf("second sample count = %d", len(got))
	}
	if got := second.Header().Get(headers.NameContentLength); got != "" {
		t.Fatalf("second content length = %s", got)
	}

	third := runPromReadHandler(t, client, rsc, encodePromReadRequest(t, start, start+4, 1))
	if third.Code != http.StatusOK {
		t.Fatalf("third status = %d", third.Code)
	}
	if got := decodePromReadResponse(t, third.Body.Bytes()).Results[0].Timeseries[0].Samples; len(got) != 4 {
		t.Fatalf("third sample count = %d", len(got))
	}
	for _, response := range []*httptest.ResponseRecorder{first, second, third} {
		if response.Header().Get(headers.NameContentType) != promremote.ContentType ||
			response.Header().Get(headers.NameContentEncoding) != promremote.ContentEncoding {
			t.Errorf("wire headers = %v", response.Header())
		}
	}

	extents := transport.requestedExtents()
	want := [][2]int64{{start, start + 3}, {start + 3, start + 4}}
	if len(extents) != len(want) || extents[0] != want[0] || extents[1] != want[1] {
		t.Fatalf("origin extents = %#v, want %#v", extents, want)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, encoding := range transport.acceptEncodings {
		if encoding != promremote.ContentEncoding {
			t.Errorf("upstream Accept-Encoding = %q, want snappy", encoding)
		}
	}
}

func TestPromRemoteReadRebuiltEncoding(t *testing.T) {
	for _, mode := range []string{"gzip", "missing"} {
		t.Run(mode, func(t *testing.T) {
			client, rsc, transport := newPromReadHandlerTest(t)
			transport.forceGzip = mode == "gzip"
			transport.omitEncoding = mode == "missing"
			start := time.Now().Add(-time.Minute).Truncate(time.Millisecond).UnixMilli()
			for _, count := range []int64{3, 4, 4} {
				w := runPromReadHandler(t, client, rsc, encodePromReadRequest(t, start, start+count, 1))
				if w.Code != http.StatusOK || w.Header().Get(headers.NameContentEncoding) != promremote.ContentEncoding {
					t.Fatalf("status = %d, headers = %v, body = %s", w.Code, w.Header(), w.Body.String())
				}
				if got := decodePromReadResponse(t, w.Body.Bytes()).Results[0].Timeseries[0].Samples; int64(len(got)) != count {
					t.Fatalf("samples = %d, want %d", len(got), count)
				}
			}
			if got := transport.requestedExtents(); len(got) != 2 {
				t.Fatalf("origin requests = %v, want miss and partial hit only", got)
			}
		})
	}
}

func TestPromRemoteReadNoCacheEncoding(t *testing.T) {
	client, rsc, transport := newPromReadHandlerTest(t)
	transport.forceGzip = true
	start := time.Now().Add(-time.Minute).Truncate(time.Millisecond).UnixMilli()
	for range 2 {
		w := runPromReadHandler(t, client, rsc, encodePromReadRequest(t, start, start+3, 1), true)
		if w.Code != http.StatusOK || w.Header().Get(headers.NameContentEncoding) != promremote.ContentEncoding {
			t.Fatalf("status = %d, headers = %v", w.Code, w.Header())
		}
		if got := decodePromReadResponse(t, w.Body.Bytes()).Results[0].Timeseries[0].Samples; len(got) != 3 {
			t.Fatalf("sample count = %d, want 3", len(got))
		}
	}
	if got := transport.requestedExtents(); len(got) != 2 {
		t.Fatalf("origin requests = %v, want two uncached reads", got)
	}
}

func TestPromRemoteReadRetention(t *testing.T) {
	for _, provider := range []string{"memory", "filesystem"} {
		for _, method := range []evictionmethods.TimeseriesEvictionMethod{
			evictionmethods.EvictionMethodOldest, evictionmethods.EvictionMethodLRU,
		} {
			t.Run(fmt.Sprintf("%s/%d", provider, method), func(t *testing.T) {
				client, rsc, transport := newPromReadHandlerTest(t)
				transport.sampleStepMs = 15_000
				rsc.BackendOptions.TimeseriesEvictionMethod = method
				rsc.BackendOptions.TimeseriesRetentionFactor = 1024
				if provider == "filesystem" {
					if err := rsc.CacheClient.Close(); err != nil {
						t.Fatal(err)
					}
					opts := cacheoptions.New()
					opts.Name, opts.Provider = t.Name(), provider
					opts.Filesystem.CachePath = t.TempDir()
					cache := manager.NewCache(filesystem.NewCache(t.Name(), opts), manager.CacheOptions{}, opts)
					if err := cache.Connect(); err != nil {
						t.Fatal(err)
					}
					rsc.CacheClient = cache
					rsc.CacheConfig = opts
				}
				start := time.Now().Add(-2 * time.Hour).Truncate(15 * time.Second).UnixMilli()
				end := start + time.Hour.Milliseconds()
				for _, extent := range [][2]int64{{start, end}, {start, end}, {start - 15_000, end}, {start - 15_000, end}} {
					w := runPromReadHandler(t, client, rsc, encodePromReadRequest(t, extent[0], extent[1], 1))
					if w.Code != http.StatusOK {
						t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
					}
					wantCount := (extent[1] - extent[0]) / 15_000
					if got := decodePromReadResponse(t, w.Body.Bytes()).Results[0].Timeseries[0].Samples; int64(len(got)) != wantCount {
						t.Fatalf("samples = %d, want %d", len(got), wantCount)
					}
				}
				got := transport.requestedExtents()
				if len(got) != 2 || got[0] != [2]int64{start, end} || got[1] != [2]int64{start - 15_000, start} {
					t.Fatalf("origin requests = %v, want initial hour and only the missing 15s", got)
				}
			})
		}
	}
}

func TestPromRemoteReadLRUUsesCurrentHint(t *testing.T) {
	client, rsc, transport := newPromReadHandlerTest(t)
	transport.sampleStepMs = 15_000
	rsc.BackendOptions.TimeseriesEvictionMethod = evictionmethods.EvictionMethodLRU
	rsc.BackendOptions.TimeseriesRetentionFactor = 1024
	start := time.Now().Add(-2 * time.Hour).Truncate(15 * time.Second).UnixMilli()
	end := start + time.Hour.Milliseconds()
	first := runPromReadHandler(t, client, rsc, encodePromReadRequestWithStep(t, start, end, 1, 1000))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	// Raw samples share a cache key even when the caller changes the hint.
	// With a 15s hint the hour is below budget, so an earlier range is cacheable.
	for range 2 {
		w := runPromReadHandler(t, client, rsc, encodePromReadRequest(t, start-15_000, start, 1))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if got := decodePromReadResponse(t, w.Body.Bytes()).Results[0].Timeseries[0].Samples; len(got) != 1 {
			t.Fatalf("sample count = %d, want 1", len(got))
		}
	}
	got := transport.requestedExtents()
	if len(got) != 2 || got[0] != [2]int64{start, end} || got[1] != [2]int64{start - 15_000, start} {
		t.Fatalf("origin requests = %v, want the older range cached with the current hint", got)
	}
}

func TestPromRemoteReadMissingStepPassesThrough(t *testing.T) {
	for _, method := range []evictionmethods.TimeseriesEvictionMethod{
		evictionmethods.EvictionMethodOldest, evictionmethods.EvictionMethodLRU,
	} {
		t.Run(fmt.Sprint(method), func(t *testing.T) {
			client, rsc, transport := newPromReadHandlerTest(t)
			rsc.BackendOptions.TimeseriesEvictionMethod = method
			start := time.Now().Add(-time.Minute).Truncate(time.Millisecond).UnixMilli()
			body := encodePromReadRequestWithStep(t, start, start+3, 1, 0)
			for range 2 {
				w := runPromReadHandler(t, client, rsc, body)
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
				}
			}
			transport.mu.Lock()
			defer transport.mu.Unlock()
			if len(transport.bodies) != 2 {
				t.Fatalf("origin requests = %d, want two passthrough requests", len(transport.bodies))
			}
			for _, got := range transport.bodies {
				if !bytes.Equal(got, body) {
					t.Fatal("passthrough rewrote the original request body")
				}
			}
		})
	}
}

func TestPromRemoteReadUnsupportedShapePassesThrough(t *testing.T) {
	client, rsc, transport := newPromReadHandlerTest(t)
	start := time.Now().Add(-time.Minute).Truncate(time.Millisecond).UnixMilli()
	w := runPromReadHandler(t, client, rsc, encodePromReadRequest(t, start, start+1, 2))
	if w.Code != http.StatusBadRequest || w.Body.String() != "one query required" {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	if len(transport.requestedExtents()) != 0 {
		t.Fatal("unsupported request was parsed as a cacheable query")
	}
}

func TestPromRemoteReadPointShardingPassesThrough(t *testing.T) {
	client, rsc, transport := newPromReadHandlerTest(t)
	rsc.BackendOptions.MaxShardSizePoints = 1
	rsc.BackendOptions.DoesShard = true
	start := time.Now().Add(-time.Minute).Truncate(time.Millisecond).UnixMilli()
	body := encodePromReadRequest(t, start, start+2, 1)

	for range 2 {
		w := runPromReadHandler(t, client, rsc, body)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
	}
	extents := transport.requestedExtents()
	want := [2]int64{start, start + 2}
	if len(extents) != 2 || extents[0] != want || extents[1] != want {
		t.Fatalf("origin extents = %#v, want two passthrough requests for %#v", extents, want)
	}
}
