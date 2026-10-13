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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
)

// steps returns float samples every 10ms from first to last, valued as their timestamps
func steps(first, last int64) []testPoint {
	var out []testPoint
	for ts := first; ts <= last; ts += 10 {
		out = append(out, testPoint{t: ts, f: float64(ts)})
	}
	return out
}

// xorChunkOf encodes points as one chunk with its span
func xorChunkOf(t *testing.T, points []testPoint) (chunkenc.Chunk, span) {
	t.Helper()
	c := chunkenc.NewXORChunk()
	app, err := c.Appender()
	require.NoError(t, err)
	for _, p := range points {
		app.Append(0, p.t, p.f)
	}
	return c, span{mint: points[0].t, maxt: points[len(points)-1].t}
}

// seriesOf builds a member's series from chunks of points, each with its span
func seriesOf(t *testing.T, lset labels.Labels, parts ...[]testPoint) *chunkSeries {
	t.Helper()
	cs := &chunkSeries{lset: lset}
	for _, points := range parts {
		c, sp := xorChunkOf(t, points)
		cs.chunks, cs.spans = append(cs.chunks, c), append(cs.spans, sp)
	}
	return cs
}

func rawPoints(t *testing.T, s rawSeries) []testPoint {
	t.Helper()
	views, _, err := appendViews(nil, s.chunks, 0, 1<<40, nil)
	require.NoError(t, err)
	return readPoints(t, &viewSeries{lset: s.lset, views: views})
}

func TestNormalize(t *testing.T) {
	a, b, c := podLabels("a"), podLabels("b"), podLabels("c")
	disjoint := seriesOf(t, a, steps(0, 90), steps(100, 190), steps(200, 290))
	overlapping := seriesOf(t, b, steps(0, 150), steps(100, 250))
	unknown := seriesOf(t, c, steps(0, 50))
	unknown.spans[0] = span{mint: 1, maxt: 0}
	outside := seriesOf(t, podLabels("d"), steps(500, 600))
	out, err := normalize([]storage.Series{outside, unknown, overlapping, disjoint}, 55, 245)
	require.NoError(t, err)
	require.Len(t, out, 2, "a series without samples in range is left out")
	require.Equal(t, a, out[0].lset)
	// the chunk wholly inside is kept as it is; the ones across either end are cut there
	require.Same(t, disjoint.chunks[1], chunkenc.Iterable(out[0].chunks[1].chk))
	require.Equal(t, []span{{60, 90}, {100, 190}, {200, 240}}, []span{
		out[0].chunks[0].span, out[0].chunks[1].span, out[0].chunks[2].span,
	})
	require.Equal(t, steps(60, 240), rawPoints(t, out[0]))
	// overlapping chunks are merged into disjoint ones
	require.Equal(t, b, out[1].lset)
	require.Equal(t, steps(60, 240), rawPoints(t, out[1]))

	// samples, including histograms, are encoded as chunks of their kinds
	samples := listSeries(podLabels("e"), testPoint{t: 1, f: 1}, testPoint{t: 2, h: true}, testPoint{t: 3, f: 3})
	out, err = normalize([]storage.Series{samples}, 0, 10)
	require.NoError(t, err)
	require.Len(t, out[0].chunks, 3)
	require.Equal(t, chunkenc.EncFloatHistogram, out[0].chunks[1].chk.Encoding())
	require.Equal(t, []testPoint{{t: 1, f: 1}, {t: 2, h: true}, {t: 3, f: 3}}, rawPoints(t, out[0]))

	// a series a member sent twice is joined, its samples merged where its parts overlap
	out, err = normalize([]storage.Series{seriesOf(t, a, steps(0, 50)), seriesOf(t, a, steps(100, 150))}, 0, 200)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, append(steps(0, 50), steps(100, 150)...), rawPoints(t, out[0]))
	out, err = normalize([]storage.Series{seriesOf(t, a, steps(0, 100)), seriesOf(t, a, steps(50, 150))}, 0, 200)
	require.NoError(t, err)
	require.Equal(t, steps(0, 150), rawPoints(t, out[0]))

	// a chunk whose samples can't be read fails, rather than being cut short
	damaged := seriesOf(t, a, steps(0, 90))
	damaged.chunks[0] = truncated(t, damaged.chunks[0].(chunkenc.Chunk))
	_, err = normalize([]storage.Series{damaged}, 5, 85)
	require.Error(t, err)
}

