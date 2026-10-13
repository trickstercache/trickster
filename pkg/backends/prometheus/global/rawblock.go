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
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/value"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
)

const (
	// rawBlockCodecVersion starts every encoded block, so a block of another version is refused
	rawBlockCodecVersion byte = 1
	// the fewest bytes an encoded series or chunk can take, which bounds the counts a block can claim
	minEncodedSeriesBytes = 2
	minEncodedChunkBytes  = 4
	// the heap a series or chunk retains besides its labels' and chunk's bytes, for cache accounting
	rawSeriesOverhead = 48
	rawChunkOverhead  = 64
	// rawChunkSamples is the most samples a chunk grows to as later samples are appended, as Prometheus cuts them
	rawChunkSamples = 120
	// rawTailChunks is how many small chunks may trail a block before they are encoded again as full ones
	rawTailChunks = 8
)

var errRawBlockCodec = errors.New("invalid cached raw series")

// rawChunk is an encoded chunk and the span of its samples
type rawChunk struct {
	span
	chk chunkenc.Chunk
}

func compareChunks(a, b rawChunk) int { return cmp.Compare(a.mint, b.mint) }

// rawSeries is a series' chunks in time order, none overlapping another
type rawSeries struct {
	lset   labels.Labels
	chunks []rawChunk
}

func compareRawSeries(a, b rawSeries) int { return labels.Compare(a.lset, b.lset) }

// rawBlock is the series a member holds for one selector, in label order, over the parts of a block of
// time its cache entry's extents name
type rawBlock struct {
	series []rawSeries
}

// normalize returns series' samples from mint to maxt as disjoint chunks in time order, keeping chunks
// wholly inside and encoding the rest again; empty series are left out
func normalize(series []storage.Series, mint, maxt int64) ([]rawSeries, error) {
	out := make([]rawSeries, 0, len(series))
	var r chunkReader
	for _, s := range series {
		chunks, err := normalizeSeries(s, mint, maxt, &r)
		if err != nil {
			return nil, err
		}
		if len(chunks) > 0 {
			out = append(out, rawSeries{lset: s.Labels(), chunks: chunks})
		}
	}
	return sortRawSeries(out, mint, maxt)
}

// normalizeSeries returns s's samples from mint to maxt as disjoint chunks, each read through first, and
// kept as it is only when its samples rise within its reported span
func normalizeSeries(s storage.Series, mint, maxt int64, r *chunkReader) ([]rawChunk, error) {
	cs, ok := s.(*chunkSeries)
	if !ok || !cs.disjoint() {
		return encodeRange(nil, s.Iterator(nil), mint, maxt)
	}
	actual := make([]span, len(cs.chunks))
	for i, c := range cs.chunks {
		sp, rising, err := r.readSpan(c.(chunkenc.Chunk))
		if err != nil {
			return nil, err
		}
		if !rising || (sp.known() && (sp.mint < cs.spans[i].mint || sp.maxt > cs.spans[i].maxt)) {
			// a misreported span can't keep the chunks apart, so their samples are merged and encoded again
			return encodeRange(nil, s.Iterator(nil), mint, maxt)
		}
		actual[i] = sp
	}
	var chunks []rawChunk
	for i, sp := range actual {
		if !sp.known() {
			continue
		}
		var err error
		if chunks, err = cropChunk(chunks, rawChunk{span: sp, chk: cs.chunks[i].(chunkenc.Chunk)}, mint, maxt); err != nil {
			return nil, err
		}
	}
	return chunks, nil
}

// chunkReader reads chunks through, reusing one iterator, and one histogram of each kind to materialize into
type chunkReader struct {
	it chunkenc.Iterator
	h  *histogram.Histogram
	fh *histogram.FloatHistogram
}

// readSpan reads c through, returning the span of its samples, unknown when it has none, and whether they rise
// strictly. A histogram chunk's samples share one bucket layout, materialized once, so a malformed one panics here.
func (r *chunkReader) readSpan(c chunkenc.Chunk) (span, bool, error) {
	sp, rising, laidOut := span{mint: 1, maxt: 0}, true, false
	r.it = c.Iterator(r.it)
	for vt := r.it.Next(); vt != chunkenc.ValNone; vt = r.it.Next() {
		if !laidOut {
			laidOut = r.materialize(vt)
		}
		switch t := r.it.AtT(); {
		case !sp.known():
			sp = span{mint: t, maxt: t}
		case t <= sp.maxt:
			rising = false
		default:
			sp.maxt = t
		}
	}
	return sp, rising, r.it.Err()
}

