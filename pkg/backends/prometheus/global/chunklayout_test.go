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
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
)

// testVarbitSizes are the widths of each varbit form short of the widest, as the library writes them
var testVarbitSizes = []int{3, 6, 9, 12, 18, 25, 56}

// testBits writes bits from the most significant of each byte, as the library's chunks hold them
type testBits struct {
	b []byte
	n int
}

func (w *testBits) write(v uint64, bits int) {
	for i := bits - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>i&1 == 1 {
			w.b[len(w.b)-1] |= 1 << (7 - w.n%8)
		}
		w.n++
	}
}

// varbit writes v in the narrowest form fits allows
func (w *testBits) varbit(v uint64, fits func(size int) bool) {
	if v == 0 {
		w.write(0, 1)
		return
	}
	for i, size := range testVarbitSizes {
		if fits(size) {
			w.write(1<<(i+2)-2, i+2)
			w.write(v, size)
			return
		}
	}
	w.write(math.MaxUint8, 8)
	w.write(v, 64)
}

func (w *testBits) varbitUint(v uint64) { w.varbit(v, func(size int) bool { return v < 1<<size }) }

func (w *testBits) varbitInt(v int64) {
	w.varbit(uint64(v), func(size int) bool { return v > -(1<<(size-1)) && v <= 1<<(size-1) })
}

// testSpan is a bucket span as a chunk's layout writes it, its length a varbit of any width
type testSpan struct {
	length uint64
	offset int64
}

// testLayout is a histogram chunk's bucket layout, and its first sample's buckets, each written as a delta of zero
type testLayout struct {
	threshold float64
	schema    int32
	pos, neg  []testSpan
	custom    []float64
	// buckets are written for the first sample, and none when it is negative
	buckets int
}

// layoutChunk writes a histogram chunk of enc claiming samples, whose layout is l
func layoutChunk(enc chunkenc.Encoding, samples uint16, l testLayout) []byte {
	return writeLayout(enc, samples, l).b
}

// writeLayout writes a histogram chunk of enc claiming samples, whose layout is l, returning its bits
func writeLayout(enc chunkenc.Encoding, samples uint16, l testLayout) *testBits {
	w := &testBits{b: append(binary.BigEndian.AppendUint16(nil, samples), 0), n: histogramHeaderBytes * 8}
	if l.threshold == 0 {
		w.write(0, 8)
	} else {
		w.write(zeroThresholdFloat, 8)
		w.write(math.Float64bits(l.threshold), 64)
	}
	w.varbitInt(int64(l.schema))
	for _, spans := range [][]testSpan{l.pos, l.neg} {
		w.varbitUint(uint64(len(spans)))
		for _, sp := range spans {
			w.varbitUint(sp.length)
			w.varbitInt(sp.offset)
		}
	}
	if histogram.IsCustomBucketsSchema(l.schema) {
		w.varbitUint(uint64(len(l.custom)))
		for _, bound := range l.custom {
			// a bound that is a whole number of thousandths is written as one more than that number
			if scaled := math.Round(bound * 1000); scaled >= 0 && scaled < 1<<56-1 && scaled/1000 == bound {
				w.varbitUint(uint64(scaled) + 1)
				continue
			}
			w.varbitUint(0)
			w.write(math.Float64bits(bound), 64)
		}
	}
	if l.buckets < 0 {
		return w
	}
	// the first sample at time zero, of counts and a sum of zero
	w.varbitInt(0)
	if enc == chunkenc.EncFloatHistogram {
		w.write(0, 3*64)
		for range l.buckets {
			w.write(0, floatBucketBits)
		}
		return w
	}
	w.varbitUint(0)
	w.varbitUint(0)
	w.write(0, 64)
	for range l.buckets {
		w.varbitInt(0)
	}
	return w
}

