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
	"fmt"
	"slices"
	"unsafe"

	"github.com/cespare/xxhash/v2"
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// decodeExpansion is how many times a response's byte limit the series kept from it may take in memory, above what
	// small chunks and short labels expand to
	decodeExpansion = 4
	// countQuantum is the most samples a decoder counts before adding them to its budget, which a shared read
	// charges to each evaluation it is for
	countQuantum = 4096
	// seriesSlotBytes is the memory a series takes in the slice of a response's series
	seriesSlotBytes = int64(unsafe.Sizeof(storage.Series(nil)))
	// chunkSeriesBytes and sampleSeriesBytes are the memory a series keeps besides its labels, chunks and samples
	chunkSeriesBytes  = seriesSlotBytes + int64(unsafe.Sizeof(chunkSeries{}))
	sampleSeriesBytes = seriesSlotBytes + int64(unsafe.Sizeof(sampleSeries{}))
	// chunkBytes is the memory a kept chunk takes besides its data: itself, and its place and span in its series
	chunkBytes = int64(unsafe.Sizeof(chunkenc.Iterable(nil)) + unsafe.Sizeof(span{}) + unsafe.Sizeof(chunkenc.XORChunk{}))
	// labelSlotBytes is what the label builder takes for each label of the series with the most
	labelSlotBytes = int64(unsafe.Sizeof(labels.Label{}))
	// labelLengthBytes is what a label set takes for each label besides its name and value: their lengths
	labelLengthBytes = 2
	// sampleBytes, floatHistogramBytes, spanBytes and bucketBytes are the memory a sample read as samples takes
	sampleBytes         = int64(unsafe.Sizeof(sample{}))
	floatHistogramBytes = int64(unsafe.Sizeof(histogram.FloatHistogram{}))
	spanBytes           = int64(unsafe.Sizeof(histogram.Span{}))
	bucketBytes         = int64(unsafe.Sizeof(float64(0)))
	// unsafeLabelStrings is set when a label set copies the strings it is built from, so they may share a message's memory
	unsafeLabelStrings = labels.ImplementationName == "stringlabels"
	// groupBytes is what an entry of a map of label set hashes to sums may take, its table as little as 7/16 full once grown
	groupBytes = 48
	// labelSeparator ends each name and value hashed into a label set's hash
	labelSeparator = "\xff"
)

// decoder reads one member response's series, keeping only chunks and samples from mint to maxt. Before keeping any,
// it counts their samples against the budget and charges the memory they take against the response's decode limit.
type decoder struct {
	budget     *Budget
	mint, maxt int64
	// kind is the error a malformed response fails with
	kind error
	// limit is the most memory what's kept may take, of which allowance remains
	limit, allowance int64
	// series and samples are counted but not yet added to the budget
	series, samples int64
	// labelSlots is the most labels one series has held in builder, each slot charged once
	labelSlots int
	// labelCount and labelBytes count the labels in builder and the memory a label set of them takes
	labelCount int
	labelBytes int64
	builder    labels.ScratchBuilder
	// lset is overwritten with a series' labels, to compare them with the series before it, or to hash them
	lset labels.Labels
	// scratch reads chunks across mint or maxt through, a chunk and iterator of each encoding a member sends reused
	scratch [chunkenc.EncXOR2 + 1]scratchChunk
	// scratchLaid is the most a chunk read through each encoding's scratch lays out, which its iterator keeps
	scratchLaid [chunkenc.EncXOR2 + 1]int64
	// laid is the most memory charged for what reading histogram chunks lays out at once: what the scratch iterators
	// keep, or every kept chunk of a series with its copies and replicas, as deduplication chains them all
	laid int64
	// replicaLabels are the labels deduplication groups series without, reading a group's series together
	replicaLabels []string
	// groups is what the kept histogram chunks of each group lay out, by the hash of its labels; group is that hash for
	// the message being read, once grouped is set
	groups  map[uint64]int64
	group   uint64
	grouped bool
	// key is a label set without replicaLabels, as it is hashed
	key []byte
	out []storage.Series
	// last is the series a series message continues when its labels are the same
	last *chunkSeries
}