// materialize reads the current histogram sample into r's own, reporting whether that settles its chunk's
// layout: a float chunk has none, and a stale marker is read without it
func (r *chunkReader) materialize(vt chunkenc.ValueType) bool {
	switch vt {
	case chunkenc.ValHistogram:
		if r.h == nil {
			r.h = &histogram.Histogram{}
		}
		_, h := r.it.AtHistogram(r.h)
		return !value.IsStaleNaN(h.Sum)
	case chunkenc.ValFloatHistogram:
		if r.fh == nil {
			r.fh = &histogram.FloatHistogram{}
		}
		_, fh := r.it.AtFloatHistogram(r.fh)
		return !value.IsStaleNaN(fh.Sum)
	}
	return true
}

// cropChunk appends c's samples from mint to maxt to dst: c itself when it lies wholly inside, or else
// those samples encoded again
func cropChunk(dst []rawChunk, c rawChunk, mint, maxt int64) ([]rawChunk, error) {
	switch {
	case c.maxt < mint || c.mint > maxt:
		return dst, nil
	case c.mint >= mint && c.maxt <= maxt:
		return append(dst, c), nil
	}
	return encodeRange(dst, c.chk.Iterator(nil), max(mint, c.mint), min(maxt, c.maxt))
}

// encodeRange appends to dst the samples it reads from mint to maxt, encoded as chunks
func encodeRange(dst []rawChunk, it chunkenc.Iterator, mint, maxt int64) ([]rawChunk, error) {
	bi := &boundedIterator{}
	bi.reset(it, mint, maxt)
	chunks := storage.NewSeriesToChunkEncoder(iteratorSeries{it: bi}).Iterator(nil)
	for chunks.Next() {
		m := chunks.At()
		m.Chunk.Compact()
		dst = append(dst, rawChunk{mint: m.MinTime, maxt: m.MaxTime, chk: m.Chunk})
	}
	return dst, chunks.Err()
}

// iteratorSeries is a series whose samples are an iterator's, for an encoder to read once
type iteratorSeries struct {
	it chunkenc.Iterator
}

func (iteratorSeries) Labels() labels.Labels                          { return labels.EmptyLabels() }
func (s iteratorSeries) Iterator(chunkenc.Iterator) chunkenc.Iterator { return s.it }

// sortRawSeries sorts series by labels, joining any a member sent twice
func sortRawSeries(series []rawSeries, mint, maxt int64) ([]rawSeries, error) {
	if !slices.IsSortedFunc(series, compareRawSeries) {
		slices.SortStableFunc(series, compareRawSeries)
	}
	out := series[:0]
	for _, s := range series {
		n := len(out)
		if n == 0 || !labels.Equal(out[n-1].lset, s.lset) {
			out = append(out, s)
			continue
		}
		joined, err := joinChunks(out[n-1].chunks, s.chunks, mint, maxt)
		if err != nil {
			return nil, err
		}
		out[n-1].chunks = joined
	}
	return out, nil
}

// joinChunks returns two chunk lists of one series as one, merging their samples where they overlap
func joinChunks(a, b []rawChunk, mint, maxt int64) ([]rawChunk, error) {
	all := append(slices.Clip(a), b...)
	slices.SortFunc(all, compareChunks)
	disjoint := true
	for i := 1; i < len(all) && disjoint; i++ {
		disjoint = all[i].mint > all[i-1].maxt
	}
	if disjoint {
		return all, nil
	}
	iterables := make([]chunkenc.Iterable, len(all))
	for i, c := range all {
		iterables[i] = c.chk
	}
	return encodeRange(nil, storage.ChainSampleIteratorFromIterables(nil, iterables), mint, maxt)
}

// cropSeries returns series' samples within extents, which are in time order
func cropSeries(series []rawSeries, extents [][2]int64) ([]rawSeries, error) {
	out := make([]rawSeries, 0, len(series))
	for _, s := range series {
		var chunks []rawChunk
		for _, e := range extents {
			from := sort.Search(len(s.chunks), func(i int) bool { return s.chunks[i].maxt >= e[0] })
			for _, c := range s.chunks[from:] {
				if c.mint > e[1] {
					break
				}
				var err error
				if chunks, err = cropChunk(chunks, c, e[0], e[1]); err != nil {
					return nil, err
				}
			}
		}
		if len(chunks) > 0 {
			out = append(out, rawSeries{lset: s.lset, chunks: chunks})
		}
	}
	return out, nil
}

