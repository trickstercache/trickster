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
	"errors"
	"hash/crc32"
	"io"
	"math"
	"net/http"
	"runtime"
	"strconv"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/klauspost/compress/snappy"
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

// decodeTestMint and decodeTestMaxt are the range the decoder tests read
const (
	decodeTestMint = 100
	decodeTestMaxt = 200
)

func appendBytesField(b []byte, num protowire.Number, v []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(b, num, protowire.BytesType), v)
}

func appendVarintField(b []byte, num protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(b, num, protowire.VarintType), v)
}

func appendFixed64Field(b []byte, num protowire.Number, v uint64) []byte {
	return protowire.AppendFixed64(protowire.AppendTag(b, num, protowire.Fixed64Type), v)
}

// chunkedFrame returns one frame's ChunkedReadResponse holding series
func chunkedFrame(t testing.TB, series ...*prompb.ChunkedSeries) []byte {
	t.Helper()
	data, err := (&prompb.ChunkedReadResponse{ChunkedSeries: series}).Marshal()
	require.NoError(t, err)
	return data
}

// samplesResponse returns a ReadResponse holding series of samples
func samplesResponse(t testing.TB, series ...*prompb.TimeSeries) []byte {
	t.Helper()
	data, err := (&prompb.ReadResponse{Results: []*prompb.QueryResult{{Timeseries: series}}}).Marshal()
	require.NoError(t, err)
	return data
}

// claimedChunk is XOR chunk data of only a header claiming n samples, all that counting a chunk within range reads
func claimedChunk(n uint16, mint, maxt int64) prompb.Chunk {
	return prompb.Chunk{MinTimeMs: mint, MaxTimeMs: maxt, Type: prompb.Chunk_XOR, Data: binary.BigEndian.AppendUint16(nil, n)}
}

// pointsAt returns float points at each time, valued by it
func pointsAt(ts ...int64) []testPoint {
	out := make([]testPoint, len(ts))
	for i, at := range ts {
		out[i] = testPoint{t: at, f: float64(at)}
	}
	return out
}

func decodeFrames(t testing.TB, budget *Budget, frames ...[]byte) ([]storage.Series, error) {
	t.Helper()
	d := newDecoder(budget, decodeTestMint, decodeTestMaxt, errRemoteRead)
	for _, f := range frames {
		if err := d.chunkedReadResponse(f); err != nil {
			return nil, err
		}
	}
	return d.out, nil
}

// decodeAllocs returns the allocations and bytes decoding data takes, and its error
func decodeAllocs(t *testing.T, decode func(*decoder) error) (allocs float64, allocated uint64, err error) {
	t.Helper()
	allocs = testing.AllocsPerRun(5, func() {
		err = decode(newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead))
	})
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err = decode(newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead))
	runtime.ReadMemStats(&after)
	return allocs, after.TotalAlloc - before.TotalAlloc, err
}

func TestDecoderKeepsOnlyInRange(t *testing.T) {
	frame := chunkedFrame(t,
		&prompb.ChunkedSeries{Labels: testLabels("before"), Chunks: []prompb.Chunk{xorChunk(t, pointsAt(10, 50)...)}},
		&prompb.ChunkedSeries{Labels: testLabels("after"), Chunks: []prompb.Chunk{xorChunk(t, pointsAt(300, 400)...)}},
		&prompb.ChunkedSeries{Labels: testLabels("empty"), Chunks: []prompb.Chunk{claimedChunk(0, 150, 150)}},
		&prompb.ChunkedSeries{Labels: testLabels("across"), Chunks: []prompb.Chunk{
			xorChunk(t, pointsAt(10, 20)...), xorChunk(t, pointsAt(90, 110, 130)...), xorChunk(t, pointsAt(150)...),
			claimedChunk(0, 160, 170), xorChunk(t, pointsAt(190, 210)...), xorChunk(t, pointsAt(300)...),
		}},
	)
	budget := &Budget{}
	series, err := decodeFrames(t, budget, frame)
	require.NoError(t, err)
	require.Len(t, series, 1, "series without samples in range are never kept")
	cs := series[0].(*chunkSeries)
	require.Equal(t, labels.FromStrings(labels.MetricName, testMetric, testPod, "across"), cs.lset)
	// only chunks with samples in range are kept, those across either end whole
	require.Equal(t, []span{{90, 130}, {150, 150}, {190, 210}}, cs.spans)
	require.Equal(t, pointsAt(110, 130, 150, 190), readPoints(t, bound(series, decodeTestMint, decodeTestMaxt)[0]))
	fetchedSeries, fetchedSamples := budget.Fetched()
	require.Equal(t, int64(1), fetchedSeries)
	require.Equal(t, int64(4), fetchedSamples, "a chunk across either end counts only its samples in range")
}

func TestDecoderSeriesContinuations(t *testing.T) {
	x := func(ts ...int64) []byte {
		return chunkedFrame(t, &prompb.ChunkedSeries{Labels: testLabels("x"), Chunks: []prompb.Chunk{xorChunk(t, pointsAt(ts...)...)}})
	}
	y := chunkedFrame(t, &prompb.ChunkedSeries{Labels: testLabels("y"), Chunks: []prompb.Chunk{xorChunk(t, pointsAt(10)...)}})
	budget := &Budget{}
	series, err := decodeFrames(t, budget,
		x(10, 20),   // before the range: nothing is kept, and no series begins
		x(110, 120), // the series begins with its first chunk in range
		x(300),      // after the range: the series goes on, keeping nothing
		x(150),      // so this continues it
		y,           // another series keeps nothing, yet ends x's
		x(160),      // so x is sent again, as a series of its own, as before
	)
	require.NoError(t, err)
	require.Len(t, series, 2)
	require.Equal(t, []span{{110, 120}, {150, 150}}, series[0].(*chunkSeries).spans)
	require.Equal(t, []span{{160, 160}}, series[1].(*chunkSeries).spans)
	fetchedSeries, fetchedSamples := budget.Fetched()
	require.Equal(t, int64(2), fetchedSeries)
	require.Equal(t, int64(4), fetchedSamples)
}

func TestDecoderLabelsOutliveTheMessage(t *testing.T) {
	frame := chunkedFrame(t, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
		xorChunk(t, pointsAt(110, 120)...),
	}})
	series, err := decodeFrames(t, nil, frame)
	require.NoError(t, err)
	// a frame's buffer is reused for the next, so nothing kept may share its memory
	clear(frame)
	require.Equal(t, labels.FromStrings(labels.MetricName, testMetric, testPod, "a"), series[0].Labels())
	require.Equal(t, pointsAt(110, 120), readPoints(t, series[0]))
}

func TestDecoderManyEntriesTakeNoMemory(t *testing.T) {
	outside := func(n int) []byte {
		series := make([]*prompb.ChunkedSeries, n)
		for i := range series {
			series[i] = &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
				claimedChunk(120, 10, 20), claimedChunk(0, 150, 160),
			}}
		}
		return chunkedFrame(t, series...)
	}
	emptySamples := func(n int) []byte {
		// empty Sample messages are at time zero, before the range
		ts := appendBytesField(nil, seriesLabels, nil)
		ts = append(ts, bytes.Repeat(appendBytesField(nil, timeSeriesSamples, nil), n)...)
		return appendBytesField(nil, readResponseResults, appendBytesField(nil, queryResultTimeseries, ts))
	}
	shapes := map[string]struct {
		message func(n int) []byte
		decode  func(*decoder, []byte) error
	}{
		"series out of range": {message: outside, decode: (*decoder).chunkedReadResponse},
		"empty chunked series": {message: func(n int) []byte {
			return bytes.Repeat(appendBytesField(nil, chunkedResponseSeries, nil), n)
		}, decode: (*decoder).chunkedReadResponse},
		"empty time series": {message: func(n int) []byte {
			return appendBytesField(nil, readResponseResults,
				bytes.Repeat(appendBytesField(nil, queryResultTimeseries, nil), n))
		}, decode: (*decoder).readResponse},
		"empty samples": {message: emptySamples, decode: (*decoder).readResponse},
	}
	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			few, many := shape.message(10), shape.message(20000)
			fewAllocs, _, err := decodeAllocs(t, func(d *decoder) error { return shape.decode(d, few) })
			require.NoError(t, err)
			manyAllocs, manyBytes, err := decodeAllocs(t, func(d *decoder) error {
				if err := shape.decode(d, many); err != nil {
					return err
				}
				if len(d.out) > 0 || d.series != 0 || d.samples != 0 {
					return errors.New("kept or counted an entry")
				}
				return nil
			})
			require.NoError(t, err)
			// what nothing in range is decoded from takes no memory for each entry
			require.Equal(t, fewAllocs, manyAllocs)
			require.Less(t, manyBytes, uint64(len(many)))
		})
	}
}