// layoutCost is what an iterator of enc lays out for l
func layoutCost(enc chunkenc.Encoding, l testLayout) int64 {
	per := int64(histogramLayoutBytes)
	if enc == chunkenc.EncFloatHistogram {
		per = floatHistogramLayoutBytes
	}
	var buckets int64
	for _, sp := range append(append([]testSpan(nil), l.pos...), l.neg...) {
		buckets += int64(uint32(sp.length))
	}
	return buckets*per + int64(len(l.pos)+len(l.neg))*spanBytes + int64(len(l.custom))*bucketBytes
}

var histogramEncodings = []chunkenc.Encoding{chunkenc.EncHistogram, chunkenc.EncFloatHistogram}

func TestHistogramLayoutMatchesPrometheus(t *testing.T) {
	ih := &histogram.Histogram{
		Schema: 3, Count: 12, Sum: 3.5, ZeroThreshold: 0.001, ZeroCount: 2,
		PositiveSpans: []histogram.Span{{Offset: -1, Length: 2}, {Offset: 3, Length: 1}}, PositiveBuckets: []int64{1, 2, -1},
		NegativeSpans: []histogram.Span{{Offset: 0, Length: 2}}, NegativeBuckets: []int64{3, -2},
	}
	custom := &histogram.Histogram{
		Schema: histogram.CustomBucketsSchema, Count: 3, Sum: 4, CustomValues: []float64{1, 2.5, -7, 1e9},
		PositiveSpans: []histogram.Span{{Offset: 0, Length: 2}}, PositiveBuckets: []int64{1, 1},
	}
	coarse := testHistogram()
	coarse.Schema = histogram.ExponentialSchemaMin
	for name, h := range map[string]*histogram.Histogram{"exponential": ih, "custom": custom, "coarse": coarse} {
		for _, enc := range histogramEncodings {
			t.Run(name+"/"+enc.String(), func(t *testing.T) {
				chk, err := chunkenc.NewEmptyChunk(enc)
				require.NoError(t, err)
				laid, err := histogramLayout(enc, chk.Bytes())
				require.NoError(t, err)
				require.Zero(t, laid, "an empty chunk has no layout")
				app, err := chk.Appender()
				require.NoError(t, err)
				for at := range int64(3) {
					if enc == chunkenc.EncFloatHistogram {
						_, _, app, err = app.AppendFloatHistogram(nil, 0, at, h.ToFloat(nil), false)
					} else {
						_, _, app, err = app.AppendHistogram(nil, 0, at, h, false)
					}
					require.NoError(t, err)
				}
				per := int64(histogramLayoutBytes)
				if enc == chunkenc.EncFloatHistogram {
					per = floatHistogramLayoutBytes
				}
				laid, err = histogramLayout(enc, chk.Bytes())
				require.NoError(t, err)
				require.Equal(t, int64(len(h.PositiveBuckets)+len(h.NegativeBuckets))*per+
					int64(len(h.PositiveSpans)+len(h.NegativeSpans))*spanBytes+int64(len(h.CustomValues))*bucketBytes, laid)
			})
		}
	}
	_, err := histogramLayout(chunkenc.EncXOR, nil)
	require.NoError(t, err, "other encodings lay out nothing, and their data isn't read")
}