// mergeBlock returns b, left as it is since it may be shared, with add merged in; complete reports that
// extents, which now hold both, cover the whole block
func mergeBlock(b *rawBlock, add []rawSeries, extents timeseries.ExtentList, complete bool) *rawBlock {
	var old []rawSeries
	if b != nil {
		old = b.series
	}
	out := make([]rawSeries, 0, max(len(old), len(add)))
	i, j := 0, 0
	for i < len(old) || j < len(add) {
		c := 1
		switch {
		case j == len(add):
			c = -1
		case i < len(old):
			c = labels.Compare(old[i].lset, add[j].lset)
		}
		switch {
		case c < 0 && complete:
			// a series the last read added nothing to is still compacted once its block is complete
			out = append(out, rawSeries{lset: old[i].lset, chunks: compact(old[i].chunks, nil, extents, complete)})
			i++
		case c < 0:
			out = append(out, old[i])
			i++
		case c > 0:
			out = append(out, rawSeries{lset: add[j].lset, chunks: compact(nil, add[j].chunks, extents, complete)})
			j++
		default:
			out = append(out, rawSeries{lset: old[i].lset, chunks: compact(old[i].chunks, add[j].chunks, extents,
				complete)})
			i++
			j++
		}
	}
	return &rawBlock{series: out}
}

// compact returns old's and add's chunks in time order, encoding the small chunks refreshes leave again as
// full ones once a run ends, grows past rawTailChunks, or completes
func compact(old, add []rawChunk, extents timeseries.ExtentList, complete bool) []rawChunk {
	out := make([]rawChunk, 0, len(old)+len(add))
	out = append(append(out, old...), add...)
	if len(old) > 0 && len(add) > 0 && add[0].mint <= old[len(old)-1].maxt {
		slices.SortFunc(out, compareChunks)
	}
	var merged []rawChunk
	for i := 0; i < len(out); {
		j := runEnd(out, i, extents)
		// the trailing run still grows with each refresh, so it waits until it holds enough to encode once
		if j-i > 1 && (complete || j < len(out) || j-i > rawTailChunks) {
			if run, ok := mergeRun(out[i:j]); ok {
				if merged == nil {
					merged = append(make([]rawChunk, 0, len(out)), out[:i]...)
				}
				merged = append(merged, run...)
				i = j
				continue
			}
		}
		if merged != nil {
			merged = append(merged, out[i:j]...)
		}
		i = j
	}
	if merged != nil {
		return merged
	}
	return out
}

// runEnd returns the end of the run of small chunks starting at i: contiguous, of one encoding, and none but
// its first starting with a reset; a full chunk is a run of its own
func runEnd(chunks []rawChunk, i int, extents timeseries.ExtentList) int {
	j := i + 1
	for j < len(chunks) && chunks[j-1].chk.NumSamples() < rawChunkSamples && chunks[j].chk.NumSamples() < rawChunkSamples &&
		chunks[j].chk.Encoding() == chunks[i].chk.Encoding() && contiguous(chunks[j-1].maxt, chunks[j].mint, extents) &&
		!startsWithReset(chunks[j].chk) {
		j++
	}
	return j
}

// contiguous reports whether one extent holds both a and b, so no uncovered time lies between them
func contiguous(a, b int64, extents timeseries.ExtentList) bool {
	i := sort.Search(len(extents), func(i int) bool { return extents[i].End.UnixMilli() >= a })
	return i < len(extents) && extents[i].Start.UnixMilli() <= a && extents[i].End.UnixMilli() >= b
}

