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
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	"github.com/klauspost/compress/snappy"
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
)

const (
	testTenantHeader = "X-Scope-OrgID"
	testTenant       = "tenant-a"
	testConfigHeader = "X-Configured"
	testConfigValue  = "yes"
	testQueryPath    = "/api/v1/query_range"
	testMetric       = "up"
	testPod          = "pod"
)

type testPoint struct {
	t int64
	f float64
	h bool
}

func xorChunk(t *testing.T, points ...testPoint) prompb.Chunk {
	t.Helper()
	c := chunkenc.NewXORChunk()
	app, err := c.Appender()
	require.NoError(t, err)
	for _, p := range points {
		app.Append(0, p.t, p.f)
	}
	return prompb.Chunk{MinTimeMs: points[0].t, MaxTimeMs: points[len(points)-1].t, Type: prompb.Chunk_XOR, Data: c.Bytes()}
}

func testHistogram() *histogram.Histogram {
	return &histogram.Histogram{
		Count: 4, Sum: 5, PositiveSpans: []histogram.Span{{Offset: 0, Length: 2}},
		PositiveBuckets: []int64{2, 0},
	}
}

func histogramChunk(t *testing.T, ts ...int64) prompb.Chunk {
	t.Helper()
	c := chunkenc.NewHistogramChunk()
	app, err := c.Appender()
	require.NoError(t, err)
	var prev chunkenc.Appender
	for _, at := range ts {
		_, _, app2, err := app.AppendHistogram(prev, 0, at, testHistogram(), true)
		require.NoError(t, err)
		prev, app = app2, app2
	}
	return prompb.Chunk{MinTimeMs: ts[0], MaxTimeMs: ts[len(ts)-1], Type: prompb.Chunk_HISTOGRAM, Data: c.Bytes()}
}

func testLabels(pod string) []prompb.Label {
	return prompb.FromLabels(labels.FromStrings(labels.MetricName, testMetric, testPod, pod), nil)
}

func writeFrame(t *testing.T, w *bytes.Buffer, series ...*prompb.ChunkedSeries) {
	t.Helper()
	data, err := (&prompb.ChunkedReadResponse{ChunkedSeries: series}).Marshal()
	require.NoError(t, err)
	var size [binary.MaxVarintLen64]byte
	w.Write(size[:binary.PutUvarint(size[:], uint64(len(data)))])
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], crc32.Checksum(data, castagnoli))
	w.Write(crc[:])
	w.Write(data)
}

// readPoints iterates a series, reporting each sample as a testPoint
func readPoints(t *testing.T, s storage.Series) []testPoint {
	t.Helper()
	var out []testPoint
	it := s.Iterator(nil)
	for vt := it.Next(); vt != chunkenc.ValNone; vt = it.Next() {
		switch vt {
		case chunkenc.ValFloat:
			at, f := it.At()
			out = append(out, testPoint{t: at, f: f})
		default:
			out = append(out, testPoint{t: it.AtT(), h: true})
		}
	}
	require.NoError(t, it.Err())
	return out
}

func newTestRemoteRead(t *testing.T, handler http.HandlerFunc) *RemoteRead {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL + prop.RemoteReadPath)
	require.NoError(t, err)
	pc := po.New()
	pc.Path = testQueryPath
	pc.RequestHeaders = map[string]string{testConfigHeader: testConfigValue}
	return NewRemoteRead(u, srv.Client(), po.List{pc})
}

func testContext(t *testing.T, budget *Budget) context.Context {
	t.Helper()
	in, err := http.NewRequest(http.MethodGet, "http://trickster"+testQueryPath, nil)
	require.NoError(t, err)
	in.Header.Set(testTenantHeader, testTenant)
	in.Header.Set(headers.NameAuthorization, "Bearer t")
	in.Header.Set(headers.NameConnection, "close")
	in.Header.Set(headers.NameAcceptEncoding, "gzip")
	in.Header.Set("If-None-Match", "x")
	return WithBudget(WithRequest(context.Background(), in), budget)
}

