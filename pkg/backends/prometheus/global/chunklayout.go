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
	"fmt"
	"math"
	"math/bits"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
)

const (
	// histogramHeaderBytes is the header of a histogram chunk of either kind, before the bucket layout its samples share
	histogramHeaderBytes = 3
	// zeroThresholdFloat is the byte after which a zero threshold is written as a whole float64
	zeroThresholdFloat = 255
	// histogramSampleBits and floatHistogramSampleBits are the fewest bits a chunk's first sample takes besides its
	// buckets: a varbit time, then two varbit counts and a 64-bit sum, or three 64-bit values
	histogramSampleBits      = 3 + 64
	floatHistogramSampleBits = 1 + 3*64
	// floatBucketBits is what each bucket of a float histogram chunk's first sample takes, where an integer one's
	// takes at least a bit
	floatBucketBits = 64
	// histogramLayoutBytes and floatHistogramLayoutBytes are what a chunk's iterator lays out for each bucket:
	// three 8-byte arrays, or one and two of bytes
	histogramLayoutBytes      = 3 * bucketBytes
	floatHistogramLayoutBytes = bucketBytes + 2
	// maxSpanWidth is where the reach of a side's spans saturates, past the bounds any histogram can have
	maxSpanWidth = math.MaxInt64 / 2
)

// varbitSizes are how many bits follow each count of a varbit's leading ones, as the library writes them
var varbitSizes = [...]uint8{0, 3, 6, 9, 12, 18, 25, 56, 64}

// bitReader reads bits from the most significant of each byte, failing any read past the end
type bitReader struct {
	// b is the data not yet loaded into buf, whose most significant bits are the next valid ones, the rest zero
	b     []byte
	buf   uint64
	valid uint8
}

func (r *bitReader) remaining() int { return int(r.valid) + 8*len(r.b) }

// fill loads as many whole bytes into buf as it has room for, or as remain
func (r *bitReader) fill() {
	if len(r.b) >= 8 {
		n := (64 - r.valid) / 8
		loaded := 8 * n
		r.buf |= binary.BigEndian.Uint64(r.b) >> (64 - loaded) << (64 - loaded) >> r.valid
		r.b, r.valid = r.b[n:], r.valid+loaded
		return
	}
	for r.valid <= 56 && len(r.b) > 0 {
		r.buf |= uint64(r.b[0]) << (56 - r.valid)
		r.b, r.valid = r.b[1:], r.valid+8
	}
}

// read returns the next n bits, n at most 64
func (r *bitReader) read(n uint8) (uint64, bool) {
	if n > 56 {
		hi, ok := r.read(n - 32)
		lo, loOK := r.read(32)
		return hi<<32 | lo, ok && loOK
	}
	if r.valid < n {
		if r.fill(); r.valid < n {
			return 0, false
		}
	}
	v := r.buf >> (64 - n)
	r.buf <<= n
	r.valid -= n
	return v, true
}

// varbit reads a varbit's bits and how many there are, which up to eight leading ones choose; a zero ends them,
// unless there are eight
func (r *bitReader) varbit() (uint64, uint8, bool) {
	if r.valid < 8 {
		r.fill()
	}
	// the bits past those loaded are zero, so a run of ones ends within them
	ones := uint8(min(bits.LeadingZeros64(^r.buf), len(varbitSizes)-1)) //nolint:gosec // at most eight
	prefix := min(ones+1, uint8(len(varbitSizes)-1))
	if prefix > r.valid {
		return 0, 0, false
	}
	r.buf <<= prefix
	r.valid -= prefix
	size := varbitSizes[ones]
	if size == 0 {
		return 0, 0, true
	}
	v, ok := r.read(size)
	return v, size, ok
}

func (r *bitReader) varbitUint() (uint64, bool) {
	v, _, ok := r.varbit()
	return v, ok
}

// varbitInt reads a signed varbit, which any but the widest form writes biased up by one
func (r *bitReader) varbitInt() (int64, bool) {
	v, size, ok := r.varbit()
	if size > 0 && size < 64 && v > 1<<(size-1) {
		return int64(v) - 1<<size, ok //nolint:gosec // v holds fewer than 64 bits
	}
	return int64(v), ok //nolint:gosec // wire protocol reinterpret
}

