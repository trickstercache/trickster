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
	"fmt"
	"math"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"google.golang.org/protobuf/encoding/protowire"
)

// Prometheus remote-read field numbers: ReadResponse, QueryResult, TimeSeries, Sample, Histogram, BucketSpan,
// ChunkedReadResponse and Chunk; ChunkedSeries and Label number theirs as StoreAPI's Series and Label do
const (
	readResponseResults     = 1
	queryResultTimeseries   = 1
	timeSeriesSamples       = 2
	timeSeriesHistograms    = 4
	sampleValue             = 1
	sampleTimestamp         = 2
	histogramCountInt       = 1
	histogramCountFloat     = 2
	histogramSum            = 3
	histogramSchema         = 4
	histogramZeroThreshold  = 5
	histogramZeroCountInt   = 6
	histogramZeroCountFloat = 7
	histogramNegativeSpans  = 8
	histogramNegativeDeltas = 9
	histogramNegativeCounts = 10
	histogramPositiveSpans  = 11
	histogramPositiveDeltas = 12
	histogramPositiveCounts = 13
	histogramResetHint      = 14
	histogramTimestamp      = 15
	histogramCustomValues   = 16
	bucketSpanOffset        = 1
	bucketSpanLength        = 2
	chunkedResponseSeries   = 1
	remoteChunkMinTime      = 1
	remoteChunkMaxTime      = 2
	remoteChunkType         = 3
	remoteChunkData         = 4
	// sampleValueTag and sampleTimestampTag start a Sample's value, a fixed64, and its time, a varint, which the value's
	// tag and eight bytes, sampleFixedBytes, precede as Prometheus writes them
	sampleValueTag     = sampleValue<<3 | byte(protowire.Fixed64Type)
	sampleTimestampTag = sampleTimestamp<<3 | byte(protowire.VarintType)
	sampleFixedBytes   = 9
	// floatKind and histogramKind are the kinds of sample in a series message, as an order error names them
	floatKind     = 0
	histogramKind = 1
)

// errSampleOrder is returned for a series message whose floats, or whose histograms, go back in time
var errSampleOrder = errors.New("samples out of time order")

// sampleKindNames name each kind of sample, as an error reports it
var sampleKindNames = [...]string{floatKind: "float", histogramKind: "histogram"}

// chunkedReadResponse reads one frame's ChunkedReadResponse, adding what it counts to the budget; its query index
// is ignored, as a read asks one query
func (d *decoder) chunkedReadResponse(b []byte) error {
	for len(b) > 0 {
		f, rest, err := d.field(b)
		if err != nil {
			return err
		}
		b = rest
		if f.num != chunkedResponseSeries {
			continue
		}
		if err := d.expect(f, protowire.BytesType); err != nil {
			return err
		}
		if err := d.chunkedSeries(f.b, remoteChunk); err != nil {
			return err
		}
	}
	return d.flush()
}

// remoteChunk reads a remote-read Chunk message
func remoteChunk(d *decoder, b []byte) (wireChunk, error) {
	var c wireChunk
	var typ uint64
	for len(b) > 0 {
		f, rest, err := d.field(b)
		if err != nil {
			return c, err
		}
		b = rest
		switch f.num {
		case remoteChunkMinTime, remoteChunkMaxTime, remoteChunkType:
			if err := d.expect(f, protowire.VarintType); err != nil {
				return c, err
			}
			switch f.num {
			case remoteChunkMinTime:
				c.sp.mint = int64(f.u) //nolint:gosec // wire protocol reinterpret
			case remoteChunkMaxTime:
				c.sp.maxt = int64(f.u) //nolint:gosec // wire protocol reinterpret
			default:
				typ = f.u
			}
		case remoteChunkData:
			if err := d.expect(f, protowire.BytesType); err != nil {
				return c, err
			}
			c.data = f.b
		}
	}
	switch typ {
	case uint64(prompb.Chunk_XOR):
		c.enc = chunkenc.EncXOR
	case uint64(prompb.Chunk_XOR2):
		c.enc = chunkenc.EncXOR2
	case uint64(prompb.Chunk_HISTOGRAM):
		c.enc = chunkenc.EncHistogram
	case uint64(prompb.Chunk_FLOAT_HISTOGRAM):
		c.enc = chunkenc.EncFloatHistogram
	default:
		return c, fmt.Errorf("%w: unsupported chunk encoding %d", d.kind, typ)
	}
	if err := checkChunk(c.enc, c.data); err != nil {
		return c, fmt.Errorf("%w: %w", d.kind, err)
	}
	return c, nil
}

