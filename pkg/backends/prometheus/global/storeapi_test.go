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

package global

import (
	"context"
	"net"
	"net/url"
	"sync"
	"testing"

	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"
)

const storeTestTarget = "passthrough:///storeapi"

// storeRequest is the part of a SeriesRequest the test store checks
type storeRequest struct {
	minTime, maxTime, step, rangeMs int64
	matchers                        [][3]string
	aggregates                      []byte
	partialDisabled                 bool
	strategy                        uint64
}

func decodeTestSeriesRequest(t *testing.T, b []byte) storeRequest {
	t.Helper()
	var req storeRequest
	require.NoError(t, forEachField(b, func(num protowire.Number, v []byte) error {
		switch num {
		case seriesRequestMatchers:
			var m [3]string
			require.NoError(t, forEachField(v, func(num protowire.Number, v []byte) error {
				if num == matcherName {
					m[1] = string(v)
				} else if num == matcherValue {
					m[2] = string(v)
				}
				return nil
			}, func(num protowire.Number, v uint64) { m[0] = labels.MatchType(v).String() }))
			req.matchers = append(req.matchers, m)
		case seriesRequestAggregates:
			req.aggregates = v
		}
		return nil
	}, func(num protowire.Number, v uint64) {
		switch num {
		case seriesRequestMinTime:
			req.minTime = int64(v)
		case seriesRequestMaxTime:
			req.maxTime = int64(v)
		case seriesRequestPartialDisabled:
			req.partialDisabled = protowire.DecodeBool(v)
		case seriesRequestPartialStrategy:
			req.strategy = v
		case seriesRequestStep:
			req.step = int64(v)
		case seriesRequestRange:
			req.rangeMs = int64(v)
		}
	}))
	return req
}

func encodeTestChunk(enc uint64, data []byte, raw bool) []byte {
	var chunk []byte
	// proto3 writes no type for XOR, its zero value
	if enc != storeChunkXOR {
		chunk = protowire.AppendTag(chunk, chunkType, protowire.VarintType)
		chunk = protowire.AppendVarint(chunk, enc)
	}
	chunk = protowire.AppendTag(chunk, chunkData, protowire.BytesType)
	chunk = protowire.AppendBytes(chunk, data)
	field := protowire.Number(aggrChunkRaw)
	if !raw {
		// the count aggregate of a downsampled chunk
		field = aggrChunkRaw + 1
	}
	aggr := protowire.AppendTag(nil, field, protowire.BytesType)
	return protowire.AppendBytes(aggr, chunk)
}

// encodeTestSeries writes a SeriesResponse holding a series, its labels in the order given
func encodeTestSeries(kv []string, chunks ...[]byte) []byte {
	var series []byte
	for i := 0; i < len(kv); i += 2 {
		l := protowire.AppendTag(nil, labelName, protowire.BytesType)
		l = protowire.AppendString(l, kv[i])
		l = protowire.AppendTag(l, labelValue, protowire.BytesType)
		l = protowire.AppendString(l, kv[i+1])
		series = protowire.AppendTag(series, seriesLabels, protowire.BytesType)
		series = protowire.AppendBytes(series, l)
	}
	for _, c := range chunks {
		series = protowire.AppendTag(series, seriesChunks, protowire.BytesType)
		series = protowire.AppendBytes(series, c)
	}
	return protowire.AppendBytes(protowire.AppendTag(nil, seriesResponseSeries, protowire.BytesType), series)
}

func encodeTestResponse(field protowire.Number, v []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, field, protowire.BytesType), v)
}

// testStore is an in-process StoreAPI answering every Series call with frames, or err
type testStore struct {
	frames [][]byte
	err    error

	mu       sync.Mutex
	requests []storeRequest
	md       metadata.MD
}

func (s *testStore) serve(t *testing.T) *StoreAPI {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.ForceServerCodecV2(rawCodec{}),
		grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
			method, _ := grpc.MethodFromServerStream(stream)
			if method != storeAPISeriesMethod {
				return status.Error(codes.Unimplemented, method)
			}
			var req rawFrame
			if err := stream.RecvMsg(&req); err != nil {
				return err
			}
			md, _ := metadata.FromIncomingContext(stream.Context())
			s.mu.Lock()
			s.requests = append(s.requests, decodeTestSeriesRequest(t, req.data))
			s.md = md
			s.mu.Unlock()
			if s.err != nil {
				return s.err
			}
			for _, f := range s.frames {
				if err := stream.SendMsg(&rawFrame{data: f}); err != nil {
					return err
				}
			}
			return nil
		}))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	pc := po.New()
	pc.Path = testQueryPath
	pc.RequestHeaders = map[string]string{testConfigHeader: testConfigValue}
	source, err := newStoreAPI(storeTestTarget, po.List{pc}, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = source.Close() })
	return source
}

func xorData(t *testing.T, points ...testPoint) []byte {
	t.Helper()
	return xorChunk(t, points...).Data
}

