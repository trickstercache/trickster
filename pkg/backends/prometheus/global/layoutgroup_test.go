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
	"net/http"
	"runtime"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
)

const (
	// layoutTestBuckets is how many empty buckets a test histogram has, each a bit of its chunk's data; each array an
	// iterator lays out for them fills whole pages, so it takes no more than it is charged
	layoutTestBuckets uint32 = 8192
	// layoutTestAt is when a test histogram is sampled, within the decoder tests' range
	layoutTestAt = 150
	// layoutTestSlack is what iterators take besides their bucket layouts
	layoutTestSlack = 64 << 10
	// heapAttempts is how many times a heap measurement is taken, the least and most kept
	heapAttempts     = 3
	layoutRemoteRead = "remote read"
	layoutStoreAPI   = "storeapi"
)

// emptyHistogram is an integer histogram of buckets empty buckets
func emptyHistogram(buckets uint32) *histogram.Histogram {
	return &histogram.Histogram{PositiveSpans: []histogram.Span{{Length: buckets}}, PositiveBuckets: make([]int64, buckets)}
}

// appendedChunk is a chunk of enc Prometheus appends h to at each time
func appendedChunk(t testing.TB, enc chunkenc.Encoding, h *histogram.Histogram, ts ...int64) prompb.Chunk {
	t.Helper()
	c, err := chunkenc.NewEmptyChunk(enc)
	require.NoError(t, err)
	app, err := c.Appender()
	require.NoError(t, err)
	for _, at := range ts {
		if enc == chunkenc.EncFloatHistogram {
			_, _, app, err = app.AppendFloatHistogram(nil, 0, at, h.ToFloat(nil), false)
		} else {
			_, _, app, err = app.AppendHistogram(nil, 0, at, h, false)
		}
		require.NoError(t, err)
	}
	return prompb.Chunk{MinTimeMs: ts[0], MaxTimeMs: ts[len(ts)-1], Type: promChunkTypes[enc], Data: c.Bytes()}
}

// emptyBucketsChunk is a chunk Prometheus appends of one integer histogram of layoutTestBuckets empty buckets
func emptyBucketsChunk(t testing.TB) prompb.Chunk {
	t.Helper()
	return appendedChunk(t, chunkenc.EncHistogram, emptyHistogram(layoutTestBuckets), layoutTestAt)
}

// layoutSeries is a series message of one chunk
type layoutSeries struct {
	lset  labels.Labels
	chunk prompb.Chunk
}

func (s layoutSeries) remote() *prompb.ChunkedSeries {
	return &prompb.ChunkedSeries{Labels: prompb.FromLabels(s.lset, nil), Chunks: []prompb.Chunk{s.chunk}}
}

func (s layoutSeries) store() []byte {
	var kv []string
	s.lset.Range(func(l labels.Label) { kv = append(kv, l.Name, l.Value) })
	enc := uint64(storeChunkXOR)
	if s.chunk.Type == prompb.Chunk_HISTOGRAM {
		enc = storeChunkHistogram
	}
	return encodeTestSeries(kv, spanned(encodeTestChunk(enc, s.chunk.Data, true), s.chunk.MinTimeMs, s.chunk.MaxTimeMs))
}

// layoutShape is a response of n series messages holding chunk, read alone or together as deduplication groups them
type layoutShape struct {
	series   []layoutSeries
	replicas []string
	// groups is how many groups deduplication reads, and together the most series of one
	groups, together int
}

// layoutShapes returns responses sending chunk n times: as copies of one series, each after a message of another
// series keeping nothing, so it doesn't continue the copy before it; as replicas; and as n series of their own
func layoutShapes(t testing.TB, chunk prompb.Chunk, n int) map[string]layoutShape {
	t.Helper()
	other := xorChunk(t, pointsAt(decodeTestMint/2)...)
	var copies, replicas, distinct []layoutSeries
	for i := range n {
		id := strconv.Itoa(i)
		copies = append(copies, layoutSeries{podLabels("a"), chunk}, layoutSeries{podLabels("b"), other})
		replicas = append(replicas, layoutSeries{podLabels("a", testReplicaLabel, id), chunk})
		distinct = append(distinct, layoutSeries{podLabels(id), chunk})
	}
	return map[string]layoutShape{
		"copies":   {series: copies, groups: 1, together: n},
		"replicas": {series: replicas, replicas: []string{testReplicaLabel}, groups: 1, together: n},
		// series that differ only in a label deduplication keeps are read one at a time
		"replicas without replica labels": {series: replicas, groups: n, together: 1},
		"distinct":                        {series: distinct, groups: n, together: 1},
	}
}