func TestHistogramLayoutReadsWhatPrometheusReads(t *testing.T) {
	// every width of varbit the library writes, and the forms a threshold and a custom bound take
	var pos []testSpan
	for _, size := range testVarbitSizes {
		pos = append(pos, testSpan{length: 1, offset: 1<<(size-1) - 1}, testSpan{length: 1, offset: -(1 << (size - 2))})
	}
	pos = append(pos, testSpan{length: 2, offset: math.MinInt32})
	layouts := map[string]testLayout{
		"exponential": {threshold: 0.3, schema: -2, pos: pos, neg: []testSpan{{length: 3, offset: 2}, {length: 1, offset: 9}}},
		"wide schema": {schema: histogram.ExponentialSchemaMaxReserved, neg: []testSpan{{length: 1 << 12, offset: 0}}},
		"custom": {
			schema: histogram.CustomBucketsSchema, pos: []testSpan{{length: 4, offset: 0}},
			custom: []float64{0.001, 2.5, 33554.43, -1, 33554.431, 1e-7},
		},
		// a span's length and offset keep their low 32 bits, whatever form they are written in
		"wide lengths": {schema: 0, pos: []testSpan{{length: 1<<40 | 3, offset: -1}, {length: 1<<60 | 1<<32 | 2, offset: 1<<60 | 7}}},
	}
	for name, l := range layouts {
		for _, enc := range histogramEncodings {
			t.Run(name+"/"+enc.String(), func(t *testing.T) {
				for _, sp := range append(append([]testSpan(nil), l.pos...), l.neg...) {
					l.buckets += int(uint32(sp.length))
				}
				data := layoutChunk(enc, 1, l)
				laid, err := histogramLayout(enc, data)
				require.NoError(t, err)
				require.Equal(t, layoutCost(enc, l), laid)
				chk, err := chunkenc.FromData(enc, data)
				require.NoError(t, err)
				it := chk.Iterator(nil)
				require.NotEqual(t, chunkenc.ValNone, it.Next())
				require.NoError(t, it.Err())
				_, fh := it.AtFloatHistogram(nil)
				require.InDelta(t, l.threshold, fh.ZeroThreshold, 0)
				if l.schema > histogram.ExponentialSchemaMax {
					// the iterator reduces a schema finer than the engine reads, merging its buckets
					require.Equal(t, histogram.ExponentialSchemaMax, fh.Schema)
					return
				}
				require.Equal(t, l.schema, fh.Schema)
				require.Len(t, fh.PositiveSpans, len(l.pos))
				require.Len(t, fh.NegativeSpans, len(l.neg))
				for i, sp := range l.pos {
					require.Equal(t, histogram.Span{Offset: int32(sp.offset), Length: uint32(sp.length)}, fh.PositiveSpans[i])
				}
				var positive int
				for _, sp := range l.pos {
					positive += int(uint32(sp.length))
				}
				require.Len(t, fh.PositiveBuckets, positive)
				require.Equal(t, l.custom, fh.CustomValues)
			})
		}
	}
}