// scratchChunk is a chunk reset onto each chunk data of its encoding that is read through, and its iterator
type scratchChunk struct {
	chk chunkenc.Chunk
	it  chunkenc.Iterator
}

func newDecoder(budget *Budget, mint, maxt int64, kind error) *decoder {
	limit := budget.decodeLimit()
	return &decoder{budget: budget, mint: mint, maxt: maxt, kind: kind, limit: limit, allowance: limit}
}

// charge takes n bytes from the allowance before they are allocated, failing once the response would keep more
func (d *decoder) charge(n int64) error {
	if d.allowance -= n; d.allowance < 0 {
		return fmt.Errorf("%w: the series decoded from a response would take more than %d bytes in memory",
			ErrFetchLimit, d.limit)
	}
	return nil
}

// lay charges the memory reading histogram chunks lays out at once, n, for as much as it exceeds the most charged
// before, as iterators reuse what they laid out
func (d *decoder) lay(n int64) error {
	if n <= d.laid {
		return nil
	}
	if err := d.charge(n - d.laid); err != nil {
		return err
	}
	d.laid = n
	return nil
}

// layGroup adds what a kept chunk lays out to its series' group, charging the most any group lays out at once: a
// series' iterator chains its chunks, and deduplication chains every copy and replica of a series
func (d *decoder) layGroup(laid int64) error {
	if !d.grouped {
		d.group, d.grouped = d.groupKey(), true
	}
	sum, ok := d.groups[d.group]
	if !ok {
		if err := d.charge(groupBytes); err != nil {
			return err
		}
		if d.groups == nil {
			d.groups = make(map[uint64]int64)
		}
	}
	sum += laid
	if err := d.lay(sum); err != nil {
		return err
	}
	d.groups[d.group] = sum
	return nil
}

// groupKey returns the hash of the labels in the builder without replicaLabels, which deduplication groups series by
func (d *decoder) groupKey() uint64 {
	d.builder.Overwrite(&d.lset)
	if len(d.replicaLabels) == 0 {
		return d.lset.Hash()
	}
	key := d.key[:0]
	d.lset.Range(func(l labels.Label) {
		if !slices.Contains(d.replicaLabels, l.Name) {
			key = append(append(append(append(key, l.Name...), labelSeparator...), l.Value...), labelSeparator...)
		}
	})
	d.key = key
	return xxhash.Sum64(key)
}

// layScratch charges what the chunk of enc read through lays out, laid, as its scratch iterator keeps the most it
// laid out while the other encodings' keep theirs
func (d *decoder) layScratch(enc chunkenc.Encoding, laid int64) error {
	if laid <= d.scratchLaid[enc] {
		return nil
	}
	d.scratchLaid[enc] = laid
	return d.lay(d.scratchLaid[chunkenc.EncHistogram] + d.scratchLaid[chunkenc.EncFloatHistogram])
}

// release drops what the decoder still refers to of a message buffer that another replaces: the label strings its
// builder holds past the last series' labels, and the chunk data each scratch chunk read through last
func (d *decoder) release() {
	if unsafeLabelStrings {
		d.builder.Reset()
		for range d.labelSlots {
			d.builder.Add("", "")
		}
		d.builder.Reset()
	}
	clear(d.scratch[:])
}

// count counts series and samples before they are kept, failing once the budget can't afford them. They are added to
// it as each quantum gathers, and as each frame or message ends, so a budget concurrent reads share is seldom written.
func (d *decoder) count(series, samples int64) error {
	d.series += series
	d.samples += samples
	if d.samples >= countQuantum || d.budget.exceeds(d.series, d.samples) {
		return d.flush()
	}
	return nil
}

// flush adds what's been counted to the budget
func (d *decoder) flush() error {
	if d.series == 0 && d.samples == 0 {
		return nil
	}
	series, samples := d.series, d.samples
	d.series, d.samples = 0, 0
	return d.budget.add(series, samples)
}

// wireField is one field of a protobuf message: its varint or fixed-size value, or its bytes when length-delimited
type wireField struct {
	num protowire.Number
	typ protowire.Type
	u   uint64
	b   []byte
}