// mergeRun encodes run's samples again as new chunks of up to rawChunkSamples, cut where a histogram
// resets; run's chunks, disjoint and of one encoding, may be shared
func mergeRun(run []rawChunk) ([]rawChunk, bool) {
	enc := run[0].chk.Encoding()
	var out []rawChunk
	var chk chunkenc.Chunk
	var app chunkenc.Appender
	var first, last int64
	flush := func() {
		if chk != nil && chk.NumSamples() > 0 {
			chk.Compact()
			out = append(out, rawChunk{mint: first, maxt: last, chk: chk})
		}
		chk = nil
	}
	var it chunkenc.Iterator
	var h *histogram.Histogram
	var fh *histogram.FloatHistogram
	for _, c := range run {
		it = c.chk.Iterator(it)
		for vt := it.Next(); vt != chunkenc.ValNone; vt = it.Next() {
			t := it.AtT()
			if chk == nil || chk.NumSamples() >= rawChunkSamples {
				flush()
				var err error
				if chk, err = chunkenc.NewEmptyChunk(enc); err != nil {
					return nil, false
				}
				if app, err = chk.Appender(); err != nil {
					return nil, false
				}
				first = t
			}
			var cut chunkenc.Chunk
			var recoded bool
			var err error
			switch vt {
			case chunkenc.ValFloat:
				_, v := it.At()
				app.Append(it.AtST(), t, v)
			case chunkenc.ValHistogram:
				_, h = it.AtHistogram(h)
				cut, recoded, app, err = app.AppendHistogram(nil, it.AtST(), t, h, false)
			case chunkenc.ValFloatHistogram:
				_, fh = it.AtFloatHistogram(fh)
				cut, recoded, app, err = app.AppendFloatHistogram(nil, it.AtST(), t, fh, false)
			}
			if err != nil {
				return nil, false
			}
			// a new bucket layout recodes the chunk, while a reset starts another
			if cut != nil && !recoded {
				flush()
				first = t
			}
			if cut != nil {
				chk = cut
			}
			last = t
		}
		if it.Err() != nil {
			return nil, false
		}
	}
	flush()
	return out, true
}

// startsWithReset reports whether c's first sample is a histogram hinted as a counter reset
func startsWithReset(c chunkenc.Chunk) bool {
	if enc := c.Encoding(); enc != chunkenc.EncHistogram && enc != chunkenc.EncFloatHistogram {
		return false
	}
	it := c.Iterator(nil)
	switch it.Next() {
	case chunkenc.ValHistogram:
		_, h := it.AtHistogram(nil)
		return h.CounterResetHint == histogram.CounterReset
	case chunkenc.ValFloatHistogram:
		_, fh := it.AtFloatHistogram(nil)
		return fh.CounterResetHint == histogram.CounterReset
	}
	return false
}

// rawSource is series whose chunks are read only from lo to hi; block is their cached block's key, or
// empty for a member's answer, counted as it arrived. Only series meeting every match matcher are read.
type rawSource struct {
	series []rawSeries
	lo, hi int64
	block  string
	match  []*labels.Matcher
}

// assemble joins sources, whose ranges are disjoint, into the series with samples in range, in label
// order; it returns the cached samples, or a block it can't read
func assemble(sources []rawSource) ([]storage.Series, int64, string, error) {
	heads := make([]int, len(sources))
	// matched is each filtered source's head once it has met the source's matchers
	matched := make([]int, len(sources))
	var out []storage.Series
	var cached int64
	for {
		var lset labels.Labels
		found := false
		for i, src := range sources {
			if len(src.match) > 0 {
				for heads[i] < len(src.series) && matched[i] <= heads[i] {
					if matchesAll(src.series[heads[i]].lset, src.match) {
						matched[i] = heads[i] + 1
						break
					}
					heads[i]++
				}
			}
			if heads[i] < len(src.series) && (!found || labels.Compare(src.series[heads[i]].lset, lset) < 0) {
				lset, found = src.series[heads[i]].lset, true
			}
		}
		if !found {
			return out, cached, "", nil
		}
		var views []chunkView
		for i, src := range sources {
			if heads[i] < len(src.series) && labels.Equal(src.series[heads[i]].lset, lset) {
				var n int64
				var err error
				if views, n, err = appendViews(views, src.series[heads[i]].chunks, src.lo, src.hi, src.block != ""); err != nil {
					return nil, 0, src.block, err
				}
				cached += n
				heads[i]++
			}
		}
		if len(views) == 0 {
			continue
		}
		if !slices.IsSortedFunc(views, compareViews) {
			slices.SortFunc(views, compareViews)
		}
		out = append(out, &viewSeries{lset: lset, views: views})
	}
}

// appendViews appends a view of each chunk reaching from lo to hi; with count set, it counts their samples
// there, reading only chunks crossing an end, skipping those with none
func appendViews(dst []chunkView, chunks []rawChunk, lo, hi int64, count bool) ([]chunkView, int64, error) {
	var total int64
	i := sort.Search(len(chunks), func(i int) bool { return chunks[i].maxt >= lo })
	for ; i < len(chunks) && chunks[i].mint <= hi; i++ {
		c := chunks[i]
		if count {
			n, err := samplesWithin(c.chk, c.span, lo, hi)
			if err != nil {
				return nil, 0, err
			}
			if n == 0 {
				continue
			}
			total += n
		}
		dst = append(dst, chunkView{chk: c.chk, lo: max(lo, c.mint), hi: min(hi, c.maxt)})
	}
	return dst, total, nil
}