// remoteBody is a streamed remote-read response of one frame holding series
func remoteBody(t *testing.T, series []layoutSeries) []byte {
	t.Helper()
	chunked := make([]*prompb.ChunkedSeries, len(series))
	for i, s := range series {
		chunked[i] = s.remote()
	}
	var body bytes.Buffer
	writeFrame(t, &body, chunked...)
	return body.Bytes()
}

// storeMessages is a StoreAPI response of a message for each of series, and their total size
func storeMessages(series []layoutSeries) ([][]byte, int64) {
	frames := make([][]byte, len(series))
	var size int64
	for i, s := range series {
		frames[i] = s.store()
		size += int64(len(frames[i]))
	}
	return frames, size
}

// layoutDecodes decodes the series of a shape through a decoder as each API sends them
var layoutDecodes = map[string]func(*testing.T, *decoder, []layoutSeries) error{
	layoutRemoteRead: func(t *testing.T, d *decoder, series []layoutSeries) error {
		chunked := make([]*prompb.ChunkedSeries, len(series))
		for i, s := range series {
			chunked[i] = s.remote()
		}
		return d.chunkedReadResponse(chunkedFrame(t, chunked...))
	},
	layoutStoreAPI: func(_ *testing.T, d *decoder, series []layoutSeries) error {
		d.Kind = errStoreAPI
		frames, _ := storeMessages(series)
		for _, f := range frames {
			if _, err := d.seriesResponse(f); err != nil {
				return err
			}
		}
		return nil
	},
}

func TestDecoderChargesCopiesAndReplicasTogether(t *testing.T) {
	const n = 3
	chunk := emptyBucketsChunk(t)
	laid, err := histogramLayout(chunkenc.EncHistogram, chunk.Data)
	require.NoError(t, err)
	require.Equal(t, int64(layoutTestBuckets)*histogramLayoutBytes+spanBytes, laid)
	for name, shape := range layoutShapes(t, chunk, n) {
		// what each kept series takes, the builder's slots for the most labels any message has, and each group's sum
		var kept int64
		var slots int
		for _, s := range shape.series {
			slots = max(slots, s.lset.Len())
			if s.chunk.Type != prompb.Chunk_HISTOGRAM {
				continue
			}
			kept += chunkSeriesBytes + chunkBytes + int64(len(s.chunk.Data))
			s.lset.Range(func(l labels.Label) { kept += int64(len(l.Name)+len(l.Value)) + labelLengthBytes })
		}
		cost := kept + int64(slots)*labelSlotBytes + int64(shape.groups)*groupBytes + int64(shape.together)*laid
		for api, decode := range layoutDecodes {
			t.Run(name+"/"+api, func(t *testing.T) {
				for _, limit := range []int64{cost, cost - 1} {
					d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
					d.limit, d.allowance, d.replicaLabels = limit, limit, shape.replicas
					err := decode(t, d, shape.series)
					if limit < cost {
						require.ErrorIs(t, err, ErrFetchLimit)
						continue
					}
					require.NoError(t, err)
					require.Zero(t, d.allowance)
					require.Equal(t, int64(shape.together)*laid, d.laid, "the most one group lays out at once")
					require.Len(t, d.groups, shape.groups)
					require.Len(t, d.out, n)
				}
			})
		}
	}
}

func TestDecoderChargesEveryScratchLayout(t *testing.T) {
	const buckets = 64
	h := emptyHistogram(buckets)
	// each chunk lies across the range's end, so its encoding's scratch iterator reads it through and keeps its layout
	ints := appendedChunk(t, chunkenc.EncHistogram, h, 190, 210)
	floats := appendedChunk(t, chunkenc.EncFloatHistogram, h, 190, 210)
	intLaid, floatLaid := buckets*histogramLayoutBytes+spanBytes, buckets*floatHistogramLayoutBytes+spanBytes
	frame := chunkedFrame(t,
		&prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{ints}},
		&prompb.ChunkedSeries{Labels: testLabels("b"), Chunks: []prompb.Chunk{floats}},
	)
	labelsSize := int64(len(labels.MetricName)+len(testMetric)+len(testPod)+1) + 2*labelLengthBytes
	cost := 2*labelSlotBytes + 2*(chunkSeriesBytes+labelsSize+chunkBytes+groupBytes) + int64(len(ints.Data)+len(floats.Data)) +
		intLaid + floatLaid
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
		require.Equal(t, intLaid+floatLaid, d.laid, "both scratch iterators keep their layouts")
		require.Len(t, d.out, 2)
	}
}

