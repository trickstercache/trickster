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
	"sort"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
)

// errMalformedChunk is returned for chunk data that can't hold its encoding's header
var errMalformedChunk = errors.New("malformed chunk")

// chunkHeaderSize returns the bytes a chunk's header takes in each encoding a member may send
func chunkHeaderSize(enc chunkenc.Encoding) (int, bool) {
	switch enc {
	case chunkenc.EncXOR:
		return 2, true
	case chunkenc.EncXOR2, chunkenc.EncHistogram, chunkenc.EncFloatHistogram:
		return 3, true
	}
	return 0, false
}

// newChunk wraps data as a chunk of enc, refusing data shorter than its header, which the library
// reads for the sample count and iterators without checking the length
func newChunk(enc chunkenc.Encoding, data []byte) (chunkenc.Chunk, error) {
	if err := checkChunk(enc, data); err != nil {
		return nil, err
	}
	return chunkenc.FromData(enc, data)
}

// checkChunk refuses data of an encoding no member sends, or shorter than its encoding's header
func checkChunk(enc chunkenc.Encoding, data []byte) error {
	size, ok := chunkHeaderSize(enc)
	if !ok {
		return fmt.Errorf("%w: unsupported encoding %s", errMalformedChunk, enc)
	}
	if len(data) < size {
		return fmt.Errorf("%w: %d bytes of %s data, under its %d-byte header", errMalformedChunk, len(data), enc, size)
	}
	return nil
}

// headerSamples returns the sample count that starts the header of data checkChunk accepted, as each
// encoding's NumSamples reads it
func headerSamples(data []byte) int64 { return int64(binary.BigEndian.Uint16(data)) }

// sample is a float or float-histogram sample of a series read as samples; histograms are held as
// float histograms, which the engine reads whatever their original kind
type sample struct {
	t  int64
	f  float64
	fh *histogram.FloatHistogram
}

func (s sample) valueType() chunkenc.ValueType {
	if s.fh != nil {
		return chunkenc.ValFloatHistogram
	}
	return chunkenc.ValFloat
}

// sampleSeries is a series read as samples, one at each time, in time order, which seeking relies on
type sampleSeries struct {
	lset    labels.Labels
	samples []sample
}

func (s *sampleSeries) Labels() labels.Labels { return s.lset }

func (s *sampleSeries) Iterator(it chunkenc.Iterator) chunkenc.Iterator {
	if si, ok := it.(*sampleIterator); ok {
		si.samples, si.idx = s.samples, -1
		return si
	}
	return &sampleIterator{samples: s.samples, idx: -1}
}

// sampleIterator copies each histogram into the caller's buffer, as a chunk's iterator does: the engine
// reuses histograms it was handed as buffers, so none may be the series' own
type sampleIterator struct {
	samples []sample
	idx     int
}

func (it *sampleIterator) Next() chunkenc.ValueType {
	if it.idx+1 >= len(it.samples) {
		it.idx = len(it.samples)
		return chunkenc.ValNone
	}
	it.idx++
	return it.samples[it.idx].valueType()
}

func (it *sampleIterator) Seek(t int64) chunkenc.ValueType {
	it.idx = max(it.idx, 0)
	if it.idx >= len(it.samples) {
		return chunkenc.ValNone
	}
	if it.samples[it.idx].t < t {
		rest := it.samples[it.idx:]
		it.idx += sort.Search(len(rest), func(i int) bool { return rest[i].t >= t })
		if it.idx >= len(it.samples) {
			return chunkenc.ValNone
		}
	}
	return it.samples[it.idx].valueType()
}

func (it *sampleIterator) At() (int64, float64) {
	s := it.samples[it.idx]
	return s.t, s.f
}

// AtHistogram returns no histogram, as samples hold float histograms, which AtFloatHistogram reads
func (it *sampleIterator) AtHistogram(*histogram.Histogram) (int64, *histogram.Histogram) {
	return it.samples[it.idx].t, nil
}

func (it *sampleIterator) AtFloatHistogram(fh *histogram.FloatHistogram) (int64, *histogram.FloatHistogram) {
	s := it.samples[it.idx]
	switch {
	case s.fh == nil:
		return s.t, nil
	case fh == nil:
		return s.t, s.fh.Copy()
	}
	s.fh.CopyTo(fh)
	return s.t, fh
}

func (it *sampleIterator) AtT() int64 { return it.samples[it.idx].t }
func (*sampleIterator) AtST() int64   { return 0 }
func (*sampleIterator) Err() error    { return nil }

// span is the time range of a chunk's samples, both ends inclusive; a span whose mint is after its maxt
// is unknown
type span struct {
	mint, maxt int64
}

func (s span) known() bool { return s.mint <= s.maxt }

// outside reports whether the span is known and lies wholly before mint or after maxt
func (s span) outside(mint, maxt int64) bool { return s.known() && (s.maxt < mint || s.mint > maxt) }

// inside reports whether the span is known and lies wholly from mint to maxt
func (s span) inside(mint, maxt int64) bool { return s.known() && s.mint >= mint && s.maxt <= maxt }