func TestHistogramLayoutRefusesWhatItsDataCantHold(t *testing.T) {
	max32 := uint64(math.MaxUint32)
	bombs := map[string]struct {
		spans []testSpan
		// written is how many buckets the first sample holds
		written int
	}{
		"2^22 buckets":   {spans: []testSpan{{length: 1 << 22}}},
		"2^25-1 buckets": {spans: []testSpan{{length: 1<<25 - 1}}},
		"2^32-1 buckets": {spans: []testSpan{{length: max32}}},
		// summed as 32-bit lengths, these would wrap to the one bucket the sample holds
		"spans summing past 2^32":  {spans: []testSpan{{length: max32}, {length: 2}}, written: 1},
		"spans together past data": {spans: []testSpan{{length: 600}, {length: 600}}, written: 600},
	}
	for name, bomb := range bombs {
		for _, enc := range histogramEncodings {
			for side, l := range map[string]testLayout{
				"positive": {pos: bomb.spans, buckets: bomb.written}, "negative": {neg: bomb.spans, buckets: bomb.written},
			} {
				t.Run(name+"/"+side+"/"+enc.String(), func(t *testing.T) {
					_, err := histogramLayout(enc, layoutChunk(enc, 1, l))
					require.ErrorIs(t, err, errMalformedChunk)
					require.ErrorContains(t, err, "more buckets than its data holds")
				})
			}
		}
	}

	for _, enc := range histogramEncodings {
		t.Run("boundary/"+enc.String(), func(t *testing.T) {
			sampleBits, bucketBits := histogramSampleBits, 1
			if enc == chunkenc.EncFloatHistogram {
				sampleBits, bucketBits = floatHistogramSampleBits, floatBucketBits
			}
			// as many buckets as the first sample's bits hold, with no bit to spare, then one more over the same data
			buckets := 1000
			layoutBits := writeLayout(enc, 1, testLayout{pos: []testSpan{{length: uint64(buckets)}}, buckets: -1}).n
			if bucketBits == 1 {
				buckets += (8 - (layoutBits+sampleBits+buckets)%8) % 8
			}
			size := (layoutBits + sampleBits + buckets*bucketBits + 7) / 8
			for _, declared := range []int{buckets, buckets + 1} {
				l := testLayout{pos: []testSpan{{length: uint64(declared)}}, buckets: -1}
				w := writeLayout(enc, 1, l)
				require.Equal(t, layoutBits, w.n)
				// zero bits read as a first sample at time zero, with every count, sum and bucket zero
				data := append(w.b, make([]byte, size-len(w.b))...)
				laid, err := histogramLayout(enc, data)
				if declared > buckets {
					require.ErrorIs(t, err, errMalformedChunk)
					continue
				}
				require.NoError(t, err)
				require.Equal(t, layoutCost(enc, l), laid)
				chk, err := chunkenc.FromData(enc, data)
				require.NoError(t, err)
				it := chk.Iterator(nil)
				require.NotEqual(t, chunkenc.ValNone, it.Next())
				require.NoError(t, it.Err())
			}
		})
	}

	malformed := map[string]struct {
		enc  chunkenc.Encoding
		data []byte
		want error
	}{
		"header truncated":  {enc: chunkenc.EncFloatHistogram, data: []byte{0, 1}},
		"threshold missing": {enc: chunkenc.EncHistogram, data: []byte{0, 1, 0}},
		"wide threshold truncated": {
			enc: chunkenc.EncFloatHistogram, data: []byte{0, 1, 0, zeroThresholdFloat, 1, 2},
		},
		"schema truncated": {enc: chunkenc.EncHistogram, data: []byte{0, 1, 0, 0}},
		"unknown schema": {
			enc: chunkenc.EncHistogram, data: layoutChunk(chunkenc.EncHistogram, 1, testLayout{schema: 60}),
			want: histogram.ErrHistogramsUnknownSchema,
		},
		// a schema of zero, then seven of the eight leading ones of a span count
		"span count past the data": {enc: chunkenc.EncHistogram, data: []byte{0, 1, 0, 0, 0x7f}},
		"span past the data": {
			enc:  chunkenc.EncHistogram,
			data: layoutChunk(chunkenc.EncHistogram, 1, testLayout{pos: []testSpan{{length: 1}}, buckets: -1})[:5],
		},
		"spans counted past the data": {enc: chunkenc.EncFloatHistogram, data: func() []byte {
			w := &testBits{b: []byte{0, 1, 0}, n: 24}
			w.write(0, 8)
			w.varbitInt(0)
			w.varbitUint(math.MaxUint64)
			return w.b
		}()},
		"custom bounds counted past the data": {enc: chunkenc.EncHistogram, data: func() []byte {
			w := &testBits{b: []byte{0, 1, 0}, n: 24}
			w.write(0, 8)
			w.varbitInt(int64(histogram.CustomBucketsSchema))
			w.varbitUint(0)
			w.varbitUint(0)
			w.varbitUint(1 << 40)
			return w.b
		}()},
		"custom bound truncated": {enc: chunkenc.EncHistogram, data: layoutChunk(chunkenc.EncHistogram, 1, testLayout{
			schema: histogram.CustomBucketsSchema, custom: []float64{-1}, buckets: -1,
		})},
		"no first sample": {
			enc: chunkenc.EncFloatHistogram, data: layoutChunk(chunkenc.EncFloatHistogram, 1, testLayout{buckets: -1}),
		},
	}
	for name, test := range malformed {
		t.Run(name, func(t *testing.T) {
			_, err := histogramLayout(test.enc, test.data)
			require.ErrorIs(t, err, errMalformedChunk)
			if test.want != nil {
				require.ErrorIs(t, err, test.want)
			}
		})
	}
}