func TestDecoderAllowanceAppliesWithoutFetchLimits(t *testing.T) {
	inRange := xorChunk(t, pointsAt(110)...)
	chunk, err := inRange.Marshal()
	require.NoError(t, err)
	manyLabels := bytes.Repeat(appendBytesField(nil, seriesLabels, nil), 1000)
	// emptyHistograms are n histograms of no buckets, each step milliseconds after the one before
	emptyHistograms := func(n int, step uint64) []byte {
		ts := appendBytesField(nil, seriesLabels, nil)
		for i := range uint64(n) {
			ts = appendBytesField(ts, timeSeriesHistograms, appendVarintField(nil, histogramTimestamp, decodeTestMint+i*step))
		}
		return appendBytesField(nil, readResponseResults, appendBytesField(nil, queryResultTimeseries, ts))
	}
	tests := map[string]struct {
		message []byte
		decode  func(*decoder, []byte) error
	}{
		"empty labels": {
			message: appendBytesField(nil, chunkedResponseSeries, appendBytesField(manyLabels, seriesChunks, chunk)),
			decode:  (*decoder).chunkedReadResponse,
		},
		// each sample's slot is charged before the series' samples are allocated, those sharing a time too
		"many samples": {message: emptyHistograms(1000, 0), decode: (*decoder).readResponse},
		// and each histogram kept, one at each time, before it is
		"many histograms": {message: emptyHistograms(200, 1), decode: (*decoder).readResponse},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// fetching is unlimited, and the response well under its byte limit
			budget := &Budget{MaxResponseBytes: 4 << 10}
			require.Less(t, len(test.message), int(budget.MaxResponseBytes))
			_, allocated, err := decodeAllocs(t, func(d *decoder) error {
				d.budget, d.limit, d.allowance = budget, budget.decodeLimit(), budget.decodeLimit()
				return test.decode(d, test.message)
			})
			require.ErrorIs(t, err, ErrFetchLimit)
			require.ErrorContains(t, err, "decoded from a response would take more than")
			// what is kept stays within the limit; slices grown by doubling allocated as much again before it
			require.Less(t, allocated, uint64(3*budget.decodeLimit()))
		})
	}
}

func TestDecoderAllowanceBoundary(t *testing.T) {
	chunk := xorChunk(t, pointsAt(110, 120)...)
	frame := chunkedFrame(t, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{chunk}})
	// two label slots, the series with its labels, and the chunk with its data
	labelsSize := int64(len(labels.MetricName)+len(testMetric)+len(testPod)+len("a")) + 2*labelLengthBytes
	cost := 2*labelSlotBytes + chunkSeriesBytes + labelsSize + chunkBytes + int64(len(chunk.Data))
	for _, limit := range []int64{cost, cost - 1} {
		d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
		d.limit, d.allowance = limit, limit
		err := d.chunkedReadResponse(frame)
		if limit == cost {
			require.NoError(t, err)
			require.Zero(t, d.allowance)
			continue
		}
		require.ErrorIs(t, err, ErrFetchLimit)
		require.Empty(t, d.out, "the chunk over the limit is never kept")
	}

	require.Equal(t, decodeExpansion*int64(bo.DefaultMaxCaptureBytes), (*Budget)(nil).decodeLimit())
	require.Equal(t, decodeExpansion*int64(bo.DefaultMaxCaptureBytes), (&Budget{}).decodeLimit())
	require.Equal(t, int64(decodeExpansion*1000), (&Budget{MaxResponseBytes: 1000}).decodeLimit())
	require.Equal(t, int64(math.MaxInt64), (&Budget{MaxResponseBytes: math.MaxInt64 - 1}).decodeLimit())
}

func TestDecoderLabelSlotsChargedOnce(t *testing.T) {
	d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
	series := func(kv ...string) []byte {
		lset := prompb.FromLabels(labels.FromStrings(kv...), nil)
		return chunkedFrame(t, &prompb.ChunkedSeries{Labels: lset, Chunks: []prompb.Chunk{claimedChunk(1, 10, 20)}})
	}
	require.NoError(t, d.chunkedReadResponse(series("a", "1", "b", "2", "c", "3")))
	require.Equal(t, 3*labelSlotBytes, d.limit-d.allowance)
	require.NoError(t, d.chunkedReadResponse(series("a", "1")))
	require.Equal(t, 3*labelSlotBytes, d.limit-d.allowance, "a series with fewer labels reuses the slots")
	require.NoError(t, d.chunkedReadResponse(series("a", "1", "b", "2", "c", "3", "d", "4")))
	require.Equal(t, 4*labelSlotBytes, d.limit-d.allowance)
}

func TestDecoderCountsBeforeKeeping(t *testing.T) {
	const chunks, perChunk = 1000, 120
	manyChunks := func() *prompb.ChunkedSeries {
		cs := &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: make([]prompb.Chunk, chunks)}
		for i := range cs.Chunks {
			cs.Chunks[i] = claimedChunk(perChunk, decodeTestMint, decodeTestMaxt)
		}
		return cs
	}
	frame := chunkedFrame(t, manyChunks())
	storeSeries := []byte{}
	for range chunks {
		storeSeries = appendBytesField(storeSeries, seriesChunks,
			spanned(encodeTestChunk(storeChunkXOR, binary.BigEndian.AppendUint16(nil, perChunk), true), decodeTestMint, decodeTestMaxt))
	}
	decodes := map[string]func(*decoder) error{
		"remote read": func(d *decoder) error { return d.chunkedReadResponse(frame) },
		"storeapi":    func(d *decoder) error { return d.chunkedSeries(storeSeries, storeChunk) },
	}
	for name, decode := range decodes {
		t.Run(name, func(t *testing.T) {
			for _, kept := range []int64{0, 3} {
				budget := &Budget{MaxSamples: kept*perChunk + 1}
				d := newDecoder(budget, decodeTestMint, decodeTestMaxt, errRemoteRead)
				require.ErrorIs(t, decode(d), ErrFetchLimit)
				// the read stops at the chunk the budget can't afford, before keeping it
				if kept == 0 {
					require.Empty(t, d.out)
				} else {
					require.Len(t, d.out[0].(*chunkSeries).chunks, int(kept))
				}
				_, samples := budget.Fetched()
				require.Equal(t, (kept+1)*perChunk, samples)
			}
			allocs := testing.AllocsPerRun(5, func() {
				_ = decode(newDecoder(&Budget{MaxSamples: 1}, decodeTestMint, decodeTestMaxt, errRemoteRead))
			})
			// an allocation for each chunk not read would come to a thousand; other tests' goroutines add a few
			require.Less(t, allocs, float64(chunks/10), "nothing is allocated for the chunks not read")
		})
	}
}