// truncated returns c with half its samples' bytes, so its iterator fails partway
func truncated(t *testing.T, c chunkenc.Chunk) chunkenc.Chunk {
	t.Helper()
	b := c.Bytes()
	size, _ := chunkHeaderSize(c.Encoding())
	n := size + (len(b)-size)/2
	chk, err := newChunk(c.Encoding(), b[:n:n])
	require.NoError(t, err)
	return chk
}

func TestMergeBlock(t *testing.T) {
	a, b, c := podLabels("a"), podLabels("b"), podLabels("c")
	chunk := func(first, last int64) rawChunk {
		chk, sp := xorChunkOf(t, steps(first, last))
		return rawChunk{span: sp, chk: chk}
	}
	old := &rawBlock{series: []rawSeries{{lset: a, chunks: []rawChunk{chunk(100, 190)}}, {lset: c, chunks: []rawChunk{chunk(0, 90)}}}}
	merged := mergeBlock(old, []rawSeries{
		{lset: a, chunks: []rawChunk{chunk(0, 90)}}, {lset: b, chunks: []rawChunk{chunk(0, 90)}},
	}, timeseries.ExtentList{extent(0, 190)}, false)
	require.Len(t, merged.series, 3)
	require.Equal(t, steps(0, 190), rawPoints(t, merged.series[0]))
	require.Equal(t, b, merged.series[1].lset)
	require.Equal(t, c, merged.series[2].lset)
	// the cached block is left as it was
	require.Len(t, old.series[0].chunks, 1)
	require.Len(t, mergeBlock(nil, merged.series, nil, false).series, 3)
}

func TestViewIterator(t *testing.T) {
	c1, s1 := xorChunkOf(t, steps(0, 90))
	c2, s2 := xorChunkOf(t, steps(100, 190))
	c3, s3 := xorChunkOf(t, steps(200, 290))
	s := &viewSeries{lset: podLabels("a"), views: []chunkView{
		{chk: c1, lo: 20, hi: s1.maxt}, {chk: c2, lo: s2.mint, hi: s2.maxt}, {chk: c3, lo: s3.mint, hi: 250},
	}}
	require.Equal(t, steps(20, 250), readPoints(t, s))

	it := s.Iterator(nil)
	require.Equal(t, chunkenc.ValFloat, it.Seek(55))
	require.Equal(t, int64(60), it.AtT())
	require.Equal(t, chunkenc.ValFloat, it.Seek(40), "a seek behind the current sample stays")
	require.Equal(t, int64(60), it.AtT())
	require.Equal(t, chunkenc.ValFloat, it.Seek(85), "a seek within the current chunk")
	require.Equal(t, int64(90), it.AtT())
	require.Equal(t, chunkenc.ValFloat, it.Seek(205), "a seek past the next chunk")
	require.Equal(t, int64(210), it.AtT())
	require.Equal(t, chunkenc.ValNone, it.Seek(260))
	require.Equal(t, chunkenc.ValNone, it.Seek(100))
	require.Equal(t, chunkenc.ValNone, it.Next())
	require.Zero(t, it.AtST())
	require.NoError(t, it.Err())

	// views whose chunks overlap, as spans a member misreported, pass on each sample once, in order
	overlapping := &viewSeries{views: []chunkView{{chk: c1, lo: 0, hi: 1000}, {chk: c1, lo: 0, hi: 1000}}}
	require.Equal(t, steps(0, 90), readPoints(t, overlapping))
	require.Same(t, it, overlapping.Iterator(it), "an iterator is reused")

	// a damaged chunk ends the series with its error
	damaged := &viewSeries{views: []chunkView{{chk: truncated(t, c1), lo: 0, hi: 1000}, {chk: c2, lo: 0, hi: 1000}}}
	dit := damaged.Iterator(nil)
	for dit.Next() != chunkenc.ValNone {
	}
	require.Error(t, dit.Err())
	require.Equal(t, chunkenc.ValNone, dit.Seek(150))
	dit = damaged.Iterator(nil)
	require.Equal(t, chunkenc.ValNone, dit.Seek(85))
	require.Error(t, dit.Err())

	// histograms are read into the caller's buffers
	h := chunkenc.NewHistogramChunk()
	app, err := h.Appender()
	require.NoError(t, err)
	_, _, _, err = app.AppendHistogram(nil, 0, 5, testHistogram(), true)
	require.NoError(t, err)
	hit := (&viewSeries{views: []chunkView{{chk: h, lo: 0, hi: 10}}}).Iterator(nil)
	require.Equal(t, chunkenc.ValHistogram, hit.Next())
	_, got := hit.AtHistogram(&histogram.Histogram{})
	require.Equal(t, testHistogram().Count, got.Count)
	_, fh := hit.AtFloatHistogram(nil)
	require.Equal(t, float64(testHistogram().Count), fh.Count)
}