// readResponse reads a ReadResponse of samples, adding what it counts to the budget
func (d *decoder) readResponse(b []byte) error {
	for len(b) > 0 {
		f, rest, err := d.field(b)
		if err != nil {
			return err
		}
		b = rest
		if f.num != readResponseResults {
			continue
		}
		if err := d.expect(f, protowire.BytesType); err != nil {
			return err
		}
		if err := d.queryResult(f.b); err != nil {
			return err
		}
	}
	return d.flush()
}

// queryResult reads a QueryResult's series of samples
func (d *decoder) queryResult(b []byte) error {
	for len(b) > 0 {
		f, rest, err := d.field(b)
		if err != nil {
			return err
		}
		b = rest
		if f.num != queryResultTimeseries {
			continue
		}
		if err := d.expect(f, protowire.BytesType); err != nil {
			return err
		}
		if err := d.timeSeries(f.b); err != nil {
			return err
		}
	}
	return nil
}

// sampleCursor walks one kind of sample in a TimeSeries message, floats or histograms, each kind in time order
type sampleCursor struct {
	rest []byte
	num  protowire.Number
	ok   bool
	t    int64
	f    float64
	// h is a histogram's message, whose shape a first pass found
	h     []byte
	shape histogramShape
}

// next moves c to its next sample, clearing ok when it has none
func (d *decoder) next(c *sampleCursor) error {
	c.ok = false
	for len(c.rest) > 0 {
		f, rest, err := d.field(c.rest)
		if err != nil {
			return err
		}
		c.rest = rest
		if f.num != c.num {
			continue
		}
		if err := d.expect(f, protowire.BytesType); err != nil {
			return err
		}
		if c.num == timeSeriesSamples {
			c.t, c.f, err = d.sample(f.b)
		} else {
			c.h = f.b
			c.shape, err = d.histogramShape(f.b)
			c.t = c.shape.t
		}
		c.ok = err == nil
		return err
	}
	return nil
}

// inRange reports whether t lies from mint to maxt
func (d *decoder) inRange(t int64) bool { return t >= d.mint && t <= d.maxt }

// timeSeries reads a TimeSeries message, keeping its samples from mint to maxt. A first pass reads its labels, checks
// every sample's order, and counts those in range, so a series without any is never kept, and the rest are counted first.
func (d *decoder) timeSeries(b []byte) error {
	d.resetLabels()
	var n, t int64
	var histograms bool
	// a sample earlier than the last of its kind fails the response, as merging and seeking a kind's samples would
	// silently skip those after it; each kind's last time is its own local, so it stays in a register
	lastFloat, lastHistogram := int64(math.MinInt64), int64(math.MinInt64)
	for rest := b; len(rest) > 0; {
		f, next, err := d.field(rest)
		if err != nil {
			return err
		}
		rest = next
		switch f.num {
		case seriesLabels:
			err = d.addLabel(f)
		case timeSeriesSamples:
			if t, err = d.sampleTime(f); err == nil {
				if t < lastFloat {
					return d.orderError(floatKind, t, lastFloat)
				}
				if lastFloat = t; d.inRange(t) {
					n++
				}
			}
		case timeSeriesHistograms:
			if t, err = d.sampleTime(f); err == nil {
				if t < lastHistogram {
					return d.orderError(histogramKind, t, lastHistogram)
				}
				if lastHistogram = t; d.inRange(t) {
					n++
					histograms = true
				}
			}
		}
		if err != nil {
			return err
		}
	}
	if n == 0 {
		return nil
	}
	d.builder.Sort()
	fresh, err := d.fresh()
	if err != nil {
		return err
	}
	var series int64
	if fresh {
		series = 1
	}
	if err = d.count(series, n); err != nil {
		return err
	}
	if err = d.charge(sampleSeriesBytes + d.labelBytes + n*sampleBytes); err != nil {
		return err
	}
	samples := make([]sample, 0, n)
	if histograms {
		if samples, err = d.mergeSamples(b, samples); err != nil {
			return err
		}
	} else {
		samples = d.floatSamples(b, samples)
	}
	d.out = append(d.out, &sampleSeries{lset: d.builder.Labels(), samples: samples})
	return nil
}