func decodeTestRequest(t *testing.T, r *http.Request) *prompb.ReadRequest {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	data, err := snappy.Decode(nil, body)
	require.NoError(t, err)
	var req prompb.ReadRequest
	require.NoError(t, req.Unmarshal(data))
	return &req
}

func TestRemoteReadStreamedChunks(t *testing.T) {
	var got *prompb.ReadRequest
	var gotHeader http.Header
	source := newTestRemoteRead(t, func(w http.ResponseWriter, r *http.Request) {
		got, gotHeader = decodeTestRequest(t, r), r.Header.Clone()
		var body bytes.Buffer
		// series a is split across two frames, and series b's chunks overlap
		writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
			xorChunk(t, testPoint{t: 1, f: 1}, testPoint{t: 2, f: 2}),
		}})
		writeFrame(t, &body,
			&prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{xorChunk(t, testPoint{t: 3, f: 3})}},
			&prompb.ChunkedSeries{Labels: testLabels("b"), Chunks: []prompb.Chunk{
				xorChunk(t, testPoint{t: 1, f: 1}, testPoint{t: 3, f: 3}),
				xorChunk(t, testPoint{t: 2, f: 2}, testPoint{t: 3, f: 3}),
			}},
			&prompb.ChunkedSeries{Labels: testLabels("c"), Chunks: []prompb.Chunk{histogramChunk(t, 5, 6)}},
		)
		w.Header().Set(headers.NameContentType, contentTypeStreamed+"; proto=prometheus.ChunkedReadResponse")
		_, _ = w.Write(body.Bytes())
	})
	matchers := []*labels.Matcher{
		labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric),
		labels.MustNewMatcher(labels.MatchNotEqual, testPod, "x"),
		labels.MustNewMatcher(labels.MatchRegexp, testPod, "a|b|c"),
		labels.MustNewMatcher(labels.MatchNotRegexp, testPod, "z"),
	}
	hints := &storage.SelectHints{Start: 1, End: 9, Step: 2, Func: "rate", Range: 4, Grouping: []string{testPod}, By: true}
	budget := &Budget{}
	series, err := source.Select(testContext(t, budget), 0, 10, hints, matchers...)
	require.NoError(t, err)

	require.Equal(t, prop.SeriesAPIRemoteRead, source.SeriesAPI())
	require.Equal(t, contentEncodingSnappy, gotHeader.Get(headers.NameContentEncoding))
	require.Equal(t, contentTypeProtobuf, gotHeader.Get(headers.NameContentType))
	require.Equal(t, remoteReadVersion, gotHeader.Get(remoteReadVersionHeader))
	require.Equal(t, testTenant, gotHeader.Get(testTenantHeader))
	require.Equal(t, testConfigValue, gotHeader.Get(testConfigHeader))
	require.Empty(t, gotHeader.Get("If-None-Match"))
	require.NotEqual(t, "close", gotHeader.Get(headers.NameConnection))

	require.Len(t, got.Queries, 1)
	q := got.Queries[0]
	require.Equal(t, int64(0), q.StartTimestampMs)
	require.Equal(t, int64(10), q.EndTimestampMs)
	require.Equal(t, []prompb.LabelMatcher_Type{
		prompb.LabelMatcher_EQ, prompb.LabelMatcher_NEQ,
		prompb.LabelMatcher_RE, prompb.LabelMatcher_NRE,
	},
		[]prompb.LabelMatcher_Type{q.Matchers[0].Type, q.Matchers[1].Type, q.Matchers[2].Type, q.Matchers[3].Type})
	require.Equal(t, prompb.ReadHints{
		StepMs: 2, Func: "rate", StartMs: 1, EndMs: 9, Grouping: []string{testPod},
		By: true, RangeMs: 4,
	}, *q.Hints)
	require.Equal(t, []prompb.ReadRequest_ResponseType{
		prompb.ReadRequest_STREAMED_XOR_CHUNKS,
		prompb.ReadRequest_SAMPLES,
	}, got.AcceptedResponseTypes)

	require.Len(t, series, 3)
	require.Equal(t, []testPoint{{t: 1, f: 1}, {t: 2, f: 2}, {t: 3, f: 3}}, readPoints(t, series[0]))
	require.Equal(t, []testPoint{{t: 1, f: 1}, {t: 2, f: 2}, {t: 3, f: 3}}, readPoints(t, series[1]))
	require.Equal(t, []testPoint{{t: 5, h: true}, {t: 6, h: true}}, readPoints(t, series[2]))
	require.Equal(t, int64(3), budget.series.Load())
	require.Equal(t, int64(9), budget.samples.Load())
}