// rawSeriesOf is a series of one chunk of points
func rawSeriesOf(t *testing.T, pod string, points []testPoint) rawSeries {
	t.Helper()
	c, sp := xorChunkOf(t, points)
	return rawSeries{lset: podLabels(pod), chunks: []rawChunk{{span: sp, chk: c}}}
}

func TestAssembleJoinsReadsOntoCachedSeries(t *testing.T) {
	// a block caches b and d from 100 to 190; reads hold some of them, and others, before and after it
	cached := func() []rawSource {
		return []rawSource{{
			series: []rawSeries{rawSeriesOf(t, "b", steps(100, 190)), rawSeriesOf(t, "d", steps(100, 190))},
			lo:     100, hi: 190, block: "block",
		}}
	}
	reads := func(pods ...string) []rawSource {
		before, after := rawSource{lo: 0, hi: 99}, rawSource{lo: 191, hi: 290}
		for _, pod := range pods {
			before.series = append(before.series, rawSeriesOf(t, pod, steps(0, 90)))
			after.series = append(after.series, rawSeriesOf(t, pod, steps(200, 290)))
		}
		return []rawSource{after, before}
	}
	pointsByPod := func(series []storage.Series) map[string][]testPoint {
		out := make(map[string][]testPoint, len(series))
		var last labels.Labels
		for i, s := range series {
			if i > 0 {
				require.Negative(t, labels.Compare(last, s.Labels()), "series are in label order")
			}
			last = s.Labels()
			out[s.Labels().Get(testPod)] = readPoints(t, s)
		}
		return out
	}

	// assembled returns the cached block's series, assembled
	assembled := func() *assembly {
		a := &assembly{}
		_, _, err := a.add(context.Background(), cached(), nil)
		require.NoError(t, err)
		return a
	}
	a := &assembly{}
	samples, bad, err := a.add(context.Background(), cached(), nil)
	require.NoError(t, err)
	require.Empty(t, bad)
	require.Equal(t, int64(2*len(steps(100, 190))), samples, "the cached block's samples are counted")
	require.Equal(t, map[string][]testPoint{"b": steps(100, 190), "d": steps(100, 190)}, pointsByPod(a.result()))

	// reads of only cached series add their views to those series in place
	a = assembled()
	first := &a.series[0]
	samples, _, err = a.add(context.Background(), reads("b", "d"), nil)
	require.NoError(t, err)
	require.Zero(t, samples, "nothing read is counted as cached")
	require.Same(t, first, &a.series[0], "the cached series' array is kept")
	require.Nil(t, a.spare)
	require.Equal(t, map[string][]testPoint{"b": steps(0, 290), "d": steps(0, 290)}, pointsByPod(a.result()))

	// reads adding series the cache lacks, before, between and after its own, move to an array of their own
	a = assembled()
	base := a.series
	kept := append([]*viewSeries(nil), base...)
	_, _, err = a.add(context.Background(), reads("a", "c", "d", "e"), nil)
	require.NoError(t, err)
	require.Equal(t, map[string][]testPoint{
		"a": append(steps(0, 90), steps(200, 290)...), "b": steps(100, 190), "c": append(steps(0, 90), steps(200, 290)...),
		"d": steps(0, 290), "e": append(steps(0, 90), steps(200, 290)...),
	}, pointsByPod(a.result()))
	require.Equal(t, kept, base, "the cached series' array is never overwritten")

	// an assembly starts in its spare array, and once series move from an array, the next series added move to it
	spare := make([]*viewSeries, 0, 8)
	a = &assembly{spare: spare}
	_, _, err = a.add(context.Background(), cached(), nil)
	require.NoError(t, err)
	require.Same(t, &spare[:1][0], &a.series[0])
	_, _, err = a.add(context.Background(), reads("a", "c", "d", "e"), nil)
	require.NoError(t, err)
	require.Same(t, &spare[:1][0], &a.spare[:1][0])
	later := rawSource{lo: 300, hi: 390, series: []rawSeries{rawSeriesOf(t, "f", steps(300, 390))}}
	_, _, err = a.add(context.Background(), []rawSource{later}, nil)
	require.NoError(t, err)
	require.Same(t, &spare[:1][0], &a.series[0], "the spare array is reused")
	require.Len(t, a.series, 6)
	require.Equal(t, steps(300, 390), pointsByPod(a.result())["f"])

	// with nothing cached, the reads alone are assembled
	a = &assembly{}
	_, _, err = a.add(context.Background(), reads("a"), nil)
	require.NoError(t, err)
	require.Equal(t, map[string][]testPoint{"a": append(steps(0, 90), steps(200, 290)...)}, pointsByPod(a.result()))
}