// field reads the field b starts with, returning the bytes after it; any length is checked against b's
func (d *decoder) field(b []byte) (wireField, []byte, error) {
	num, typ, n := protowire.ConsumeTag(b)
	if n < 0 {
		return wireField{}, nil, d.parseError(n)
	}
	b = b[n:]
	f := wireField{num: num, typ: typ}
	switch typ {
	case protowire.VarintType:
		f.u, n = protowire.ConsumeVarint(b)
	case protowire.Fixed64Type:
		f.u, n = protowire.ConsumeFixed64(b)
	case protowire.BytesType:
		f.b, n = protowire.ConsumeBytes(b)
	default:
		n = protowire.ConsumeFieldValue(num, typ, b)
	}
	if n < 0 {
		return wireField{}, nil, d.parseError(n)
	}
	return f, b[n:], nil
}

func (d *decoder) parseError(n int) error {
	return fmt.Errorf("%w: %w", d.kind, protowire.ParseError(n))
}

// expect refuses a field without the wire type its field number is declared with
func (d *decoder) expect(f wireField, typ protowire.Type) error {
	if f.typ == typ {
		return nil
	}
	return fmt.Errorf("%w: field %d has wire type %d, not %d", d.kind, f.num, f.typ, typ)
}

// repeated returns how many values a repeated scalar field holds: one when it isn't packed, and is of wire type
// typ, or as many varints, or fixed64 values when typ is that, as its packed bytes hold
func (d *decoder) repeated(f wireField, typ protowire.Type) (int, error) {
	if f.typ == typ {
		return 1, nil
	}
	if err := d.expect(f, protowire.BytesType); err != nil {
		return 0, err
	}
	if typ == protowire.Fixed64Type {
		if len(f.b)%8 != 0 {
			return 0, fmt.Errorf("%w: field %d packs %d bytes, not whole 8-byte values", d.kind, f.num, len(f.b))
		}
		return len(f.b) / 8, nil
	}
	var count int
	for b := f.b; len(b) > 0; count++ {
		_, n := protowire.ConsumeVarint(b)
		if n < 0 {
			return 0, d.parseError(n)
		}
		b = b[n:]
	}
	return count, nil
}

// readLabels adds the labels of a series message to the builder, sorted, returning the memory a label set of
// them takes; StoreAPI's Series and remote read's ChunkedSeries and TimeSeries number their labels alike
func (d *decoder) readLabels(b []byte) (int64, error) {
	d.resetLabels()
	for len(b) > 0 {
		f, rest, err := d.field(b)
		if err != nil {
			return 0, err
		}
		b = rest
		if f.num != seriesLabels {
			continue
		}
		if err := d.addLabel(f); err != nil {
			return 0, err
		}
	}
	d.builder.Sort()
	return d.labelBytes, nil
}

// resetLabels empties the builder for the next series' labels
func (d *decoder) resetLabels() {
	d.builder.Reset()
	d.labelCount, d.labelBytes = 0, 0
}

// addLabel adds a label field's name and value to the builder, which keeps a slot for each label of the series with
// the most, each charged as it is first needed
func (d *decoder) addLabel(f wireField) error {
	if err := d.expect(f, protowire.BytesType); err != nil {
		return err
	}
	name, value, err := d.label(f.b)
	if err != nil {
		return err
	}
	if d.labelCount++; d.labelCount > d.labelSlots {
		if err := d.charge(labelSlotBytes); err != nil {
			return err
		}
		d.labelSlots = d.labelCount
	}
	d.builder.Add(labelString(name), labelString(value))
	d.labelBytes += int64(len(name)+len(value)) + labelLengthBytes
	return nil
}

// label reads a Label message's name and value
func (d *decoder) label(b []byte) (name, value []byte, err error) {
	for len(b) > 0 {
		var f wireField
		if f, b, err = d.field(b); err != nil {
			return nil, nil, err
		}
		if f.num != labelName && f.num != labelValue {
			continue
		}
		if err = d.expect(f, protowire.BytesType); err != nil {
			return nil, nil, err
		}
		if f.num == labelName {
			name = f.b
		} else {
			value = f.b
		}
	}
	return name, value, nil
}