func TestRemoteReadSamples(t *testing.T) {
	source := newTestRemoteRead(t, func(w http.ResponseWriter, r *http.Request) {
		_ = decodeTestRequest(t, r)
		res := &prompb.ReadResponse{Results: []*prompb.QueryResult{{Timeseries: []*prompb.TimeSeries{{
			Labels:  testLabels("a"),
			Samples: []prompb.Sample{{Value: 1, Timestamp: 1}, {Value: math.Inf(1), Timestamp: 4}},
			Histograms: []prompb.Histogram{
				prompb.FromIntHistogram(2, testHistogram()),
				prompb.FromFloatHistogram(3, testHistogram().ToFloat(nil)),
			},
		}}}}}
		data, err := res.Marshal()
		require.NoError(t, err)
		w.Header().Set(headers.NameContentType, contentTypeProtobuf)
		_, _ = w.Write(snappy.Encode(nil, data))
	})
	series, err := source.Select(testContext(t, nil), 0, 10, nil)
	require.NoError(t, err)
	require.Len(t, series, 1)
	require.Equal(t, []testPoint{{t: 1, f: 1}, {t: 2, h: true}, {t: 3, h: true}, {t: 4, f: math.Inf(1)}},
		readPoints(t, series[0]))
	it := series[0].Iterator(nil)
	require.Equal(t, chunkenc.ValFloat, it.Next())
	for range 2 {
		require.Equal(t, chunkenc.ValFloatHistogram, it.Next())
		_, fh := it.AtFloatHistogram(nil)
		require.InDelta(t, 4.0, fh.Count, 0)
	}
}

func TestRemoteReadErrors(t *testing.T) {
	frames := func(t *testing.T, mutate func([]byte) []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			var body bytes.Buffer
			writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
				xorChunk(t, testPoint{t: 1, f: 1}, testPoint{t: 2, f: 2}),
			}}, &prompb.ChunkedSeries{Labels: testLabels("b"), Chunks: []prompb.Chunk{
				xorChunk(t, testPoint{t: 1, f: 1}),
			}})
			w.Header().Set(headers.NameContentType, contentTypeStreamed)
			_, _ = w.Write(mutate(body.Bytes()))
		}
	}
	same := func(b []byte) []byte { return b }
	tests := []struct {
		name    string
		handler http.HandlerFunc
		budget  *Budget
		want    error
		text    string
	}{
		{name: "status", handler: func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "tenant missing", http.StatusUnauthorized)
		}, want: errRemoteRead, text: "tenant missing"},
		{name: "checksum", handler: frames(t, func(b []byte) []byte {
			b[len(b)-1] ^= 0xff
			return b
		}), want: errRemoteRead, text: "checksum"},
		{name: "truncated", handler: frames(t, func(b []byte) []byte { return b[:len(b)-2] }), want: errRemoteRead},
		{name: "oversized frame", handler: func(w http.ResponseWriter, _ *http.Request) {
			var size [binary.MaxVarintLen64]byte
			w.Header().Set(headers.NameContentType, contentTypeStreamed)
			_, _ = w.Write(size[:binary.PutUvarint(size[:], maxFrameBytes+1)])
		}, want: errRemoteRead, text: "exceeds"},
		{name: "unknown encoding", handler: func(w http.ResponseWriter, _ *http.Request) {
			var body bytes.Buffer
			writeFrame(t, &body, &prompb.ChunkedSeries{
				Labels: testLabels("a"),
				Chunks: []prompb.Chunk{{Type: prompb.Chunk_UNKNOWN}},
			})
			w.Header().Set(headers.NameContentType, contentTypeStreamed)
			_, _ = w.Write(body.Bytes())
		}, want: errRemoteRead, text: "encoding"},
		{name: "series limit", handler: frames(t, same), budget: &Budget{MaxSeries: 1}, want: ErrFetchLimit},
		{name: "sample limit", handler: frames(t, same), budget: &Budget{MaxSamples: 2}, want: ErrFetchLimit},
		{name: "streamed byte limit", handler: frames(t, same), budget: &Budget{MaxResponseBytes: 8}, want: ErrFetchLimit},
		{name: "samples byte limit", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(snappy.Encode(nil, bytes.Repeat([]byte{1}, 64)))
		}, budget: &Budget{MaxResponseBytes: 32}, want: ErrFetchLimit},
		{name: "samples corrupt", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not snappy"))
		}, want: errRemoteRead},
		{name: "samples not protobuf", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(snappy.Encode(nil, []byte{0xff, 0xff, 0xff}))
		}, want: errRemoteRead},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newTestRemoteRead(t, test.handler).Select(testContext(t, test.budget), 0, 10, nil)
			require.ErrorIs(t, err, test.want)
			if test.text != "" {
				require.Contains(t, err.Error(), test.text)
			}
		})
	}
}