// countedChunk counts the times its samples are counted: by number for a cached chunk wholly within a view, else by reading
// it through; each, when set, is called once it has counted
type countedChunk struct {
	chunkenc.Chunk
	counted *int
	each    func()
}

func (c countedChunk) count() {
	*c.counted++
	if c.each != nil {
		c.each()
	}
}

func (c countedChunk) NumSamples() int {
	c.count()
	return c.Chunk.NumSamples()
}

func (c countedChunk) Iterator(it chunkenc.Iterator) chunkenc.Iterator {
	c.count()
	return c.Chunk.Iterator(it)
}

// countedSeries is pod's series of chunks of 10 samples, one each 100ms from first, counting each chunk's counts in counted
func countedSeries(t *testing.T, pod string, first, chunks int, counted *int, each func()) rawSeries {
	t.Helper()
	rs := rawSeries{lset: podLabels(pod)}
	for i := first; i < first+chunks; i++ {
		c, sp := xorChunkOf(t, steps(int64(i)*100, int64(i)*100+90))
		rs.chunks = append(rs.chunks, rawChunk{span: sp, chk: countedChunk{Chunk: c, counted: counted, each: each}})
	}
	return rs
}

func TestAssemblyStopsWithinASeries(t *testing.T) {
	const chunks, perChunk = 50, 10
	var counted int
	// source is block's samples of pod's series of chunks, from first, within lo to hi
	source := func(block, pod string, first int, lo, hi int64) rawSource {
		return rawSource{lo: lo, hi: hi, block: block, series: []rawSeries{countedSeries(t, pod, first, chunks, &counted, nil)}}
	}
	whole := func(block string, first int) rawSource {
		return source(block, "a", first, int64(first)*100, int64(first+chunks)*100-1)
	}
	for name, test := range map[string]struct {
		// before is assembled under no budget first, and charged to it, as an earlier block is
		before  []rawSource
		sources []rawSource
		budget  *Budget
		// counted is how many chunks the limited add counts, and fits whether it succeeds
		counted int
		fits    bool
	}{
		"the first chunk":  {sources: []rawSource{whole("block", 0)}, budget: &Budget{MaxSamples: perChunk - 1}, counted: 1},
		"the second chunk": {sources: []rawSource{whole("block", 0)}, budget: &Budget{MaxSamples: perChunk + 1}, counted: 2},
		"exactly the budget": {
			sources: []rawSource{whole("block", 0)}, budget: &Budget{MaxSamples: chunks * perChunk}, counted: chunks, fits: true,
		},
		"a series assembled before, from another block": {
			before: []rawSource{whole("earlier", 0)}, sources: []rawSource{whole("block", chunks)},
			budget: &Budget{MaxSamples: chunks*perChunk + perChunk + 1}, counted: 2,
		},
		"a series of several sources": {
			sources: []rawSource{whole("later", chunks), whole("block", 0)},
			budget:  &Budget{MaxSamples: chunks*perChunk + 1}, counted: chunks + 1,
		},
		"a chunk crossing the range's start": {
			// the first chunk's samples from 50ms are 5, read through
			sources: []rawSource{source("block", "a", 0, 45, chunks*100-1)}, budget: &Budget{MaxSamples: perChunk/2 - 1}, counted: 1,
		},
		"a series limit it fits": {sources: []rawSource{whole("block", 0)}, budget: &Budget{MaxSeries: 1}, counted: chunks, fits: true},
		"a series limit it exceeds": {
			before: []rawSource{whole("earlier", 0)}, sources: []rawSource{source("block", "b", 0, 0, chunks*100-1)},
			budget: &Budget{MaxSeries: 1}, counted: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := &assembly{}
			for _, src := range test.before {
				samples, _, err := a.add(context.Background(), []rawSource{src}, nil)
				require.NoError(t, err)
				require.NoError(t, test.budget.add(int64(len(a.series)), samples))
			}
			counted = 0
			_, bad, err := a.add(context.Background(), test.sources, test.budget)
			require.Equal(t, test.counted, counted, "no chunk past the budget's limit is counted")
			if test.fits {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrFetchLimit)
			require.Empty(t, bad, "a block the select can't afford isn't taken for a bad one")
		})
	}

	// a select whose context ends stops counting within countDoneChunks chunks, and reports no bad block
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counted = 0
	long := rawSource{
		lo: 0, hi: 10 * countDoneChunks * 100, block: "block",
		series: []rawSeries{countedSeries(t, "a", 0, 10*countDoneChunks, &counted, func() {
			if counted == 2 {
				cancel()
			}
		})},
	}
	_, bad, err := (&assembly{}).add(ctx, []rawSource{long}, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, bad)
	require.Equal(t, countDoneChunks, counted)
	// and one under no budget whose context hasn't ended counts every chunk
	counted = 0
	_, _, err = (&assembly{}).add(context.Background(), []rawSource{long}, nil)
	require.NoError(t, err)
	require.Equal(t, 10*countDoneChunks, counted)
}