// chunkView is a chunk read only from lo to hi
type chunkView struct {
	chk    chunkenc.Chunk
	lo, hi int64
}

func compareViews(a, b chunkView) int { return cmp.Compare(a.lo, b.lo) }

// viewSeries is a series read from disjoint chunk views in time order
type viewSeries struct {
	lset  labels.Labels
	views []chunkView
}

func (s *viewSeries) Labels() labels.Labels { return s.lset }

func (s *viewSeries) Iterator(it chunkenc.Iterator) chunkenc.Iterator {
	vi, ok := it.(*viewIterator)
	if !ok {
		vi = &viewIterator{}
	}
	vi.reset(s.views)
	return vi
}

// viewIterator reads each view in turn, passing on only samples later than the last it passed on
type viewIterator struct {
	views []chunkView
	idx   int
	cur   boundedIterator
	// it is the current view's chunk iterator, reused for each view
	it   chunkenc.Iterator
	last int64
	vt   chunkenc.ValueType
	err  error
}

func (v *viewIterator) reset(views []chunkView) {
	v.views, v.idx, v.last, v.vt, v.err = views, -1, math.MinInt64, chunkenc.ValNone, nil
}

func (v *viewIterator) Next() chunkenc.ValueType {
	if v.idx >= 0 && v.idx < len(v.views) {
		if vt := v.cur.Next(); vt != chunkenc.ValNone {
			return v.found(vt)
		}
		if err := v.cur.Err(); err != nil {
			return v.fail(err)
		}
	}
	return v.open(v.idx+1, math.MinInt64)
}

func (v *viewIterator) Seek(t int64) chunkenc.ValueType {
	if v.vt != chunkenc.ValNone && v.last >= t {
		return v.vt
	}
	if v.idx >= len(v.views) {
		return chunkenc.ValNone
	}
	if v.idx >= 0 && v.views[v.idx].hi >= t {
		if vt := v.cur.Seek(t); vt != chunkenc.ValNone {
			return v.found(vt)
		}
		if err := v.cur.Err(); err != nil {
			return v.fail(err)
		}
	}
	from := v.idx + 1
	return v.open(from+sort.Search(len(v.views)-from, func(i int) bool { return v.views[from+i].hi >= t }), t)
}

// open reads the views from i on, from t, until one has a sample later than the last passed on
func (v *viewIterator) open(i int, t int64) chunkenc.ValueType {
	for ; i < len(v.views); i++ {
		view := v.views[i]
		if view.hi <= v.last {
			continue
		}
		v.idx = i
		v.it = view.chk.Iterator(v.it)
		v.cur.reset(v.it, view.lo, view.hi)
		v.cur.last = v.last
		if vt := v.cur.Seek(t); vt != chunkenc.ValNone {
			return v.found(vt)
		}
		if err := v.cur.Err(); err != nil {
			return v.fail(err)
		}
	}
	v.idx, v.vt = len(v.views), chunkenc.ValNone
	return chunkenc.ValNone
}

func (v *viewIterator) found(vt chunkenc.ValueType) chunkenc.ValueType {
	v.last, v.vt = v.cur.last, vt
	return vt
}

func (v *viewIterator) fail(err error) chunkenc.ValueType {
	v.err, v.idx, v.vt = err, len(v.views), chunkenc.ValNone
	return chunkenc.ValNone
}

func (v *viewIterator) At() (int64, float64) { return v.cur.At() }

func (v *viewIterator) AtHistogram(h *histogram.Histogram) (int64, *histogram.Histogram) {
	return v.cur.AtHistogram(h)
}

func (v *viewIterator) AtFloatHistogram(fh *histogram.FloatHistogram) (int64, *histogram.FloatHistogram) {
	return v.cur.AtFloatHistogram(fh)
}

func (v *viewIterator) AtT() int64  { return v.cur.AtT() }
func (v *viewIterator) AtST() int64 { return v.cur.AtST() }
func (v *viewIterator) Err() error  { return v.err }

// rawBlockCodec encodes blocks for caches that hold bytes; chunks are kept in their own encodings
type rawBlockCodec struct{}

func (c rawBlockCodec) Marshal(b *rawBlock) ([]byte, error) { return c.AppendMarshal(nil, b) }