// histogramLayout returns the memory an iterator lays out for the bucket layout a histogram chunk's samples share, none for other
// encodings, refusing one of an unknown schema, of spans a spanWalk refuses or past custom bounds, or of more buckets than its data holds
func histogramLayout(enc chunkenc.Encoding, data []byte) (int64, error) {
	var sampleBits, bucketBits int
	var bucketLayout int64
	switch enc {
	case chunkenc.EncHistogram:
		sampleBits, bucketBits, bucketLayout = histogramSampleBits, 1, histogramLayoutBytes
	case chunkenc.EncFloatHistogram:
		sampleBits, bucketBits, bucketLayout = floatHistogramSampleBits, floatBucketBits, floatHistogramLayoutBytes
	default:
		return 0, nil
	}
	if len(data) < histogramHeaderBytes {
		return 0, layoutError(enc)
	}
	// a chunk without samples has no layout, and its iterator reads none
	if headerSamples(data) == 0 {
		return 0, nil
	}
	r := bitReader{b: data[histogramHeaderBytes:]}
	threshold, ok := r.read(8)
	if ok && threshold == zeroThresholdFloat {
		_, ok = r.read(64)
	}
	v, schemaOK := r.varbitInt()
	if !ok || !schemaOK {
		return 0, layoutError(enc)
	}
	schema := int32(v) //nolint:gosec // the library keeps a schema's low 32 bits
	if !histogram.IsKnownSchema(schema) {
		return 0, fmt.Errorf("%w: %w", errMalformedChunk, histogram.UnknownSchemaError(schema))
	}
	isCustom := histogram.IsCustomBucketsSchema(schema)
	var spans, buckets uint64
	var width int64
	// positive spans, then negative, each a count and then each span's length and offset
	for side := range 2 {
		count, ok := r.varbitUint()
		if !ok {
			return 0, layoutError(enc)
		}
		if isCustom && side > 0 && count > 0 {
			return 0, fmt.Errorf("%w: %w", errMalformedChunk, histogram.ErrHistogramCustomBucketsNegSpans)
		}
		walk := newSpanWalk(schema)
		for ; count > 0; count-- {
			length, lengthOK := r.varbitUint()
			offset, offsetOK := r.varbitInt()
			if !lengthOK || !offsetOK {
				return 0, layoutError(enc)
			}
			// a span keeps the low 32 bits of each, as the chunk's iterator reads it
			if err := walk.add(uint32(length), int32(offset)); err != nil { //nolint:gosec // as the iterator reads them
				return 0, fmt.Errorf("%w: %w", errMalformedChunk, err)
			}
			// refused as soon as the buckets outnumber what the data could hold, so their sum can't overflow
			if buckets += uint64(uint32(length)); buckets > uint64(r.remaining()/bucketBits) { //nolint:gosec // a span's length keeps its low 32 bits
				return 0, bucketsError(enc)
			}
			spans++
		}
		if side == 0 {
			width = walk.width
		}
	}
	var custom uint64
	if isCustom {
		count, ok := r.varbitUint()
		for ; ok && count > 0; count-- {
			// a bound of zero is followed by a whole float64
			var bound uint64
			if bound, ok = r.varbitUint(); ok && bound == 0 {
				_, ok = r.read(64)
			}
			custom++
		}
		if !ok {
			return 0, layoutError(enc)
		}
		if err := checkCustomWidth(width, custom); err != nil {
			return 0, fmt.Errorf("%w: %w", errMalformedChunk, err)
		}
	}
	if r.remaining() < sampleBits || buckets > uint64((r.remaining()-sampleBits)/bucketBits) { //nolint:gosec // positive
		return 0, bucketsError(enc)
	}
	return int64(buckets)*bucketLayout + int64(spans)*spanBytes + int64(custom)*bucketBytes, nil //nolint:gosec // bounded by the data's bits
}

// spanWalk checks one side's spans in turn, summing their buckets and reach: a custom-bucket schema's are never offset back,
// as Prometheus ingests none so, nor a finer schema's than the engine reads after its first, which reduction fails on
type spanWalk struct {
	custom, fine bool
	spans        int
	buckets      uint64
	width        int64
}

// newSpanWalk returns the walk of a side's spans for a histogram of schema
func newSpanWalk(schema int32) spanWalk {
	return spanWalk{custom: histogram.IsCustomBucketsSchema(schema), fine: schema > histogram.ExponentialSchemaMax}
}

func (w *spanWalk) add(length uint32, offset int32) error {
	if offset < 0 && (w.custom || (w.fine && w.spans > 0)) {
		return fmt.Errorf("%w: span %d offset by %d", histogram.ErrHistogramSpanNegativeOffset, w.spans+1, offset)
	}
	w.spans++
	w.buckets += uint64(length)
	// the reach saturates far past any histogram's bounds, so it never overflows
	w.width = min(w.width+int64(offset)+int64(length), maxSpanWidth)
	return nil
}

// checkCustomWidth refuses custom-bucket spans reaching width buckets past the bounds' buckets, one more than the bounds
func checkCustomWidth(width int64, bounds uint64) error {
	if uint64(width) > bounds+1 { //nolint:gosec // a custom-bucket schema's reach is never negative
		return fmt.Errorf("%w: spans reach %d buckets of %d bounds", histogram.ErrHistogramCustomBucketsMismatch, width, bounds)
	}
	return nil
}

// checkBucketLayout refuses a histogram read as a sample whose bucket layout a chunk's would be refused for, or whose spans
// lay out other than its buckets, as reading it would index past them; its counts aren't checked
func checkBucketLayout(fh *histogram.FloatHistogram) error {
	custom := histogram.IsCustomBucketsSchema(fh.Schema)
	if custom && (len(fh.NegativeSpans) > 0 || len(fh.NegativeBuckets) > 0) {
		return histogram.ErrHistogramCustomBucketsNegSpans
	}
	if _, err := walkSpans(fh.NegativeSpans, len(fh.NegativeBuckets), fh.Schema); err != nil {
		return err
	}
	width, err := walkSpans(fh.PositiveSpans, len(fh.PositiveBuckets), fh.Schema)
	if err != nil || !custom {
		return err
	}
	return checkCustomWidth(width, uint64(len(fh.CustomValues)))
}

// walkSpans checks the spans of a side of a histogram of schema, which must lay out exactly buckets, returning their reach
func walkSpans(spans []histogram.Span, buckets int, schema int32) (int64, error) {
	w := newSpanWalk(schema)
	for _, sp := range spans {
		if err := w.add(sp.Length, sp.Offset); err != nil {
			return 0, err
		}
	}
	if w.buckets != uint64(buckets) { //nolint:gosec // a length is never negative
		return 0, fmt.Errorf("%w: spans lay out %d buckets, have %d", histogram.ErrHistogramSpansBucketsMismatch,
			w.buckets, buckets)
	}
	return w.width, nil
}

func layoutError(enc chunkenc.Encoding) error {
	return fmt.Errorf("%w: %s bucket layout past its data", errMalformedChunk, enc)
}

func bucketsError(enc chunkenc.Encoding) error {
	return fmt.Errorf("%w: %s bucket layout of more buckets than its data holds", errMalformedChunk, enc)
}
