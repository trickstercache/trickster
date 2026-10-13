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
	views, _, err := appendViews(nil, s.chunks, 0, 1<<40, false)
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

	base, samples, bad, err := assemble(nil, cached())
	require.NoError(t, err)
	require.Empty(t, bad)
	require.Equal(t, int64(2*len(steps(100, 190))), samples, "the cached block's samples are counted")
	require.Equal(t, map[string][]testPoint{"b": steps(100, 190), "d": steps(100, 190)}, pointsByPod(base))

	// reads of only cached series add their views to those series in place
	base, _, _, err = assemble(nil, cached())
	require.NoError(t, err)
	first := &base[0]
	joined, samples, _, err := assemble(base, reads("b", "d"))
	require.NoError(t, err)
	require.Zero(t, samples, "nothing read is counted as cached")
	require.Same(t, first, &joined[0], "the cached series' array is kept")
	require.Equal(t, map[string][]testPoint{"b": steps(0, 290), "d": steps(0, 290)}, pointsByPod(joined))

	// reads adding series the cache lacks, before, between and after its own, move to an array of their own
	base, _, _, err = assemble(nil, cached())
	require.NoError(t, err)
	kept := append([]storage.Series(nil), base...)
	joined, _, _, err = assemble(base, reads("a", "c", "d", "e"))
	require.NoError(t, err)
	require.Equal(t, map[string][]testPoint{
		"a": append(steps(0, 90), steps(200, 290)...), "b": steps(100, 190), "c": append(steps(0, 90), steps(200, 290)...),
		"d": steps(0, 290), "e": append(steps(0, 90), steps(200, 290)...),
	}, pointsByPod(joined))
	require.Equal(t, kept, base, "the cached series' array is never overwritten")

	// with nothing cached, the reads alone are assembled
	joined, _, _, err = assemble(nil, reads("a"))
	require.NoError(t, err)
	require.Equal(t, map[string][]testPoint{"a": append(steps(0, 90), steps(200, 290)...)}, pointsByPod(joined))
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