func TestDecoderAddsToBudgetByQuantum(t *testing.T) {
	var shared [][2]int64
	budget := &Budget{share: func(series, samples int64, _ []uint64) error {
		shared = append(shared, [2]int64{series, samples})
		return nil
	}}
	frame := chunkedFrame(t,
		&prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
			claimedChunk(2000, 110, 120), claimedChunk(2000, 130, 140), claimedChunk(2000, 150, 160),
		}},
		&prompb.ChunkedSeries{Labels: testLabels("b"), Chunks: []prompb.Chunk{claimedChunk(0, 110, 120)}},
		&prompb.ChunkedSeries{Labels: testLabels("c"), Chunks: []prompb.Chunk{claimedChunk(10, 110, 120)}},
	)
	series, err := decodeFrames(t, budget, frame)
	require.NoError(t, err)
	require.Len(t, series, 2)
	// a quantum is added at once, and the rest as the frame ends, each with the series new to it
	require.Equal(t, [][2]int64{{1, 6000}, {1, 10}}, shared)
	fetchedSeries, fetchedSamples := budget.Fetched()
	require.Equal(t, int64(2), fetchedSeries)
	require.Equal(t, int64(6010), fetchedSamples)

	stop := errors.New("no evaluation can afford more")
	shared = nil
	budget = &Budget{share: func(series, samples int64, _ []uint64) error {
		shared = append(shared, [2]int64{series, samples})
		return stop
	}}
	_, err = decodeFrames(t, budget, frame)
	require.ErrorIs(t, err, stop)
	require.Equal(t, [][2]int64{{1, 6000}}, shared, "a shared read stops once none of its evaluations can afford more")

	// what a frame counts short of a quantum is added as it ends, and can fail it too
	shared = nil
	_, err = decodeFrames(t, budget, chunkedFrame(t, &prompb.ChunkedSeries{
		Labels: testLabels("c"),
		Chunks: []prompb.Chunk{claimedChunk(10, 110, 120)},
	}))
	require.ErrorIs(t, err, stop)
	require.Equal(t, [][2]int64{{1, 10}}, shared)
	shared = nil
	d := newDecoder(budget, decodeTestMint, decodeTestMaxt, errRemoteRead)
	err = d.readResponse(samplesResponse(t, &prompb.TimeSeries{
		Labels:  testLabels("c"),
		Samples: []prompb.Sample{{Timestamp: 110, Value: 1}},
	}))
	require.ErrorIs(t, err, stop)
	require.Equal(t, [][2]int64{{1, 1}}, shared)
}

func TestDecoderSamples(t *testing.T) {
	floats := func(ts ...int64) []prompb.Sample {
		out := make([]prompb.Sample, len(ts))
		for i, at := range ts {
			out[i] = prompb.Sample{Timestamp: at, Value: float64(at)}
		}
		return out
	}
	data := samplesResponse(t,
		&prompb.TimeSeries{
			Labels: testLabels("outside"), Samples: floats(10, 300),
			Histograms: []prompb.Histogram{prompb.FromIntHistogram(50, testHistogram())},
		},
		&prompb.TimeSeries{
			Labels: testLabels("a"), Samples: floats(90, 110, 130, 210),
			Histograms: []prompb.Histogram{
				prompb.FromIntHistogram(50, testHistogram()), prompb.FromIntHistogram(120, testHistogram()),
				prompb.FromFloatHistogram(130, testHistogram().ToFloat(nil)), prompb.FromIntHistogram(250, testHistogram()),
			},
		},
		&prompb.TimeSeries{Labels: testLabels("floats"), Samples: floats(10, 99, 100, 150, 200, 201, 300)},
	)
	budget := &Budget{}
	d := newDecoder(budget, decodeTestMint, decodeTestMaxt, errRemoteRead)
	require.NoError(t, d.readResponse(data))
	require.Len(t, d.out, 2)
	ss := d.out[0].(*sampleSeries)
	require.Equal(t, labels.FromStrings(labels.MetricName, testMetric, testPod, "a"), ss.lset)
	// only samples in range are kept, merged in time order, a float rather than a histogram where both share a time
	require.Equal(t, []testPoint{{t: 110, f: 110}, {t: 120, h: true}, {t: 130, f: 130}}, readPoints(t, ss))
	require.Len(t, ss.samples, cap(ss.samples)-1, "the samples are allocated once, for every one sent in range")
	// a series of only floats keeps those from mint to maxt, both included
	fs := d.out[1].(*sampleSeries)
	require.Equal(t, pointsAt(100, 150, 200), readPoints(t, fs))
	require.Len(t, fs.samples, cap(fs.samples))
	fetchedSeries, fetchedSamples := budget.Fetched()
	require.Equal(t, int64(2), fetchedSeries)
	require.Equal(t, int64(7), fetchedSamples)

	// a series' samples are counted before any is allocated, every one sent in range, though some share a time
	many := make([]int64, 10000)
	for i := range many {
		many[i] = decodeTestMint + int64(i*100/len(many))
	}
	data = samplesResponse(t, &prompb.TimeSeries{Labels: testLabels("a"), Samples: floats(many...)})
	allocs, allocated, err := decodeAllocs(t, func(d *decoder) error {
		d.budget = &Budget{MaxSamples: 10}
		return d.readResponse(data)
	})
	require.ErrorIs(t, err, ErrFetchLimit)
	require.LessOrEqual(t, allocs, float64(8))
	require.Less(t, allocated, uint64(len(many))*uint64(sampleBytes))
}

func TestDecoderHistogramsMatchPrometheus(t *testing.T) {
	ih := &histogram.Histogram{
		CounterResetHint: histogram.GaugeType, Schema: 1, Count: 12, Sum: 3.5, ZeroThreshold: 0.001, ZeroCount: 2,
		PositiveSpans: []histogram.Span{{Offset: -1, Length: 2}, {Offset: 3, Length: 1}}, PositiveBuckets: []int64{1, 2, -1},
		NegativeSpans: []histogram.Span{{Offset: 0, Length: 2}}, NegativeBuckets: []int64{3, -2},
	}
	custom := &histogram.Histogram{
		Schema: histogram.CustomBucketsSchema, Count: 3, Sum: 4, CustomValues: []float64{1, 2.5},
		PositiveSpans: []histogram.Span{{Offset: 0, Length: 2}}, PositiveBuckets: []int64{1, 1},
	}
	marshal := func(h prompb.Histogram) []byte {
		b, err := h.Marshal()
		require.NoError(t, err)
		return b
	}
	span := func(offset int32, length uint32) []byte {
		b := appendVarintField(nil, bucketSpanOffset, protowire.EncodeZigZag(int64(offset)))
		return appendVarintField(b, bucketSpanLength, uint64(length))
	}
	double := func(f float64) uint64 { return math.Float64bits(f) }
	unpacked := appendVarintField(nil, histogramTimestamp, 150)
	unpacked = appendFixed64Field(unpacked, histogramCountFloat, double(9))
	// the last of a oneof's fields is the one kept, so this is an integer histogram
	unpacked = appendVarintField(unpacked, histogramCountInt, 7)
	unpacked = appendFixed64Field(unpacked, histogramZeroCountFloat, double(5))
	unpacked = appendBytesField(unpacked, histogramPositiveSpans, span(0, 1))
	unpacked = appendVarintField(unpacked, histogramPositiveDeltas, protowire.EncodeZigZag(4))
	unpacked = appendBytesField(unpacked, histogramNegativeSpans, span(2, 1))
	unpacked = appendBytesField(unpacked, histogramPositiveSpans, span(1, 2))
	unpacked = appendVarintField(unpacked, histogramPositiveDeltas, protowire.EncodeZigZag(-1))
	unpacked = appendBytesField(unpacked, histogramPositiveDeltas, protowire.AppendVarint(nil, protowire.EncodeZigZag(2)))
	unpacked = appendVarintField(unpacked, histogramNegativeDeltas, protowire.EncodeZigZag(6))
	// counts are read only for a float histogram
	unpacked = appendFixed64Field(unpacked, histogramPositiveCounts, double(100))
	floats := appendVarintField(nil, histogramCountInt, 7)
	floats = appendFixed64Field(floats, histogramCountFloat, double(9.5))
	floats = appendVarintField(floats, histogramZeroCountInt, 3)
	floats = appendFixed64Field(floats, histogramSum, double(-2))
	floats = appendVarintField(floats, histogramSchema, protowire.EncodeZigZag(-2))
	floats = appendBytesField(floats, histogramPositiveSpans, span(-3, 3))
	floats = appendFixed64Field(floats, histogramPositiveCounts, double(1.5))
	floats = appendBytesField(floats, histogramPositiveCounts, protowire.AppendFixed64(protowire.AppendFixed64(nil, double(2)), double(0.5)))
	floats = appendVarintField(floats, histogramPositiveDeltas, protowire.EncodeZigZag(9))
	floats = appendVarintField(floats, histogramResetHint, uint64(prompb.Histogram_NO))
	floats = appendVarintField(floats, histogramTimestamp, 160)
	noCount := appendBytesField(nil, histogramPositiveSpans, span(0, 1))
	noCount = appendBytesField(noCount, histogramPositiveDeltas, protowire.AppendVarint(nil, protowire.EncodeZigZag(3)))
	noCount = appendFixed64Field(noCount, histogramCustomValues, double(4))
	noCount = appendVarintField(noCount, histogramTimestamp, 170)
	messages := map[string][]byte{
		"integer":                marshal(prompb.FromIntHistogram(110, ih)),
		"float":                  marshal(prompb.FromFloatHistogram(120, ih.ToFloat(nil))),
		"custom buckets":         marshal(prompb.FromIntHistogram(130, custom)),
		"finer schema":           marshal(prompb.FromIntHistogram(140, fineHistogram(false))),
		"empty":                  marshal(prompb.Histogram{Timestamp: 145}),
		"unpacked, last oneof":   unpacked,
		"float counts, mixed":    floats,
		"no count field":         noCount,
		"unknown fields skipped": appendBytesField(appendVarintField(marshal(prompb.FromIntHistogram(180, ih)), 99, 1), 98, []byte{1}),
	}
	for name, b := range messages {
		t.Run(name, func(t *testing.T) {
			var ph prompb.Histogram
			require.NoError(t, ph.Unmarshal(b))
			want := ph.ToFloatHistogram()
			if want.Schema > histogram.ExponentialSchemaMax {
				require.NoError(t, want.ReduceResolution(histogram.ExponentialSchemaMax))
			}
			d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
			shape, err := d.histogramShape(b)
			require.NoError(t, err)
			require.Equal(t, ph.Timestamp, shape.t)
			got, err := d.histogram(b, shape)
			require.NoError(t, err)
			require.True(t, want.Equals(got), "decoded %v, want %v", got, want)
			require.Equal(t, want.CounterResetHint, got.CounterResetHint)
			require.InDelta(t, want.ZeroCount, got.ZeroCount, 0)
			require.Equal(t, want.CustomValues, got.CustomValues)
			buckets := len(ph.PositiveDeltas) + len(ph.NegativeDeltas)
			if ph.IsFloatHistogram() {
				buckets = len(ph.PositiveCounts) + len(ph.NegativeCounts)
			}
			require.Equal(t, floatHistogramBytes+int64(len(ph.PositiveSpans)+len(ph.NegativeSpans))*spanBytes+
				int64(buckets+len(ph.CustomValues))*bucketBytes, d.limit-d.allowance,
				"the buckets kept are charged, and those of the other kind are not")
		})
	}
}