func (rawBlockCodec) AppendMarshal(dst []byte, b *rawBlock) ([]byte, error) {
	if b == nil {
		return nil, errRawBlockCodec
	}
	dst = append(dst, rawBlockCodecVersion)
	dst = binary.AppendUvarint(dst, uint64(len(b.series)))
	for _, s := range b.series {
		dst = binary.AppendUvarint(dst, uint64(s.lset.Len())) //nolint:gosec // a count is never negative
		s.lset.Range(func(l labels.Label) {
			dst = appendLengthPrefixed(dst, l.Name)
			dst = appendLengthPrefixed(dst, l.Value)
		})
		dst = binary.AppendUvarint(dst, uint64(len(s.chunks)))
		for _, c := range s.chunks {
			dst = binary.AppendVarint(dst, c.mint)
			dst = binary.AppendVarint(dst, c.maxt)
			dst = append(dst, byte(c.chk.Encoding()))
			dst = appendLengthPrefixed(dst, c.chk.Bytes())
		}
	}
	return dst, nil
}

func appendLengthPrefixed[T string | []byte](dst []byte, v T) []byte {
	return append(binary.AppendUvarint(dst, uint64(len(v))), v...)
}

// Unmarshal decodes a block whose chunks refer to data, and refuses one AppendMarshal couldn't have written
func (rawBlockCodec) Unmarshal(data []byte) (*rawBlock, error) {
	if len(data) == 0 || data[0] != rawBlockCodecVersion {
		return nil, errRawBlockCodec
	}
	r := &rawReader{data: data[1:]}
	series := make([]rawSeries, r.count(minEncodedSeriesBytes))
	var builder labels.ScratchBuilder
	for i := range series {
		builder.Reset()
		for range r.count(2) {
			name, value := r.bytes(), r.bytes()
			builder.Add(string(name), string(value))
		}
		builder.Sort()
		chunks := make([]rawChunk, r.count(minEncodedChunkBytes))
		for j := range chunks {
			c := &chunks[j]
			c.mint, c.maxt = r.varint(), r.varint()
			enc, b := chunkenc.Encoding(r.byte()), r.bytes()
			if r.err != nil || !c.known() || (j > 0 && c.mint <= chunks[j-1].maxt) {
				return nil, errRawBlockCodec
			}
			chk, err := newChunk(enc, b)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", errRawBlockCodec, err)
			}
			c.chk = chk
		}
		if r.err != nil {
			return nil, errRawBlockCodec
		}
		series[i] = rawSeries{lset: builder.Labels(), chunks: chunks}
		if i > 0 && labels.Compare(series[i-1].lset, series[i].lset) >= 0 {
			return nil, errRawBlockCodec
		}
	}
	if r.err != nil || len(r.data) != 0 {
		return nil, errRawBlockCodec
	}
	return &rawBlock{series: series}, nil
}

func (rawBlockCodec) Size(b *rawBlock) int {
	if b == nil {
		return 0
	}
	size := 0
	for _, s := range b.series {
		size += rawSeriesOverhead + int(s.lset.ByteSize()) //nolint:gosec // a label set's size fits an int
		for _, c := range s.chunks {
			size += rawChunkOverhead + len(c.chk.Bytes())
		}
	}
	return size
}

// rawReader reads what AppendMarshal writes, recording the first malformed read
type rawReader struct {
	data []byte
	err  error
}

func (r *rawReader) uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.data)
	if n <= 0 {
		r.err = errRawBlockCodec
		return 0
	}
	r.data = r.data[n:]
	return v
}

func (r *rawReader) varint() int64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Varint(r.data)
	if n <= 0 {
		r.err = errRawBlockCodec
		return 0
	}
	r.data = r.data[n:]
	return v
}

func (r *rawReader) byte() byte {
	if r.err != nil || len(r.data) == 0 {
		r.err = errRawBlockCodec
		return 0
	}
	b := r.data[0]
	r.data = r.data[1:]
	return b
}

// bytes returns the next length-prefixed bytes, capped so nothing appended to them reaches the data after
func (r *rawReader) bytes() []byte {
	n := r.uvarint()
	if r.err != nil || n > uint64(len(r.data)) {
		r.err = errRawBlockCodec
		return nil
	}
	b := r.data[:n:n]
	r.data = r.data[n:]
	return b
}

// count reads a count of items each taking at least size bytes, refusing one the data can't hold
func (r *rawReader) count(size int) int {
	n := r.uvarint()
	if r.err == nil && n > uint64(len(r.data)/size) { //nolint:gosec // a length is never negative
		r.err = errRawBlockCodec
	}
	if r.err != nil {
		return 0
	}
	return int(n) //nolint:gosec // bounded by the data's length above
}