// chunkSeries is a series read as encoded chunks; chunks are in start-time order and may overlap
type chunkSeries struct {
	lset   labels.Labels
	chunks []chunkenc.Iterable
	// spans are the chunks' time ranges, as the member reported them
	spans []span
}

func (s *chunkSeries) Labels() labels.Labels { return s.lset }

func (s *chunkSeries) Iterator(it chunkenc.Iterator) chunkenc.Iterator {
	if len(s.chunks) == 1 {
		return s.chunks[0].Iterator(it)
	}
	return storage.ChainSampleIteratorFromIterables(it, s.chunks)
}

// disjoint reports whether every chunk has a known span and starts after the one before it ends, so
// the chunks can be read one after another
func (s *chunkSeries) disjoint() bool {
	if len(s.spans) != len(s.chunks) {
		return false
	}
	for i, sp := range s.spans {
		if _, ok := s.chunks[i].(chunkenc.Chunk); !ok || !sp.known() || (i > 0 && sp.mint <= s.spans[i-1].maxt) {
			return false
		}
	}
	return true
}

// samplesWithin returns how many of chk's samples lie from mint to maxt, reading it only when its span
// crosses either end or is unknown
func samplesWithin(chk chunkenc.Chunk, sp span, mint, maxt int64) (int64, error) {
	switch {
	case sp.outside(mint, maxt):
		return 0, nil
	case sp.inside(mint, maxt):
		return int64(chk.NumSamples()), nil
	}
	n, _, err := readWithin(chk, mint, maxt, nil)
	return n, err
}

// readWithin reads chk through it, returning how many of its samples lie from mint to maxt and the iterator,
// for the next read to reuse
func readWithin(chk chunkenc.Chunk, mint, maxt int64, it chunkenc.Iterator) (int64, chunkenc.Iterator, error) {
	var n int64
	it = chk.Iterator(it)
	for vt := it.Seek(mint); vt != chunkenc.ValNone && it.AtT() <= maxt; vt = it.Next() {
		n++
	}
	return n, it, it.Err()
}

// boundedSeries passes on only its series' samples from mint to maxt, as the engine asked for them
type boundedSeries struct {
	storage.Series
	mint, maxt int64
}

func (s *boundedSeries) Iterator(it chunkenc.Iterator) chunkenc.Iterator {
	bi, ok := it.(*boundedIterator)
	if !ok {
		bi = &boundedIterator{}
	}
	bi.reset(s.Series.Iterator(bi.it), s.mint, s.maxt)
	return bi
}

// boundedIterator passes on only an iterator's samples from mint to maxt, each later than the one before,
// so a malformed chunk's timestamps never run backwards
type boundedIterator struct {
	it         chunkenc.Iterator
	mint, maxt int64
	last       int64
	vt         chunkenc.ValueType
	started    bool
}

func (b *boundedIterator) reset(it chunkenc.Iterator, mint, maxt int64) {
	*b = boundedIterator{it: it, mint: mint, maxt: maxt, last: math.MinInt64}
}

func (b *boundedIterator) Next() chunkenc.ValueType {
	if !b.started {
		return b.Seek(b.mint)
	}
	if b.vt == chunkenc.ValNone {
		return chunkenc.ValNone
	}
	return b.scan(b.it.Next())
}

func (b *boundedIterator) Seek(t int64) chunkenc.ValueType {
	if b.started && (b.vt == chunkenc.ValNone || b.last >= t) {
		return b.vt
	}
	b.started = true
	return b.scan(b.it.Seek(max(t, b.mint, b.last+1)))
}

// scan returns the first sample from vt on that is in bounds and later than the last returned
func (b *boundedIterator) scan(vt chunkenc.ValueType) chunkenc.ValueType {
	for ; vt != chunkenc.ValNone; vt = b.it.Next() {
		t := b.it.AtT()
		if t > b.maxt {
			break
		}
		if t >= b.mint && t > b.last {
			b.last, b.vt = t, vt
			return vt
		}
	}
	b.vt = chunkenc.ValNone
	return chunkenc.ValNone
}

func (b *boundedIterator) At() (int64, float64) { return b.it.At() }

func (b *boundedIterator) AtHistogram(h *histogram.Histogram) (int64, *histogram.Histogram) {
	return b.it.AtHistogram(h)
}

func (b *boundedIterator) AtFloatHistogram(fh *histogram.FloatHistogram) (int64, *histogram.FloatHistogram) {
	return b.it.AtFloatHistogram(fh)
}

func (b *boundedIterator) AtT() int64  { return b.it.AtT() }
func (b *boundedIterator) AtST() int64 { return b.it.AtST() }
func (b *boundedIterator) Err() error  { return b.it.Err() }

// bound limits each of series to its samples from mint to maxt; series is left as it is, as a source
// may hand out the slice it keeps
func bound(series []storage.Series, mint, maxt int64) []storage.Series {
	out := make([]storage.Series, len(series))
	for i, s := range series {
		out[i] = &boundedSeries{Series: s, mint: mint, maxt: maxt}
	}
	return out
}