func TestDecoderOversizedHistogram(t *testing.T) {
	const buckets = 256 << 10
	h := appendVarintField(nil, histogramCountInt, 1)
	h = appendVarintField(h, histogramTimestamp, decodeTestMint)
	h = appendBytesField(h, histogramPositiveSpans, appendVarintField(nil, bucketSpanLength, buckets))
	// a delta of zero packs into one byte, but takes eight as a bucket
	h = appendBytesField(h, histogramPositiveDeltas, make([]byte, buckets))
	ts := appendBytesField(appendBytesField(nil, seriesLabels, nil), timeSeriesHistograms, h)
	data := appendBytesField(nil, readResponseResults, appendBytesField(nil, queryResultTimeseries, ts))
	budget := &Budget{MaxResponseBytes: int64(len(data))}
	require.Less(t, budget.decodeLimit(), int64(buckets)*bucketBytes)
	_, allocated, err := decodeAllocs(t, func(d *decoder) error {
		d.budget, d.limit, d.allowance = budget, budget.decodeLimit(), budget.decodeLimit()
		return d.readResponse(data)
	})
	require.ErrorIs(t, err, ErrFetchLimit)
	require.Less(t, allocated, uint64(buckets), "the buckets are refused before they are allocated")
}

func TestDecoderMalformedWire(t *testing.T) {
	chunk := func(fields []byte) []byte {
		cs := appendBytesField(nil, seriesLabels, nil)
		return appendBytesField(nil, chunkedResponseSeries, appendBytesField(cs, seriesChunks, fields))
	}
	xor := appendVarintField(appendVarintField(nil, remoteChunkType, uint64(prompb.Chunk_XOR)), remoteChunkMinTime, 110)
	samples := func(ts []byte) []byte {
		return appendBytesField(nil, readResponseResults, appendBytesField(nil, queryResultTimeseries, ts))
	}
	inRange := appendVarintField(nil, histogramTimestamp, decodeTestMint)
	hist := func(fields []byte) []byte {
		return samples(appendBytesField(nil, timeSeriesHistograms, append(append([]byte(nil), inRange...), fields...)))
	}
	storeSeries := func(aggr []byte) []byte { return appendBytesField(nil, seriesChunks, aggr) }
	rawChunk := func(fields []byte) []byte { return storeSeries(appendBytesField(nil, aggrChunkRaw, fields)) }
	tests := map[string]struct {
		message []byte
		decode  func(*decoder, []byte) error
		kind    error
	}{
		"truncated tag":           {message: []byte{0x80}},
		"overlong varint":         {message: append([]byte{0x10}, bytes.Repeat([]byte{0xff}, 11)...)},
		"field zero":              {message: []byte{0x00, 0x00}},
		"length past the message": {message: []byte{0x0a, 0x05, 0x00}},
		"unterminated group":      {message: protowire.AppendTag(nil, 9, protowire.StartGroupType)},
		"series as varint":        {message: appendVarintField(nil, chunkedResponseSeries, 1)},
		"label as varint":         {message: appendBytesField(nil, chunkedResponseSeries, appendVarintField(nil, seriesLabels, 1))},
		"label name as varint":    {message: appendBytesField(nil, chunkedResponseSeries, appendBytesField(nil, seriesLabels, appendVarintField(nil, labelName, 1)))},
		"chunk as varint":         {message: appendBytesField(nil, chunkedResponseSeries, appendVarintField(nil, seriesChunks, 1))},
		"chunk time as bytes":     {message: chunk(appendBytesField(nil, remoteChunkMaxTime, nil))},
		"chunk data as varint":    {message: chunk(appendVarintField(xor, remoteChunkData, 1))},
		"chunk without data":      {message: chunk(xor)},
		"chunk malformed":         {message: chunk([]byte{0x80})},
		"series malformed":        {message: appendBytesField(nil, chunkedResponseSeries, []byte{0x80})},
		"label malformed":         {message: appendBytesField(nil, chunkedResponseSeries, appendBytesField(nil, seriesLabels, []byte{0x80}))},
		"chunk across the range, corrupt": {message: chunk(appendBytesField(appendVarintField(appendVarintField(
			appendVarintField(nil, remoteChunkType, uint64(prompb.Chunk_XOR)), remoteChunkMinTime, 50), remoteChunkMaxTime, 150),
			remoteChunkData, binary.BigEndian.AppendUint16(nil, 3)))},
		"query result malformed": {message: appendBytesField(nil, readResponseResults, []byte{0x80}), decode: (*decoder).readResponse},
		"time series malformed":  {message: samples([]byte{0x80}), decode: (*decoder).readResponse},
		"sample malformed":       {message: samples(appendBytesField(nil, timeSeriesSamples, []byte{0x80})), decode: (*decoder).readResponse},
		"histogram malformed":    {message: samples(appendBytesField(nil, timeSeriesHistograms, []byte{0x80})), decode: (*decoder).readResponse},
		"sample label as varint": {message: samples(appendBytesField(appendVarintField(nil, seriesLabels, 1), timeSeriesSamples,
			appendVarintField(nil, sampleTimestamp, decodeTestMint))), decode: (*decoder).readResponse},
		"results as varint":         {message: appendVarintField(nil, readResponseResults, 1), decode: (*decoder).readResponse},
		"time series as varint":     {message: appendBytesField(nil, readResponseResults, appendVarintField(nil, queryResultTimeseries, 1)), decode: (*decoder).readResponse},
		"sample as varint":          {message: samples(appendVarintField(nil, timeSeriesSamples, 1)), decode: (*decoder).readResponse},
		"sample value as varint":    {message: samples(appendBytesField(nil, timeSeriesSamples, appendVarintField(nil, sampleValue, 1))), decode: (*decoder).readResponse},
		"sample time as fixed64":    {message: samples(appendBytesField(nil, timeSeriesSamples, appendFixed64Field(nil, sampleTimestamp, 1))), decode: (*decoder).readResponse},
		"histogram schema as fixed": {message: hist(appendFixed64Field(nil, histogramSchema, 1)), decode: (*decoder).readResponse},
		"histogram sum as varint":   {message: hist(appendVarintField(nil, histogramSum, 1)), decode: (*decoder).readResponse},
		"histogram spans as varint": {message: hist(appendVarintField(nil, histogramPositiveSpans, 1)), decode: (*decoder).readResponse},
		"span offset as fixed":      {message: hist(appendBytesField(nil, histogramPositiveSpans, appendFixed64Field(nil, bucketSpanOffset, 1))), decode: (*decoder).readResponse},
		"span malformed":            {message: hist(appendBytesField(nil, histogramNegativeSpans, []byte{0x80})), decode: (*decoder).readResponse},
		"deltas as fixed":           {message: hist(appendFixed64Field(nil, histogramPositiveDeltas, 1)), decode: (*decoder).readResponse},
		"packed delta truncated":    {message: hist(appendBytesField(nil, histogramNegativeDeltas, []byte{0x80})), decode: (*decoder).readResponse},
		"packed counts of 7 bytes":  {message: hist(appendBytesField(nil, histogramPositiveCounts, make([]byte, 7))), decode: (*decoder).readResponse},
		"counts as varint":          {message: hist(appendVarintField(nil, histogramCustomValues, 1)), decode: (*decoder).readResponse},
		"aggr chunk time as bytes":  {message: storeSeries(appendBytesField(nil, aggrChunkMinTime, nil)), decode: storeSeriesDecode, kind: errStoreAPI},
		"raw chunk as varint":       {message: storeSeries(appendVarintField(nil, aggrChunkRaw, 1)), decode: storeSeriesDecode, kind: errStoreAPI},
		"raw chunk type as bytes":   {message: rawChunk(appendBytesField(nil, chunkType, nil)), decode: storeSeriesDecode, kind: errStoreAPI},
		"raw chunk data as varint":  {message: rawChunk(appendVarintField(nil, chunkData, 1)), decode: storeSeriesDecode, kind: errStoreAPI},
		"raw chunk malformed":       {message: rawChunk([]byte{0x80}), decode: storeSeriesDecode, kind: errStoreAPI},
		"store chunk malformed":     {message: storeSeries([]byte{0x80}), decode: storeSeriesDecode, kind: errStoreAPI},
		"response malformed":        {message: []byte{0x80}, decode: storeResponseDecode, kind: errStoreAPI},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			decode, kind := test.decode, test.kind
			if decode == nil {
				decode = (*decoder).chunkedReadResponse
			}
			if kind == nil {
				kind = errRemoteRead
			}
			d := newDecoder(nil, decodeTestMint, decodeTestMaxt, kind)
			err := decode(d, test.message)
			require.ErrorIs(t, err, kind)
			require.NotErrorIs(t, err, ErrFetchLimit)
		})
	}

	// fields a message doesn't declare are skipped, whatever their wire types
	unknown := protowire.AppendTag(nil, 9, protowire.StartGroupType)
	unknown = protowire.AppendTag(appendVarintField(unknown, 1, 1), 9, protowire.EndGroupType)
	unknown = appendFixed64Field(appendBytesField(unknown, 8, []byte{1}), 7, 1)
	unknown = append(unknown, protowire.AppendFixed32(protowire.AppendTag(nil, 6, protowire.Fixed32Type), 1)...)
	frame := append(append([]byte(nil), unknown...), chunkedFrame(t, &prompb.ChunkedSeries{
		Labels: testLabels("a"), Chunks: []prompb.Chunk{xorChunk(t, pointsAt(110)...)},
	})...)
	frame = appendVarintField(frame, 2, 7)
	label := appendVarintField(appendBytesField(appendBytesField(nil, labelName, []byte("b")), labelValue, []byte("1")), 3, 1)
	frame = appendBytesField(frame, chunkedResponseSeries, appendBytesField(appendBytesField(nil, seriesLabels, label),
		seriesChunks, marshalChunk(t, xorChunk(t, pointsAt(120)...))))
	series, err := decodeFrames(t, nil, frame)
	require.NoError(t, err)
	require.Len(t, series, 2)
	require.Equal(t, labels.FromStrings("b", "1"), series[1].Labels())

	sp := appendVarintField(appendVarintField(appendVarintField(nil, 3, 1), bucketSpanLength, 1), bucketSpanOffset, 0)
	h := appendBytesField(appendVarintField(appendVarintField(nil, histogramCountInt, 1), histogramTimestamp, 120),
		histogramPositiveSpans, sp)
	h = appendBytesField(h, histogramPositiveDeltas, protowire.AppendVarint(nil, protowire.EncodeZigZag(1)))
	ts := appendBytesField(appendBytesField(unknown, timeSeriesHistograms, h), timeSeriesSamples,
		appendVarintField(appendFixed64Field(appendVarintField(nil, 3, 1), sampleValue, math.Float64bits(2)), sampleTimestamp, 110))
	data := appendBytesField(unknown, readResponseResults, appendBytesField(unknown, queryResultTimeseries, ts))
	d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
	require.NoError(t, d.readResponse(data))
	require.Len(t, d.out, 1)
	require.Equal(t, []testPoint{{t: 110, f: 2}, {t: 120, h: true}}, readPoints(t, d.out[0]))
}