func TestRemoteReadUnreachable(t *testing.T) {
	u, err := url.Parse("http://127.0.0.1:1" + prop.RemoteReadPath)
	require.NoError(t, err)
	_, err = NewRemoteRead(u, &http.Client{}, nil).Select(context.Background(), 0, 1, nil)
	require.ErrorIs(t, err, errRemoteRead)
}

func TestEncodeReadRequestRejectsUnknownMatcher(t *testing.T) {
	_, err := encodeReadRequest(0, 1, nil, []*labels.Matcher{{Type: labels.MatchType(99), Name: testPod}})
	require.ErrorIs(t, err, errRemoteRead)
}

func TestBudgetAndLimitedReader(t *testing.T) {
	var none *Budget
	require.NoError(t, none.add(10, 10))
	require.Zero(t, none.responseLimit())
	r := &limitedReader{r: strings.NewReader("abcdef"), remaining: 6}
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "abcdef", string(data))
	_, err = io.ReadAll(&limitedReader{r: strings.NewReader("abcdef"), remaining: 3})
	require.ErrorIs(t, err, ErrFetchLimit)
}

func TestSampleCopy(t *testing.T) {
	fh := sample{t: 1, fh: testHistogram().ToFloat(nil)}
	require.NotSame(t, fh.FH(), fh.Copy().FH())
	require.Nil(t, fh.H())
	require.Zero(t, fh.ST())
	require.Equal(t, chunkenc.ValFloat, sample{}.Copy().Type())
}

func TestDecodeChunkEncodings(t *testing.T) {
	xor2 := chunkenc.NewXOR2Chunk()
	app, err := xor2.Appender()
	require.NoError(t, err)
	app.Append(0, 1, 7)
	chk, err := decodeChunk(prompb.Chunk{Type: prompb.Chunk_XOR2, Data: xor2.Bytes()})
	require.NoError(t, err)
	require.Equal(t, chunkenc.EncXOR2, chk.Encoding())

	fh := chunkenc.NewFloatHistogramChunk()
	happ, err := fh.Appender()
	require.NoError(t, err)
	_, _, _, err = happ.AppendFloatHistogram(nil, 0, 1, testHistogram().ToFloat(nil), true)
	require.NoError(t, err)
	chk, err = decodeChunk(prompb.Chunk{Type: prompb.Chunk_FLOAT_HISTOGRAM, Data: fh.Bytes()})
	require.NoError(t, err)
	require.Equal(t, chunkenc.EncFloatHistogram, chk.Encoding())
}

func TestChunkSeriesLabels(t *testing.T) {
	lset := labels.FromStrings(labels.MetricName, testMetric)
	require.Equal(t, lset, (&chunkSeries{lset: lset}).Labels())
}