func TestStoreAPISelect(t *testing.T) {
	podA := []string{testPod, "a", labels.MetricName, testMetric}
	store := &testStore{frames: [][]byte{
		encodeTestSeries(podA, encodeTestChunk(storeChunkXOR, xorData(t, testPoint{t: 1, f: 1}, testPoint{t: 2, f: 2}), true)),
		encodeTestResponse(seriesResponseHints, []byte{}),
		encodeTestSeries(podA, encodeTestChunk(storeChunkXOR, xorData(t, testPoint{t: 3, f: 3}), true)),
		encodeTestSeries([]string{labels.MetricName, testMetric, testPod, "b"},
			encodeTestChunk(storeChunkHistogram, histogramChunk(t, 5, 6).Data, true)),
		encodeTestResponse(seriesResponseWarning, []byte("store x is missing blocks")),
	}}
	source := store.serve(t)
	require.Equal(t, prop.SeriesAPIStoreAPI, source.SeriesAPI())
	matchers := []*labels.Matcher{
		labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric),
		labels.MustNewMatcher(labels.MatchNotRegexp, testPod, "z"),
	}
	budget := &Budget{}
	series, warnings, err := source.Select(testContext(t, budget), 10, 20,
		&storage.SelectHints{Step: 15, Range: 300}, matchers...)
	require.NoError(t, err)

	require.Len(t, series, 2)
	require.Equal(t, labels.FromStrings(labels.MetricName, testMetric, testPod, "a"), series[0].Labels())
	require.Equal(t, []testPoint{{t: 1, f: 1}, {t: 2, f: 2}, {t: 3, f: 3}}, readPoints(t, series[0]))
	require.Equal(t, []testPoint{{t: 5, h: true}, {t: 6, h: true}}, readPoints(t, series[1]))
	require.Len(t, warnings, 1)
	require.ErrorContains(t, warnings.AsErrors()[0], "missing blocks")
	series2, samples := budget.Fetched()
	require.Equal(t, int64(2), series2)
	require.Equal(t, int64(5), samples)

	require.Len(t, store.requests, 1)
	req := store.requests[0]
	require.Equal(t, storeRequest{
		minTime: 10, maxTime: 20, step: 15, rangeMs: 300,
		matchers:        [][3]string{{"=", labels.MetricName, testMetric}, {"!~", testPod, "z"}},
		aggregates:      []byte{aggrRaw},
		partialDisabled: true, strategy: partialResponseStrategyAbort,
	}, req)
	require.Equal(t, []string{testTenant}, store.md.Get(testTenantHeader))
	require.Equal(t, []string{testConfigValue}, store.md.Get(testConfigHeader))
	require.Empty(t, store.md.Get("if-none-match"))
	require.Empty(t, store.md.Get("connection"))
}

func TestStoreAPIErrors(t *testing.T) {
	series := func(enc uint64, raw bool) []byte {
		return encodeTestSeries([]string{labels.MetricName, testMetric},
			encodeTestChunk(enc, xorData(t, testPoint{t: 1, f: 1}, testPoint{t: 2, f: 2}), raw))
	}
	tests := []struct {
		name   string
		store  *testStore
		budget *Budget
		want   error
		text   string
	}{
		{
			name: "status", store: &testStore{err: status.Error(codes.Unavailable, "store down")},
			want: errStoreAPI, text: "store down",
		},
		{
			name: "unknown response", store: &testStore{frames: [][]byte{encodeTestResponse(9, []byte{1})}},
			want: errStoreAPI, text: "unsupported response field",
		},
		{name: "varint response", store: &testStore{frames: [][]byte{
			protowire.AppendVarint(protowire.AppendTag(nil, seriesResponseSeries, protowire.VarintType), 1),
		}}, want: errStoreAPI},
		{name: "malformed", store: &testStore{frames: [][]byte{{0xff, 0xff}}}, want: errStoreAPI},
		{
			name: "downsampled", store: &testStore{frames: [][]byte{series(storeChunkXOR, false)}},
			want: errStoreAPI, text: "raw",
		},
		{name: "encoding", store: &testStore{frames: [][]byte{series(9, true)}}, want: errStoreAPI, text: "encoding"},
		{
			name: "series limit", store: &testStore{frames: [][]byte{
				series(storeChunkXOR, true),
				encodeTestSeries([]string{labels.MetricName, "other"},
					encodeTestChunk(storeChunkXOR, xorData(t, testPoint{t: 1, f: 1}), true)),
			}},
			budget: &Budget{MaxSeries: 1}, want: ErrFetchLimit,
		},
		{
			name: "sample limit", store: &testStore{frames: [][]byte{series(storeChunkXOR, true)}},
			budget: &Budget{MaxSamples: 1}, want: ErrFetchLimit,
		},
		{name: "byte limit", store: &testStore{frames: [][]byte{
			series(storeChunkXOR, true),
			series(storeChunkXOR, true),
		}}, budget: &Budget{MaxResponseBytes: 50}, want: ErrFetchLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := test.store.serve(t).Select(testContext(t, test.budget), 0, 10, nil)
			require.ErrorIs(t, err, test.want)
			if test.text != "" {
				require.ErrorContains(t, err, test.text)
			}
		})
	}
}

func TestStoreAPIRequests(t *testing.T) {
	_, err := encodeSeriesRequest(0, 1, nil, []*labels.Matcher{{Type: labels.MatchType(9), Name: testPod}})
	require.ErrorIs(t, err, errStoreAPI)

	store := &testStore{}
	source := store.serve(t)
	_, _, err = source.Select(context.Background(), 0, 10, nil)
	require.NoError(t, err)
	require.Empty(t, store.md.Get(testTenantHeader), "without a client request, no headers are forwarded")

	for _, scheme := range []string{prop.StoreAPIScheme, prop.StoreAPISchemeTLS} {
		u, err := url.Parse(scheme + "://thanos:10901")
		require.NoError(t, err)
		s, err := NewStoreAPI(u, nil, nil)
		require.NoError(t, err)
		require.NoError(t, s.Close())
	}
}

func TestRawCodec(t *testing.T) {
	_, err := rawCodec{}.Marshal("frame")
	require.ErrorIs(t, err, errStoreAPI)
	require.ErrorIs(t, rawCodec{}.Unmarshal(nil, "frame"), errStoreAPI)
	require.Equal(t, storeAPICodecName, rawCodec{}.Name())
}