// sampleTime returns the time of a float or histogram sample field
func (d *decoder) sampleTime(f wireField) (int64, error) {
	if err := d.expect(f, protowire.BytesType); err != nil {
		return 0, err
	}
	if f.num == timeSeriesSamples {
		t, _, err := d.sample(f.b)
		return t, err
	}
	s, err := d.histogramShape(f.b)
	return s.t, err
}

// orderError is the error a sample of kind at t fails a response with when one of its kind before it is at last
func (d *decoder) orderError(kind int, t, last int64) error {
	return fmt.Errorf("%w: %w: a %s sample at %d follows one at %d", d.kind, errSampleOrder, sampleKindNames[kind], t, last)
}

// floatSamples appends to dst the float samples from mint to maxt of a series message the first pass read, in time
// order; of samples sharing a time, only the first is kept
func (d *decoder) floatSamples(b []byte, dst []sample) []sample {
	kept := int64(math.MinInt64)
	for len(b) > 0 {
		f, rest, _ := d.field(b)
		b = rest
		if f.num != timeSeriesSamples {
			continue
		}
		if t, v, _ := d.sample(f.b); d.inRange(t) && (t > kept || len(dst) == 0) {
			dst, kept = append(dst, sample{t: t, f: v}), t
		}
	}
	return dst
}

// mergeSamples appends to dst the float and histogram samples from mint to maxt the first pass read, merging the kinds in time
// order. Each time keeps one sample, its first float, else its first histogram; Prometheus's remote-read client also prefers a float.
func (d *decoder) mergeSamples(b []byte, dst []sample) ([]sample, error) {
	floats, hists := sampleCursor{rest: b, num: timeSeriesSamples}, sampleCursor{rest: b, num: timeSeriesHistograms}
	// the first pass read every sample, so neither cursor fails
	_, _ = d.next(&floats), d.next(&hists)
	kept := int64(math.MinInt64)
	for floats.ok || hists.ok {
		// a float is taken first where both kinds share a time, so a histogram there is dropped without being decoded
		if floats.ok && (!hists.ok || floats.t <= hists.t) {
			if d.inRange(floats.t) && (floats.t > kept || len(dst) == 0) {
				dst, kept = append(dst, sample{t: floats.t, f: floats.f}), floats.t
			}
			_ = d.next(&floats)
			continue
		}
		if d.inRange(hists.t) && (hists.t > kept || len(dst) == 0) {
			fh, err := d.histogram(hists.h, hists.shape)
			if err != nil {
				return nil, err
			}
			dst, kept = append(dst, sample{t: hists.t, fh: fh}), hists.t
		}
		_ = d.next(&hists)
	}
	return dst, nil
}

// sample reads a Sample message's time and value
func (d *decoder) sample(b []byte) (t int64, v float64, err error) {
	// a sample written as Prometheus writes one, its value and then its time, is read directly
	if len(b) > sampleFixedBytes && b[0] == sampleValueTag && b[sampleFixedBytes] == sampleTimestampTag {
		if u, n := protowire.ConsumeVarint(b[sampleFixedBytes+1:]); n == len(b)-sampleFixedBytes-1 {
			return int64(u), math.Float64frombits(binary.LittleEndian.Uint64(b[1:sampleFixedBytes])), nil //nolint:gosec // wire protocol reinterpret
		}
	}
	for len(b) > 0 {
		var f wireField
		if f, b, err = d.field(b); err != nil {
			return 0, 0, err
		}
		switch f.num {
		case sampleValue:
			if err = d.expect(f, protowire.Fixed64Type); err != nil {
				return 0, 0, err
			}
			v = math.Float64frombits(f.u)
		case sampleTimestamp:
			if err = d.expect(f, protowire.VarintType); err != nil {
				return 0, 0, err
			}
			t = int64(f.u) //nolint:gosec // wire protocol reinterpret
		}
	}
	return t, v, nil
}

// histogramShape is what a first pass over a Histogram message finds: its time and schema, whether its counts are
// floats, as the last of its count fields says, and how many spans, buckets and custom values it holds
type histogramShape struct {
	t                                          int64
	schema                                     int32
	float                                      bool
	negSpans, posSpans                         int
	negDeltas, posDeltas, negCounts, posCounts int
	custom                                     int
}