func marshalChunk(t *testing.T, c prompb.Chunk) []byte {
	t.Helper()
	b, err := c.Marshal()
	require.NoError(t, err)
	return b
}

func TestAppendDeltasStopsAtMalformedVarint(t *testing.T) {
	packed := append(protowire.AppendVarint(nil, protowire.EncodeZigZag(2)), 0x80)
	got, last := appendDeltas(nil, wireField{typ: protowire.BytesType, b: packed}, 1)
	require.Equal(t, []float64{3}, got)
	require.InDelta(t, 3.0, last, 0)
}

func TestSamplesWithin(t *testing.T) {
	c := xorChunk(t, pointsAt(10, 20, 30)...)
	chk, err := newChunk(chunkenc.EncXOR, c.Data)
	require.NoError(t, err)
	for _, test := range []struct {
		sp         span
		mint, maxt int64
		want       int64
	}{
		{sp: span{10, 30}, mint: 40, maxt: 50, want: 0},
		{sp: span{10, 30}, mint: 0, maxt: 50, want: 3},
		{sp: span{10, 30}, mint: 15, maxt: 25, want: 1},
		{sp: span{1, 0}, mint: 0, maxt: 20, want: 2},
	} {
		n, err := samplesWithin(chk, test.sp, test.mint, test.maxt)
		require.NoError(t, err)
		require.Equal(t, test.want, n)
	}
}

func storeSeriesDecode(d *decoder, b []byte) error { return d.chunkedSeries(b, storeChunk) }

func storeResponseDecode(d *decoder, b []byte) error {
	_, err := d.seriesResponse(b)
	return err
}

func TestRemoteReadByteLimitBoundaries(t *testing.T) {
	many := make([]int64, 500)
	for i := range many {
		many[i] = decodeTestMint + int64(i)
	}
	t.Run("streamed", func(t *testing.T) {
		points := pointsAt(many...)
		for i := range points {
			// varied values encode as more bytes, so the chunk's data outweighs what decoding adds
			points[i].f = math.Sqrt(float64(i))
		}
		var body bytes.Buffer
		writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{xorChunk(t, points...)}})
		source := newTestRemoteRead(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(headers.NameContentType, contentTypeStreamed)
			_, _ = w.Write(body.Bytes())
		})
		size := int64(body.Len())
		series, _, err := source.Select(testContext(t, &Budget{MaxResponseBytes: size}), 0, 1000, nil)
		require.NoError(t, err, "a response of exactly its byte limit is read")
		require.Equal(t, points, readPoints(t, series[0]))
		_, _, err = source.Select(testContext(t, &Budget{MaxResponseBytes: size - 1}), 0, 1000, nil)
		require.ErrorIs(t, err, ErrFetchLimit)
	})
	t.Run("samples", func(t *testing.T) {
		samples := make([]prompb.Sample, len(many))
		for i, at := range many {
			samples[i] = prompb.Sample{Timestamp: at, Value: 1}
		}
		data := samplesResponse(t, &prompb.TimeSeries{Labels: testLabels("a"), Samples: samples})
		compressed := snappy.Encode(nil, data)
		require.Less(t, len(compressed), len(data))
		source := newTestRemoteRead(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(headers.NameContentType, contentTypeProtobuf)
			_, _ = w.Write(compressed)
		})
		size := int64(len(data))
		series, _, err := source.Select(testContext(t, &Budget{MaxResponseBytes: size}), 0, 1000, nil)
		require.NoError(t, err, "a response decompressing to exactly its byte limit is read")
		require.Len(t, series[0].(*sampleSeries).samples, len(many))
		_, _, err = source.Select(testContext(t, &Budget{MaxResponseBytes: size - 1}), 0, 1000, nil)
		require.ErrorIs(t, err, ErrFetchLimit)
	})
}