func TestDecoderGroupsOnlyHistograms(t *testing.T) {
	// a response of float chunks has nothing to lay out, so it hashes no labels and keeps no groups
	d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
	d.replicaLabels = []string{testReplicaLabel}
	require.NoError(t, d.chunkedReadResponse(chunkedFrame(t,
		&prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{xorChunk(t, pointsAt(110, 150)...)}},
		&prompb.ChunkedSeries{Labels: testLabels("b"), Chunks: []prompb.Chunk{xorChunk(t, pointsAt(120, 210)...)}},
	)))
	require.Len(t, d.out, 2)
	require.Nil(t, d.groups)
	require.Zero(t, d.laid)
	allocs := testing.AllocsPerRun(1000, func() { _ = d.groupKey() })
	require.Zero(t, allocs, "hashing a label set without its replica labels allocates nothing")
}

// heapAfter returns how much more of the heap is in use once next has run, with what it returns kept alive
func heapAfter(next func() any) int64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	kept := next()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(kept)
	return int64(after.HeapAlloc) - int64(before.HeapAlloc)
}

// heapAfterRuns returns the least and most heapAfter of a few runs of next: what other goroutines allocate meanwhile only
// adds to a run, and what they release only takes from it, so the least bounds what next keeps from above, the most from below
func heapAfterRuns(next func() any) (least, most int64) {
	least = heapAfter(next)
	most = least
	for range heapAttempts - 1 {
		n := heapAfter(next)
		least, most = min(least, n), max(most, n)
	}
	return least, most
}

func TestDeduplicationLaysOutNoMoreThanDecodingCharged(t *testing.T) {
	const n = 12
	for name, shape := range layoutShapes(t, emptyBucketsChunk(t), n) {
		for _, dedup := range []string{merge.TSMDeduplicationPenalty, merge.TSMDeduplicationChain} {
			t.Run(name+"/"+dedup, func(t *testing.T) {
				d := newDecoder(nil, decodeTestMint, decodeTestMaxt, errRemoteRead)
				d.replicaLabels = shape.replicas
				require.NoError(t, layoutDecodes[layoutRemoteRead](t, d, shape.series))
				require.LessOrEqual(t, d.laid, d.limit)
				series := Deduplicate([][]storage.Series{bound(d.out, decodeTestMint, decodeTestMaxt)}, shape.replicas,
					dedup, "")
				require.Len(t, series, shape.groups)
				// the engine reads one series at a time, which lays out every chunk deduplication chains into it
				least, most := heapAfterRuns(func() any {
					it := series[0].Iterator(nil)
					require.Equal(t, chunkenc.ValHistogram, it.Next())
					return it
				})
				t.Logf("charged %d, laid out %d to %d", d.laid, least, most)
				require.Greater(t, most, d.laid/2, "the layouts were read")
				require.LessOrEqual(t, least, d.laid+layoutTestSlack)
				runtime.KeepAlive(series)
			})
		}
	}
}

func TestSourcesRefuseCopiesAndReplicasPastTheirLimit(t *testing.T) {
	const n = 900
	chunk := emptyBucketsChunk(t)
	require.Less(t, len(chunk.Data), int(layoutTestBuckets)/7, "each empty bucket takes a bit")
	type member struct {
		source Source
		size   int64
	}
	members := map[string]func(*testing.T, []layoutSeries) member{
		layoutRemoteRead: func(t *testing.T, series []layoutSeries) member {
			body := remoteBody(t, series)
			rr := newTestRemoteRead(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set(headers.NameContentType, contentTypeStreamed)
				_, _ = w.Write(body)
			})
			rr.ID = rawTestIdentity
			return member{source: rr, size: int64(len(body))}
		},
		layoutStoreAPI: func(t *testing.T, series []layoutSeries) member {
			frames, size := storeMessages(series)
			store := (&testStore{frames: frames}).serve(t)
			store.ID = rawTestIdentity
			return member{source: store, size: size}
		},
	}
	for name, shape := range layoutShapes(t, chunk, n) {
		for api, newMember := range members {
			for _, cached := range []bool{false, true} {
				t.Run(name+"/"+api+"/cached="+strconv.FormatBool(cached), func(t *testing.T) {
					m := newMember(t, shape.series)
					// the response's own size is its limit, so only what it lays out once decoded can exceed it
					budget := &Budget{MaxResponseBytes: m.size}
					q := NewQueryable([]Member{{Name: rawTestMember, Source: m.source}}, shape.replicas, "", false, nil)
					if cached {
						rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{MaxResponseBytes: m.size})
						q.WithRawCache(rc.RawCache)
						t.Cleanup(rc.Wait)
					}
					qr, err := q.Querier(decodeTestMint, decodeTestMaxt)
					require.NoError(t, err)
					set := qr.Select(testContext(t, budget), true, nil,
						labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric))
					var read int
					for set.Next() {
						read++
					}
					if shape.together > 1 {
						require.ErrorIs(t, set.Err(), ErrFetchLimit)
						return
					}
					require.NoError(t, set.Err())
					require.Equal(t, shape.groups, read)
				})
			}
		}
	}
}