func TestDecoderRefusesHistogramLayoutsBeforeReading(t *testing.T) {
	bombs := map[string]testLayout{
		"2^22 buckets":            {pos: []testSpan{{length: 1 << 22}}},
		"2^32-1 negative buckets": {neg: []testSpan{{length: math.MaxUint32}}},
		"spans summing past 2^32": {pos: []testSpan{{length: math.MaxUint32}, {length: math.MaxUint32}}},
	}
	spans := map[string]span{
		"inside": {mint: 120, maxt: 130}, "across": {mint: 150, maxt: 300}, "unknown": {mint: 300, maxt: 50},
		"outside": {mint: 300, maxt: 400},
	}
	remote := func(enc chunkenc.Encoding, data []byte, sp span) func(*decoder) error {
		frame := chunkedFrame(t, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
			{Type: promChunkTypes[enc], Data: data, MinTimeMs: sp.mint, MaxTimeMs: sp.maxt},
		}})
		return func(d *decoder) error { return d.chunkedReadResponse(frame) }
	}
	store := func(enc chunkenc.Encoding, data []byte, sp span) func(*decoder) error {
		series := appendBytesField(nil, seriesChunks, spanned(encodeTestChunk(storeChunkTypes[enc], data, true), sp.mint, sp.maxt))
		return func(d *decoder) error {
			d.kind = errStoreAPI
			return d.chunkedSeries(series, storeChunk)
		}
	}
	for name, l := range bombs {
		for _, enc := range histogramEncodings {
			data := layoutChunk(enc, 1, testLayout{pos: l.pos, neg: l.neg, buckets: -1})
			require.Less(t, len(data), 32, "a few bytes declare the buckets")
			for where, sp := range spans {
				for api, decode := range map[string]func(chunkenc.Encoding, []byte, span) func(*decoder) error{
					"remote read": remote, "storeapi": store,
				} {
					t.Run(name+"/"+enc.String()+"/"+where+"/"+api, func(t *testing.T) {
						read := decode(enc, data, sp)
						_, allocated, err := decodeAllocs(t, func(d *decoder) error {
							err := read(d)
							if len(d.out) > 0 {
								return errors.New("kept the chunk")
							}
							return err
						})
						require.Less(t, allocated, uint64(16<<10), "nothing is laid out for the buckets")
						if where == "outside" {
							require.NoError(t, err, "a chunk outside the range is never read")
							return
						}
						require.ErrorIs(t, err, errMalformedChunk)
						if api == "storeapi" {
							require.ErrorIs(t, err, errStoreAPI)
						} else {
							require.ErrorIs(t, err, errRemoteRead)
						}
					})
				}
			}
		}
	}
}