func TestAssemblyStopsAtTheBudget(t *testing.T) {
	const pods = 100
	var counted int
	src := rawSource{lo: 0, hi: 1000, block: "block"}
	for i := range pods {
		rs := rawSeriesOf(t, fmt.Sprintf("%03d", i), steps(100, 190))
		rs.chunks[0].chk = countedChunk{Chunk: rs.chunks[0].chk, counted: &counted}
		src.series = append(src.series, rs)
	}
	perSeries := int64(len(steps(100, 190)))
	// already is what the budget counted before the assembly; counted is how many series the assembly then counts
	for name, test := range map[string]struct {
		budget           *Budget
		already, counted int
	}{
		"series":                 {budget: &Budget{MaxSeries: 3}, counted: 4},
		"samples":                {budget: &Budget{MaxSamples: 2*perSeries + 1}, counted: 3},
		"series counted before":  {budget: &Budget{MaxSeries: 5}, already: 4, counted: 2},
		"within both":            {budget: &Budget{MaxSeries: pods, MaxSamples: pods * perSeries}, counted: pods},
		"within an unset budget": {counted: pods},
	} {
		t.Run(name, func(t *testing.T) {
			counted = 0
			require.NoError(t, test.budget.add(int64(test.already), 0))
			a := &assembly{}
			samples, _, err := a.add(context.Background(), []rawSource{src}, test.budget)
			require.Equal(t, test.counted, counted, "no series past the budget's limit is counted")
			if test.budget != nil {
				series, samples := test.budget.Fetched()
				require.Equal(t, [2]int64{int64(test.already), 0}, [2]int64{series, samples}, "the assembly charges nothing")
			}
			if test.counted < pods {
				require.ErrorIs(t, err, ErrFetchLimit)
				return
			}
			require.NoError(t, err)
			require.Len(t, a.series, pods)
			require.Equal(t, pods*perSeries, samples)
		})
	}
}