// histogramShape reads a Histogram message through, checking its fields' wire types, for its shape
func (d *decoder) histogramShape(b []byte) (histogramShape, error) {
	var s histogramShape
	for len(b) > 0 {
		f, rest, err := d.field(b)
		if err != nil {
			return s, err
		}
		b = rest
		var n int
		switch f.num {
		case histogramCountInt, histogramZeroCountInt, histogramResetHint, histogramSchema, histogramTimestamp:
			err = d.expect(f, protowire.VarintType)
		case histogramCountFloat, histogramSum, histogramZeroThreshold, histogramZeroCountFloat:
			err = d.expect(f, protowire.Fixed64Type)
		case histogramNegativeSpans, histogramPositiveSpans:
			err = d.expect(f, protowire.BytesType)
		case histogramNegativeDeltas, histogramPositiveDeltas:
			n, err = d.repeated(f, protowire.VarintType)
		case histogramNegativeCounts, histogramPositiveCounts, histogramCustomValues:
			n, err = d.repeated(f, protowire.Fixed64Type)
		}
		if err != nil {
			return s, err
		}
		switch f.num {
		case histogramCountInt, histogramCountFloat:
			s.float = f.num == histogramCountFloat
		case histogramSchema:
			s.schema = zigzag32(f.u)
		case histogramTimestamp:
			s.t = int64(f.u) //nolint:gosec // wire protocol reinterpret
		case histogramNegativeSpans:
			s.negSpans++
		case histogramPositiveSpans:
			s.posSpans++
		case histogramNegativeDeltas:
			s.negDeltas += n
		case histogramPositiveDeltas:
			s.posDeltas += n
		case histogramNegativeCounts:
			s.negCounts += n
		case histogramPositiveCounts:
			s.posCounts += n
		case histogramCustomValues:
			s.custom += n
		}
	}
	return s, nil
}

// histogram reads a Histogram message of shape s as the float histogram the engine reads, its memory charged first. A schema
// finer than the engine reads is reduced, as a chunk's iterator reduces it; an unknown one, or a malformed bucket layout, fail.
func (d *decoder) histogram(b []byte, s histogramShape) (*histogram.FloatHistogram, error) {
	if !histogram.IsKnownSchema(s.schema) {
		return nil, fmt.Errorf("%w: %w", d.kind, histogram.UnknownSchemaError(s.schema))
	}
	negBuckets, posBuckets := s.negDeltas, s.posDeltas
	if s.float {
		negBuckets, posBuckets = s.negCounts, s.posCounts
	}
	spans, values := s.negSpans+s.posSpans, negBuckets+posBuckets+s.custom
	if err := d.charge(floatHistogramBytes + int64(spans)*spanBytes + int64(values)*bucketBytes); err != nil {
		return nil, err
	}
	// one array holds both sides' spans, and one every bucket and custom value
	spanBuf, valueBuf := make([]histogram.Span, spans), make([]float64, values)
	fh := &histogram.FloatHistogram{
		Schema:        s.schema,
		NegativeSpans: spanBuf[:0:s.negSpans],
		PositiveSpans: spanBuf[s.negSpans:s.negSpans:spans],
	}
	// as Prometheus converts them, a float histogram's absent buckets are nil, and so are absent custom values,
	// which an exponential histogram must not have
	if !s.float || negBuckets > 0 {
		fh.NegativeBuckets = valueBuf[:0:negBuckets]
	}
	if !s.float || posBuckets > 0 {
		fh.PositiveBuckets = valueBuf[negBuckets : negBuckets : negBuckets+posBuckets]
	}
	if s.custom > 0 {
		fh.CustomValues = valueBuf[negBuckets+posBuckets : negBuckets+posBuckets : values]
	}
	var count, zeroCount fieldValue
	var negCount, posCount float64
	for len(b) > 0 {
		// the shape's pass read every field
		f, rest, _ := d.field(b)
		b = rest
		switch f.num {
		case histogramCountInt, histogramCountFloat:
			count = fieldValue{num: f.num, u: f.u}
		case histogramZeroCountInt, histogramZeroCountFloat:
			zeroCount = fieldValue{num: f.num, u: f.u}
		case histogramSum:
			fh.Sum = math.Float64frombits(f.u)
		case histogramZeroThreshold:
			fh.ZeroThreshold = math.Float64frombits(f.u)
		case histogramResetHint:
			fh.CounterResetHint = histogram.CounterResetHint(f.u) //nolint:gosec // wire protocol reinterpret
		case histogramNegativeSpans, histogramPositiveSpans:
			sp, err := d.bucketSpan(f.b)
			if err != nil {
				return nil, err
			}
			if f.num == histogramNegativeSpans {
				fh.NegativeSpans = append(fh.NegativeSpans, sp)
			} else {
				fh.PositiveSpans = append(fh.PositiveSpans, sp)
			}
		case histogramNegativeDeltas:
			if !s.float {
				fh.NegativeBuckets, negCount = appendDeltas(fh.NegativeBuckets, f, negCount)
			}
		case histogramPositiveDeltas:
			if !s.float {
				fh.PositiveBuckets, posCount = appendDeltas(fh.PositiveBuckets, f, posCount)
			}
		case histogramNegativeCounts:
			if s.float {
				fh.NegativeBuckets = appendDoubles(fh.NegativeBuckets, f)
			}
		case histogramPositiveCounts:
			if s.float {
				fh.PositiveBuckets = appendDoubles(fh.PositiveBuckets, f)
			}
		case histogramCustomValues:
			fh.CustomValues = appendDoubles(fh.CustomValues, f)
		}
	}
	// a count is read as the kind of histogram says, and is zero when its own field is of the other kind
	fh.Count = count.of(s.float, histogramCountFloat)
	fh.ZeroCount = zeroCount.of(s.float, histogramZeroCountFloat)
	if err := checkBucketLayout(fh); err != nil {
		return nil, fmt.Errorf("%w: %w", d.kind, err)
	}
	if fh.Schema > histogram.ExponentialSchemaMax {
		if err := fh.ReduceResolution(histogram.ExponentialSchemaMax); err != nil {
			return nil, fmt.Errorf("%w: %w", d.kind, err)
		}
	}
	return fh, nil
}