func TestStoreAPIByteLimitBoundary(t *testing.T) {
	points := make([]testPoint, 500)
	for i := range points {
		points[i] = testPoint{t: int64(i), f: math.Sqrt(float64(i))}
	}
	data := xorData(t, points...)
	frames := [][]byte{
		encodeTestSeries([]string{labels.MetricName, testMetric, testPod, "a"}, spanned(encodeTestChunk(storeChunkXOR, data, true), 0, 499)),
		encodeTestSeries([]string{labels.MetricName, testMetric, testPod, "b"}, spanned(encodeTestChunk(storeChunkXOR, data, true), 0, 499)),
	}
	size := int64(len(frames[0]) + len(frames[1]))
	source := (&testStore{frames: frames}).serve(t)
	series, _, err := source.Select(testContext(t, &Budget{MaxResponseBytes: size}), 0, 1000, nil)
	require.NoError(t, err, "messages of exactly the byte limit are read")
	require.Len(t, series, 2)
	_, _, err = source.Select(testContext(t, &Budget{MaxResponseBytes: size - 1}), 0, 1000, nil)
	require.ErrorIs(t, err, ErrFetchLimit)
}

func TestStoreAPIKeepsOnlyInRange(t *testing.T) {
	lset := []string{labels.MetricName, testMetric, testPod, "a"}
	inRange := xorData(t, pointsAt(110, 120)...)
	frames := [][]byte{
		encodeTestSeries(lset, spanned(encodeTestChunk(storeChunkXOR, xorData(t, pointsAt(10, 20)...), true), 10, 20)),
		encodeTestSeries([]string{labels.MetricName, testMetric, testPod, "b"},
			spanned(encodeTestChunk(storeChunkXOR, xorData(t, pointsAt(300)...), true), 300, 300)),
		encodeTestSeries(lset, spanned(encodeTestChunk(storeChunkXOR, inRange, true), 110, 120)),
	}
	budget := &Budget{}
	series, _, err := (&testStore{frames: frames}).serve(t).Select(testContext(t, budget), decodeTestMint, decodeTestMaxt, nil)
	require.NoError(t, err)
	require.Len(t, series, 1)
	require.Equal(t, []span{{110, 120}}, series[0].(*chunkSeries).spans)
	fetchedSeries, fetchedSamples := budget.Fetched()
	require.Equal(t, int64(1), fetchedSeries)
	require.Equal(t, int64(2), fetchedSamples)

	stop := errors.New("no evaluation can afford more")
	budget = &Budget{share: func(int64, int64, []uint64) error { return stop }}
	_, _, err = (&testStore{frames: frames}).serve(t).Select(testContext(t, budget), decodeTestMint, decodeTestMaxt, nil)
	require.ErrorIs(t, err, stop, "what a message counts is added as it ends")
}