func TestBoundedIterator(t *testing.T) {
	c, _ := xorChunkOf(t, steps(0, 90))
	s := &boundedSeries{Series: &chunkSeries{lset: podLabels("a"), chunks: []chunkenc.Iterable{c}}, mint: 15, maxt: 55}
	it := s.Iterator(nil)
	require.Equal(t, steps(20, 50), readPoints(t, s))
	it = s.Iterator(it)
	require.Equal(t, chunkenc.ValFloat, it.Next())
	require.Equal(t, int64(20), it.AtT())
	require.Equal(t, chunkenc.ValNone, it.Seek(60))
	require.Equal(t, chunkenc.ValNone, it.Next(), "an exhausted iterator stays so")
	require.NoError(t, it.Err())
}

func TestBlockIndex(t *testing.T) {
	for at, want := range map[int64]int64{
		0: 0, rawBlockMs - 1: 0, rawBlockMs: 1, -1: -1, -rawBlockMs: -1, -rawBlockMs - 1: -2,
	} {
		require.Equal(t, want, blockIndex(at), at)
	}
}

func TestCompareMatchers(t *testing.T) {
	eq := labels.MustNewMatcher(labels.MatchEqual, testPod, "a")
	for _, test := range []struct {
		a, b *labels.Matcher
		want int
	}{
		{eq, labels.MustNewMatcher(labels.MatchEqual, testPod, "a"), 0},
		{eq, labels.MustNewMatcher(labels.MatchEqual, testPod, "b"), -1},
		{eq, labels.MustNewMatcher(labels.MatchNotEqual, testPod, "a"), -1},
		{labels.MustNewMatcher(labels.MatchRegexp, testPod, "a"), eq, 1},
		{eq, labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, "a"), 1},
	} {
		require.Equal(t, test.want, max(-1, min(1, compareMatchers(test.a, test.b))), "%s %s", test.a, test.b)
	}
}

func TestForwardedHeaders(t *testing.T) {
	require.Empty(t, forwardedHeaders(nil, nil))
	pc := po.New()
	pc.Path = testQueryPath
	pc.RequestHeaders = map[string]string{testConfigHeader: testConfigValue}
	paths := po.List{pc}
	r := httptest.NewRequest(http.MethodGet, testQueryPath, nil)
	r.Header = nil
	require.Equal(t, testConfigValue, forwardedHeaders(paths, r).Get(testConfigHeader))
	r = httptest.NewRequest(http.MethodGet, testQueryPath, nil)
	r.Header.Set(headers.NameConnection, testTenantHeader)
	r.Header.Set(testTenantHeader, testTenant)
	h := forwardedHeaders(paths, r)
	require.Empty(t, h.Get(testTenantHeader), "a header the connection names is hop-by-hop")
	require.Empty(t, h.Get(headers.NameConnection))
	// a source that doesn't forward headers itself is read with the client's own
	require.Equal(t, r.Header, sentHeaders(&MemorySource{}, r))
	require.Nil(t, sentHeaders(&MemorySource{}, nil))
}