// fieldValue is the last field of a protobuf oneof of an unsigned integer or a double
type fieldValue struct {
	num protowire.Number
	u   uint64
}

// of returns the value as a float histogram's count when float is set, or an integer histogram's, which is zero when
// the field was the other kind; floatNum numbers the double field
func (v fieldValue) of(float bool, floatNum protowire.Number) float64 {
	switch {
	case v.num == 0 || (v.num == floatNum) != float:
		return 0
	case float:
		return math.Float64frombits(v.u)
	}
	return float64(v.u)
}

// bucketSpan reads a BucketSpan message
func (d *decoder) bucketSpan(b []byte) (histogram.Span, error) {
	var sp histogram.Span
	for len(b) > 0 {
		f, rest, err := d.field(b)
		if err != nil {
			return sp, err
		}
		b = rest
		if f.num != bucketSpanOffset && f.num != bucketSpanLength {
			continue
		}
		if err := d.expect(f, protowire.VarintType); err != nil {
			return sp, err
		}
		if f.num == bucketSpanOffset {
			sp.Offset = zigzag32(f.u)
		} else {
			sp.Length = uint32(f.u) //nolint:gosec // a uint32 field keeps its low 32 bits
		}
	}
	return sp, nil
}

// appendDeltas appends to dst the bucket counts a repeated sint64 field of deltas holds, each added to the last
func appendDeltas(dst []float64, f wireField, last float64) ([]float64, float64) {
	if f.typ == protowire.VarintType {
		last += float64(protowire.DecodeZigZag(f.u))
		return append(dst, last), last
	}
	for b := f.b; len(b) > 0; {
		v, n := protowire.ConsumeVarint(b)
		if n <= 0 {
			break
		}
		last += float64(protowire.DecodeZigZag(v))
		dst, b = append(dst, last), b[n:]
	}
	return dst, last
}

// appendDoubles appends to dst the values a repeated double field holds
func appendDoubles(dst []float64, f wireField) []float64 {
	if f.typ == protowire.Fixed64Type {
		return append(dst, math.Float64frombits(f.u))
	}
	for b := f.b; len(b) >= 8; b = b[8:] {
		v, _ := protowire.ConsumeFixed64(b)
		dst = append(dst, math.Float64frombits(v))
	}
	return dst
}

// zigzag32 decodes a sint32 field, whose varint's low 32 bits hold its zigzag encoding
func zigzag32(v uint64) int32 {
	return int32(protowire.DecodeZigZag(v & math.MaxUint32)) //nolint:gosec // a sint32 field's value fits in its low 32 bits
}