func BenchmarkDecodeChunkedFrame(b *testing.B) {
	series := make([]*prompb.ChunkedSeries, 200)
	for i := range series {
		cs := &prompb.ChunkedSeries{Labels: prompb.FromLabels(labels.FromStrings(labels.MetricName, testMetric,
			testPod, string(rune('a'+i%26))+string(rune('a'+i/26)), "job", "node", "instance", "host:9100"), nil)}
		for c := range 4 {
			points := make([]testPoint, 120)
			for j := range points {
				points[j] = testPoint{t: int64(c*120+j) * 15000, f: float64(j % 7)}
			}
			cs.Chunks = append(cs.Chunks, xorChunk(b, points...))
		}
		series[i] = cs
	}
	frame := chunkedFrame(b, series...)
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		d := newDecoder(nil, 0, 4*120*15000, errRemoteRead)
		if err := d.chunkedReadResponse(frame); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeSamples(b *testing.B) {
	series := make([]*prompb.TimeSeries, 200)
	for i := range series {
		ts := &prompb.TimeSeries{Labels: prompb.FromLabels(labels.FromStrings(labels.MetricName, testMetric,
			testPod, string(rune('a'+i%26))+string(rune('a'+i/26)), "job", "node"), nil)}
		for j := range 120 {
			ts.Samples = append(ts.Samples, prompb.Sample{Timestamp: int64(j) * 15000, Value: float64(j % 7)})
		}
		series[i] = ts
	}
	data := samplesResponse(b, series...)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		d := newDecoder(nil, 0, 120*15000, errRemoteRead)
		if err := d.readResponse(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeMixedSamples(b *testing.B) {
	series := make([]*prompb.TimeSeries, 200)
	h := testHistogram()
	for i := range series {
		ts := &prompb.TimeSeries{Labels: prompb.FromLabels(labels.FromStrings(labels.MetricName, testMetric,
			testPod, string(rune('a'+i%26))+string(rune('a'+i/26)), "job", "node"), nil)}
		// floats and histograms alternate, so the two kinds are merged
		for j := range 60 {
			ts.Samples = append(ts.Samples, prompb.Sample{Timestamp: int64(2*j) * 15000, Value: float64(j % 7)})
			ts.Histograms = append(ts.Histograms, prompb.FromIntHistogram(int64(2*j+1)*15000, h))
		}
		series[i] = ts
	}
	data := samplesResponse(b, series...)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		d := newDecoder(nil, 0, 120*15000, errRemoteRead)
		if err := d.readResponse(data); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDecoderSeriesLimitStopsBeforeKeeping(t *testing.T) {
	const limit, sent = 3, 10
	name := func(i int) []prompb.Label { return testLabels(strconv.Itoa(i)) }
	chunked := make([]*prompb.ChunkedSeries, sent)
	timeSeries := make([]*prompb.TimeSeries, sent)
	var messages [][]byte
	for i := range sent {
		chunked[i] = &prompb.ChunkedSeries{Labels: name(i), Chunks: []prompb.Chunk{xorChunk(t, pointsAt(110)...)}}
		timeSeries[i] = &prompb.TimeSeries{Labels: name(i), Samples: []prompb.Sample{{Timestamp: 110, Value: 1}}}
		messages = append(messages, encodeTestSeries([]string{labels.MetricName, testMetric, testPod, strconv.Itoa(i)},
			spanned(encodeTestChunk(storeChunkXOR, xorData(t, pointsAt(110)...), true), 110, 110)))
	}
	frame, samples := chunkedFrame(t, chunked...), samplesResponse(t, timeSeries...)
	decodes := map[string]func(*decoder) error{
		"remote read": func(d *decoder) error { return d.chunkedReadResponse(frame) },
		"samples":     func(d *decoder) error { return d.readResponse(samples) },
		"storeapi": func(d *decoder) error {
			for _, m := range messages {
				if _, err := d.seriesResponse(m); err != nil {
					return err
				}
			}
			return nil
		},
	}
	for name, decode := range decodes {
		t.Run(name, func(t *testing.T) {
			budget := &Budget{MaxSeries: limit}
			d := newDecoder(budget, decodeTestMint, decodeTestMaxt, errRemoteRead)
			require.ErrorIs(t, decode(d), ErrFetchLimit)
			require.Len(t, d.out, limit, "the read stops at the first series the budget can't afford, before keeping it")
			series, _ := budget.Fetched()
			require.Equal(t, int64(limit+1), series)
		})
	}
}

func TestDecoderCountsCopiesOnceUnderASeriesLimit(t *testing.T) {
	// pod a is sent, then b, then a again, a copy deduplication joins to the first rather than a series of its own
	pods, times := []string{"a", "b", "a"}, []int64{110, 120, 130}
	var chunked []*prompb.ChunkedSeries
	var timeSeries []*prompb.TimeSeries
	var messages [][]byte
	for i, pod := range pods {
		chunked = append(chunked, &prompb.ChunkedSeries{
			Labels: testLabels(pod),
			Chunks: []prompb.Chunk{xorChunk(t, pointsAt(times[i])...)},
		})
		timeSeries = append(timeSeries, &prompb.TimeSeries{
			Labels:  testLabels(pod),
			Samples: []prompb.Sample{{Timestamp: times[i], Value: 1}},
		})
		messages = append(messages, encodeTestSeries([]string{labels.MetricName, testMetric, testPod, pod},
			spanned(encodeTestChunk(storeChunkXOR, xorData(t, pointsAt(times[i])...), true), times[i], times[i])))
	}
	frame, samples := chunkedFrame(t, chunked...), samplesResponse(t, timeSeries...)
	decodes := map[string]func(*decoder) error{
		"remote read": func(d *decoder) error { return d.chunkedReadResponse(frame) },
		"samples":     func(d *decoder) error { return d.readResponse(samples) },
		"storeapi": func(d *decoder) error {
			for _, m := range messages {
				if _, err := d.seriesResponse(m); err != nil {
					return err
				}
			}
			return nil
		},
	}
	id := func(pod string) uint64 { return labels.FromStrings(labels.MetricName, testMetric, testPod, pod).Hash() }
	for name, decode := range decodes {
		t.Run(name, func(t *testing.T) {
			var shared []uint64
			budget := &Budget{MaxSeries: 2, share: func(_, _ int64, ids []uint64) error {
				shared = append(shared, ids...)
				return nil
			}}
			d := newDecoder(budget, decodeTestMint, decodeTestMaxt, errRemoteRead)
			require.NoError(t, decode(d))
			require.Len(t, d.out, 3, "the copy is kept, to be joined to its series")
			series, samples := budget.Fetched()
			require.Equal(t, [2]int64{2, 3}, [2]int64{series, samples})
			require.Equal(t, []uint64{id("a"), id("b")}, shared, "each series is shared with its id once")
			require.ErrorIs(t, decode(newDecoder(&Budget{MaxSeries: 1}, decodeTestMint, decodeTestMaxt, errRemoteRead)),
				ErrFetchLimit)
			// without a limit, series aren't told apart, so the copy counts as it arrives, and no set of ids takes memory
			unlimited := &Budget{}
			plain := newDecoder(unlimited, decodeTestMint, decodeTestMaxt, errRemoteRead)
			require.NoError(t, decode(plain))
			series, _ = unlimited.Fetched()
			require.Equal(t, int64(3), series)
			require.Equal(t, int64(2*seenBytes), plain.allowance-d.allowance)
		})
	}
}

func TestDecoderAllowanceSpansAResponse(t *testing.T) {
	const sent = 10
	var messages, responses [][]byte
	timeSeries := make([]*prompb.TimeSeries, sent)
	for i := range sent {
		messages = append(messages, encodeTestSeries([]string{labels.MetricName, testMetric, testPod, strconv.Itoa(i)},
			spanned(encodeTestChunk(storeChunkXOR, xorData(t, pointsAt(110)...), true), 110, 110)))
		timeSeries[i] = &prompb.TimeSeries{Labels: testLabels(strconv.Itoa(i)), Samples: []prompb.Sample{{Timestamp: 110, Value: 1}}}
		responses = append(responses, samplesResponse(t, timeSeries[i]))
	}
	storeDecode := func(d *decoder, messages ...[]byte) error {
		for _, m := range messages {
			if _, err := d.seriesResponse(m); err != nil {
				return err
			}
		}
		return nil
	}
	storeAlone, samplesAlone := make([]func(*decoder) error, sent), make([]func(*decoder) error, sent)
	for i := range sent {
		storeAlone[i] = func(d *decoder) error { return storeDecode(d, messages[i]) }
		samplesAlone[i] = func(d *decoder) error { return d.readResponse(responses[i]) }
	}
	tests := map[string]struct {
		// alone decode each series as a response of its own, and whole decodes them all as one response
		alone []func(*decoder) error
		whole func(*decoder) error
	}{
		"storeapi messages": {alone: storeAlone, whole: func(d *decoder) error { return storeDecode(d, messages...) }},
		"time series": {alone: samplesAlone, whole: func(d *decoder) error {
			return d.readResponse(samplesResponse(t, timeSeries...))
		}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// an allowance each series fits on its own, but not all of them
			var most int64
			for _, decode := range test.alone {
				d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
				require.NoError(t, decode(d))
				most = max(most, d.limit-d.allowance)
			}
			d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
			d.limit, d.allowance = most, most
			require.ErrorIs(t, test.whole(d), ErrFetchLimit, "the allowance is the whole response's")
			require.Less(t, len(d.out), sent)
		})
	}
}

func TestDecoderSamplesAllowanceBoundary(t *testing.T) {
	data := samplesResponse(t, &prompb.TimeSeries{Labels: testLabels("a"), Samples: []prompb.Sample{
		{Timestamp: 110, Value: 1}, {Timestamp: 120, Value: 2}, {Timestamp: 300, Value: 3},
	}})
	// two label slots, the series with its labels, and a slot for each sample in range
	labelsSize := int64(len(labels.MetricName)+len(testMetric)+len(testPod)+len("a")) + 2*labelLengthBytes
	cost := 2*labelSlotBytes + sampleSeriesBytes + labelsSize + 2*sampleBytes
	for _, limit := range []int64{cost, cost - 1} {
		d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
		d.limit, d.allowance = limit, limit
		err := d.readResponse(data)
		if limit == cost {
			require.NoError(t, err)
			require.Zero(t, d.allowance)
			continue
		}
		require.ErrorIs(t, err, ErrFetchLimit)
		require.Empty(t, d.out, "the series over the limit is never kept")
	}
}

func TestDecoderSortsLabels(t *testing.T) {
	unsorted := []prompb.Label{{Name: testPod, Value: "a"}, {Name: labels.MetricName, Value: testMetric}}
	want := labels.FromStrings(labels.MetricName, testMetric, testPod, "a")
	d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
	require.NoError(t, d.readResponse(samplesResponse(t, &prompb.TimeSeries{
		Labels:  unsorted,
		Samples: []prompb.Sample{{Timestamp: 110, Value: 1}},
	})))
	require.Equal(t, want, d.out[0].Labels())
	d = newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
	require.NoError(t, d.chunkedReadResponse(chunkedFrame(t, &prompb.ChunkedSeries{
		Labels: unsorted,
		Chunks: []prompb.Chunk{xorChunk(t, pointsAt(110)...)},
	})))
	require.Equal(t, want, d.out[0].Labels())
}

func TestDecoderReadsUnknownSpansThrough(t *testing.T) {
	// a span whose start is after its end is unknown, so the chunk is read for its samples in range
	data := xorData(t, pointsAt(50, 110, 120)...)
	unknown := prompb.Chunk{Type: prompb.Chunk_XOR, Data: data, MinTimeMs: 300, MaxTimeMs: 50}
	frame := chunkedFrame(t, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{unknown}})
	storeSeries := appendBytesField(nil, seriesChunks, spanned(encodeTestChunk(storeChunkXOR, data, true), 300, 50))
	decodes := map[string]func(*decoder) error{
		"remote read": func(d *decoder) error { return d.chunkedReadResponse(frame) },
		"storeapi":    func(d *decoder) error { return d.chunkedSeries(storeSeries, storeChunk) },
	}
	for name, decode := range decodes {
		t.Run(name, func(t *testing.T) {
			budget := &Budget{}
			d := newDecoder(budget, decodeTestMint, decodeTestMaxt, errRemoteRead)
			require.NoError(t, decode(d))
			require.NoError(t, d.flush())
			require.Len(t, d.out, 1)
			require.Equal(t, pointsAt(110, 120), readPoints(t, bound(d.out, decodeTestMint, decodeTestMaxt)[0]))
			_, samples := budget.Fetched()
			require.Equal(t, int64(2), samples)
		})
	}
}

func TestDecoderReadsThroughWithoutAllocating(t *testing.T) {
	frame := func(points []testPoint, sp span) []byte {
		series := make([]*prompb.ChunkedSeries, 1000)
		for i := range series {
			c := xorChunk(t, points...)
			c.MinTimeMs, c.MaxTimeMs = sp.mint, sp.maxt
			series[i] = &prompb.ChunkedSeries{Labels: testLabels(strconv.Itoa(i)), Chunks: []prompb.Chunk{c}}
		}
		return chunkedFrame(t, series...)
	}
	across, inside := frame(pointsAt(150, 250), span{150, 250}), frame(pointsAt(150, 190), span{150, 190})
	acrossAllocs, _, err := decodeAllocs(t, func(d *decoder) error { return d.chunkedReadResponse(across) })
	require.NoError(t, err)
	insideAllocs, _, err := decodeAllocs(t, func(d *decoder) error { return d.chunkedReadResponse(inside) })
	require.NoError(t, err)
	// one chunk and iterator are reused for every chunk read through
	require.LessOrEqual(t, acrossAllocs, insideAllocs+2)

	d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
	require.NoError(t, d.chunkedReadResponse(across))
	require.NotNil(t, d.scratch[chunkenc.EncXOR].chk)
	d.release()
	require.Equal(t, [len(d.scratch)]scratchChunk{}, d.scratch, "nothing still refers to the data read through")
}

func TestDecoderAllowanceFitsOrdinaryResponses(t *testing.T) {
	const now, series = 1_760_000_000_000, 1000
	mint, maxt := int64(now-5*60*1000), int64(now)
	shortLabels := func(i int) []string { return []string{labels.MetricName, "up", "i", strconv.Itoa(i)} }
	t.Run("instant query chunks", func(t *testing.T) {
		chunked := make([]*prompb.ChunkedSeries, series)
		var messages [][]byte
		var storeSize int64
		for i := range chunked {
			c := xorChunk(t, testPoint{t: now - 15000, f: float64(i)})
			chunked[i] = &prompb.ChunkedSeries{
				Labels: prompb.FromLabels(labels.FromStrings(shortLabels(i)...), nil),
				Chunks: []prompb.Chunk{c},
			}
			m := encodeTestSeries(shortLabels(i), spanned(encodeTestChunk(storeChunkXOR, c.Data, true), c.MinTimeMs, c.MaxTimeMs))
			messages, storeSize = append(messages, m), storeSize+int64(len(m))
		}
		var body bytes.Buffer
		writeFrame(t, &body, chunked...)
		frame := chunkedFrame(t, chunked...)
		decodes := map[string]struct {
			size   int64
			decode func(*decoder) error
		}{
			"remote read": {size: int64(body.Len()), decode: func(d *decoder) error { return d.chunkedReadResponse(frame) }},
			"storeapi": {size: storeSize, decode: func(d *decoder) error {
				for _, m := range messages {
					if _, err := d.seriesResponse(m); err != nil {
						return err
					}
				}
				return nil
			}},
		}
		for name, test := range decodes {
			t.Run(name, func(t *testing.T) {
				d := newDecoder(&Budget{MaxResponseBytes: test.size}, mint, maxt, errRemoteRead)
				require.NoError(t, test.decode(d), "a response of exactly its byte limit decodes")
				require.Len(t, d.out, series)
				require.Greater(t, d.limit-d.allowance, 2*test.size, "small chunks and short labels expand past twice their size")
			})
		}
	})
	t.Run("samples of histograms", func(t *testing.T) {
		h := &histogram.Histogram{
			Schema: 3, Count: 400, Sum: 900, ZeroThreshold: 1e-128, ZeroCount: 3,
			PositiveSpans: []histogram.Span{{Offset: -20, Length: 60}, {Offset: 2, Length: 40}},
		}
		for i := range 100 {
			h.PositiveBuckets = append(h.PositiveBuckets, int64(i%7-3))
		}
		timeSeries := make([]*prompb.TimeSeries, series)
		for i := range timeSeries {
			timeSeries[i] = &prompb.TimeSeries{
				Labels:     prompb.FromLabels(labels.FromStrings(shortLabels(i)...), nil),
				Histograms: []prompb.Histogram{prompb.FromIntHistogram(now-15000, h)},
			}
		}
		data := samplesResponse(t, timeSeries...)
		// the response takes half its byte limit
		budget := &Budget{MaxResponseBytes: 2 * int64(len(data))}
		d := newDecoder(budget, mint, maxt, errRemoteRead)
		require.NoError(t, d.readResponse(data))
		require.Len(t, d.out, series)
		require.Greater(t, d.limit-d.allowance, 2*budget.MaxResponseBytes, "integer buckets expand from a byte or so to eight")
	})
}

func TestDecoderReleasesReplacedBuffers(t *testing.T) {
	const messages, pad = 20, 1 << 20
	data := xorData(t, pointsAt(110, 210)...)
	// each message is larger than the last, so it is received into a new buffer, and has fewer labels, so the label
	// builder's slots past them would still hold the labels of messages before it
	series := func(i int, chunk []byte) []byte {
		var b []byte
		for j := range messages - i {
			label := appendBytesField(appendBytesField(nil, labelName, []byte("l"+strconv.Itoa(j))), labelValue, []byte("v"))
			b = appendBytesField(b, seriesLabels, label)
		}
		// the chunk lies across the range's end, so it is read through
		b = appendBytesField(b, seriesChunks, chunk)
		return appendBytesField(b, 100, make([]byte, pad+i<<10))
	}
	retained := func(t *testing.T, start func(*Budget) <-chan error, finish func()) int64 {
		t.Helper()
		decoded := make(chan struct{}, messages)
		budget := &Budget{share: func(int64, int64, []uint64) error {
			decoded <- struct{}{}
			return nil
		}}
		var before, during runtime.MemStats
		runtime.GC()
		runtime.GC()
		runtime.ReadMemStats(&before)
		done := start(budget)
		for range messages {
			select {
			case <-decoded:
			case <-time.After(storeTestWait):
				t.Fatal("the messages were never decoded")
			}
		}
		runtime.GC()
		runtime.GC()
		runtime.ReadMemStats(&during)
		finish()
		require.NoError(t, <-done)
		return int64(during.HeapAlloc) - int64(before.HeapAlloc)
	}
	t.Run("remote read", func(t *testing.T) {
		chunk := marshalChunk(t, prompb.Chunk{Type: prompb.Chunk_XOR, Data: data, MinTimeMs: 110, MaxTimeMs: 210})
		var body bytes.Buffer
		for i := range messages {
			frame := appendBytesField(nil, chunkedResponseSeries, series(i, chunk))
			body.Write(binary.AppendUvarint(nil, uint64(len(frame))))
			body.Write(binary.BigEndian.AppendUint32(nil, crc32.Checksum(frame, castagnoli)))
			body.Write(frame)
		}
		r, w := io.Pipe()
		got := retained(t, func(budget *Budget) <-chan error {
			go func() { _, _ = w.Write(body.Bytes()) }()
			done := make(chan error, 1)
			go func() {
				_, err := decodeChunked(r, newDecoder(budget, decodeTestMint, decodeTestMaxt, errRemoteRead))
				done <- err
			}()
			return done
		}, func() { w.Close() })
		runtime.KeepAlive(body.Bytes())
		t.Logf("retained %d", got)
		require.Less(t, got, int64(messages/2*pad), "a replaced frame's buffer is held by nothing")
	})
	t.Run("storeapi", func(t *testing.T) {
		chunk := spanned(encodeTestChunk(storeChunkXOR, data, true), 110, 210)
		frames := make([][]byte, messages)
		for i := range frames {
			frames[i] = appendBytesField(nil, seriesResponseSeries, series(i, chunk))
		}
		proceed := make(chan struct{})
		store := &testStore{frames: frames, hold: func(ctx context.Context) {
			select {
			case <-proceed:
			case <-ctx.Done():
			}
		}}
		source := store.serve(t)
		got := retained(t, func(budget *Budget) <-chan error {
			done := make(chan error, 1)
			go func() {
				_, _, err := source.Select(testContext(t, budget), decodeTestMint, decodeTestMaxt, nil)
				done <- err
			}()
			return done
		}, func() { close(proceed) })
		t.Logf("retained %d", got)
		require.Less(t, got, int64(messages/2*pad), "a replaced message's buffer is held by nothing")
	})
}