// labelString returns b as a string, sharing its memory when label sets copy what they are built from
func labelString(b []byte) string {
	if unsafeLabelStrings {
		return unsafe.String(unsafe.SliceData(b), len(b))
	}
	return string(b)
}

// wireChunk is a chunk as a series message holds it, with the span its member reported
type wireChunk struct {
	enc  chunkenc.Encoding
	data []byte
	sp   span
}

// chunkParser reads one chunk field of a series message, refusing an encoding or data no chunk can have
type chunkParser func(d *decoder, b []byte) (wireChunk, error)

// chunkedSeries reads a series message of labels and of chunks, which parse reads, keeping each chunk with samples
// from mint to maxt in the series the message continues, when it has the same labels, or else in a new one
func (d *decoder) chunkedSeries(b []byte, parse chunkParser) error {
	size, err := d.readLabels(b)
	if err != nil {
		return err
	}
	d.grouped = false
	cur := d.last
	if cur != nil {
		d.builder.Overwrite(&d.lset)
		if !labels.Equal(cur.lset, d.lset) {
			cur = nil
		}
	}
	for len(b) > 0 {
		// readLabels read every field, so none fails to read here
		f, rest, _ := d.field(b)
		b = rest
		if f.num != seriesChunks {
			continue
		}
		if err := d.expect(f, protowire.BytesType); err != nil {
			return err
		}
		c, err := parse(d, f.b)
		if err != nil {
			return err
		}
		if cur, err = d.keep(cur, c, size); err != nil {
			return err
		}
	}
	// a message of another series keeps nothing in range, yet still ends the one before it
	d.last = cur
	return nil
}

// keep adds c to cur, the series being read, when c has samples from mint to maxt; the series, whose labels
// take size bytes, is created for its first such chunk. It returns the series.
func (d *decoder) keep(cur *chunkSeries, c wireChunk, size int64) (*chunkSeries, error) {
	n, laid, err := d.within(c)
	if err != nil || n == 0 {
		return cur, err
	}
	var newSeries int64
	cost := chunkBytes + int64(len(c.data))
	if cur == nil {
		newSeries, cost = 1, cost+chunkSeriesBytes+size
	}
	if err := d.count(newSeries, n); err != nil {
		return nil, err
	}
	if err := d.charge(cost); err != nil {
		return nil, err
	}
	if laid > 0 {
		if err := d.layGroup(laid); err != nil {
			return nil, err
		}
	}
	// the chunk keeps its data, so it is copied out of the message; parse checked its encoding
	chk, _ := chunkenc.FromData(c.enc, append([]byte(nil), c.data...))
	if cur == nil {
		cur = &chunkSeries{lset: d.builder.Labels()}
		d.out = append(d.out, cur)
	}
	cur.chunks = append(cur.chunks, chk)
	cur.spans = append(cur.spans, c.sp)
	return cur, nil
}

// within returns how many of c's samples lie from mint to maxt, and what reading c lays out once its layout is checked:
// none when its span lies outside, as Prometheus's remote-read client skips such a chunk, its header's count when inside
func (d *decoder) within(c wireChunk) (n, laid int64, err error) {
	if c.sp.outside(d.mint, d.maxt) {
		return 0, 0, nil
	}
	inside := c.sp.inside(d.mint, d.maxt)
	if inside {
		if n = headerSamples(c.data); n == 0 {
			return 0, 0, nil
		}
	}
	if laid, err = histogramLayout(c.enc, c.data); err != nil {
		return 0, 0, fmt.Errorf("%w: %w", d.kind, err)
	}
	if inside {
		return n, laid, nil
	}
	// else it is read through by its encoding's scratch chunk, one of each encoding the parsers produce
	if err := d.layScratch(c.enc, laid); err != nil {
		return 0, 0, err
	}
	s := &d.scratch[c.enc]
	if s.chk == nil {
		s.chk, _ = chunkenc.FromData(c.enc, c.data)
	} else {
		s.chk.Reset(c.data)
	}
	if n, s.it, err = readWithin(s.chk, d.mint, d.maxt, s.it); err != nil {
		return 0, 0, fmt.Errorf("%w: %w", d.kind, err)
	}
	return n, laid, nil
}