func TestDecoderChargesHistogramLayouts(t *testing.T) {
	h := &histogram.Histogram{
		Count: 20, Sum: 5, PositiveSpans: []histogram.Span{{Offset: 0, Length: 6}, {Offset: 2, Length: 4}},
		PositiveBuckets: []int64{1, 1, 0, 2, -1, 0, 1, 0, 0, 1},
	}
	chunkOf := func(enc chunkenc.Encoding, ts ...int64) prompb.Chunk {
		chk, err := chunkenc.NewEmptyChunk(enc)
		require.NoError(t, err)
		app, err := chk.Appender()
		require.NoError(t, err)
		for _, at := range ts {
			if enc == chunkenc.EncFloatHistogram {
				_, _, app, err = app.AppendFloatHistogram(nil, 0, at, h.ToFloat(nil), false)
			} else {
				_, _, app, err = app.AppendHistogram(nil, 0, at, h, false)
			}
			require.NoError(t, err)
		}
		return prompb.Chunk{Type: promChunkTypes[enc], Data: chk.Bytes(), MinTimeMs: ts[0], MaxTimeMs: ts[len(ts)-1]}
	}
	intLaid := 10*int64(histogramLayoutBytes) + 2*spanBytes
	floatLaid := 10*int64(floatHistogramLayoutBytes) + 2*spanBytes
	a1, a2 := chunkOf(chunkenc.EncHistogram, 110, 120), chunkOf(chunkenc.EncHistogram, 130)
	b := chunkOf(chunkenc.EncHistogram, 140)
	// read through, its samples at 190 and 210 crossing the range's end
	c := chunkOf(chunkenc.EncFloatHistogram, 190, 210)
	frame := chunkedFrame(t,
		&prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{a1, a2}},
		&prompb.ChunkedSeries{Labels: testLabels("b"), Chunks: []prompb.Chunk{b}},
		&prompb.ChunkedSeries{Labels: testLabels("c"), Chunks: []prompb.Chunk{c}},
	)
	labelsSize := int64(len("__name__")+len(testMetric)+len(testPod)+1) + 2*labelLengthBytes
	kept := 2*labelSlotBytes + 3*(chunkSeriesBytes+labelsSize+groupBytes) + 4*chunkBytes +
		int64(len(a1.Data)+len(a2.Data)+len(b.Data)+len(c.Data))
	// the series that lays out most is charged once, since each is read alone
	cost := kept + 2*intLaid
	require.Greater(t, 2*intLaid, floatLaid)
	for _, limit := range []int64{cost, cost - 1} {
		d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
		d.limit, d.allowance = limit, limit
		err := d.chunkedReadResponse(frame)
		if limit < cost {
			require.ErrorIs(t, err, ErrFetchLimit)
			continue
		}
		require.NoError(t, err)
		require.Zero(t, d.allowance)
		require.Equal(t, 2*intLaid, d.laid)
		require.Len(t, d.out, 3)
		require.Equal(t, []testPoint{{t: 190, h: true}}, readPoints(t, bound(d.out[2:], decodeTestMint, decodeTestMaxt)[0]))
	}

	// a chunk read through is charged what it lays out, though nothing in it is in range, and a kept one before
	// it is read
	layouts := map[string]struct {
		chunk prompb.Chunk
		cost  int64
		kept  int
	}{
		"read through": {chunk: chunkOf(chunkenc.EncFloatHistogram, 50, 250), cost: 2*labelSlotBytes + floatLaid},
		"kept": {chunk: a1, cost: 2*labelSlotBytes + chunkSeriesBytes + labelsSize + chunkBytes + int64(len(a1.Data)) +
			groupBytes + intLaid, kept: 1},
	}
	for name, test := range layouts {
		t.Run(name, func(t *testing.T) {
			frame := chunkedFrame(t, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{test.chunk}})
			for _, limit := range []int64{test.cost, test.cost - 1} {
				d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
				d.limit, d.allowance = limit, limit
				err := d.chunkedReadResponse(frame)
				if limit < test.cost {
					require.ErrorIs(t, err, ErrFetchLimit)
					require.Empty(t, d.out)
					continue
				}
				require.NoError(t, err)
				require.Zero(t, d.allowance)
				require.Len(t, d.out, test.kept)
			}
		})
	}

	// a chunk laying out as many buckets as its data holds lays out no more than it is charged when read
	full := testLayout{pos: []testSpan{{length: 4096}}, buckets: 4096}
	data := layoutChunk(chunkenc.EncHistogram, 1, full)
	frame = chunkedFrame(t, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
		{Type: prompb.Chunk_HISTOGRAM, Data: data, MinTimeMs: 150, MaxTimeMs: 150},
	}})
	d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
	require.NoError(t, d.chunkedReadResponse(frame))
	require.Equal(t, layoutCost(chunkenc.EncHistogram, full), d.laid)
	laidOut, _ := heapAfterRuns(func() any {
		it := d.out[0].Iterator(nil)
		require.Equal(t, chunkenc.ValHistogram, it.Next())
		return it
	})
	require.LessOrEqual(t, laidOut, d.laid+4096)
}
