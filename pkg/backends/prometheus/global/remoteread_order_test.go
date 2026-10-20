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
	"cmp"
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/remoteread"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
)

const (
	// orderTestStart is the first sample time of the order tests' members, within the range read from them
	orderTestStart = limitTestMint + rawTestStep
	// orderFloat and orderHistogram name each kind of sample, as an order error reports it
	orderFloat, orderHistogram = "float", "histogram"
)

// orderFloats returns float samples at each time, each valued by its place in the list, from 1
func orderFloats(ts ...int64) []prompb.Sample {
	out := make([]prompb.Sample, len(ts))
	for i, at := range ts {
		out[i] = prompb.Sample{Timestamp: at, Value: float64(i + 1)}
	}
	return out
}

// orderHistograms returns histogram samples at each time, each summing to its place in the list, from 1
func orderHistograms(ts ...int64) []prompb.Histogram {
	out := make([]prompb.Histogram, len(ts))
	for i, at := range ts {
		h := testHistogram()
		h.Sum = float64(i + 1)
		out[i] = prompb.FromIntHistogram(at, h)
	}
	return out
}

// orderPoints reads a series' samples, a histogram's value as its sum
func orderPoints(t *testing.T, s storage.Series) []testPoint {
	t.Helper()
	var out []testPoint
	it := s.Iterator(nil)
	for vt := it.Next(); vt != chunkenc.ValNone; vt = it.Next() {
		if vt == chunkenc.ValFloat {
			at, f := it.At()
			out = append(out, testPoint{t: at, f: f})
			continue
		}
		at, fh := it.AtFloatHistogram(nil)
		out = append(out, testPoint{t: at, f: fh.Sum, h: true})
	}
	require.NoError(t, it.Err())
	return out
}

// setPoints reads every series of a set by its labels, failing on the set's error
func setPoints(t *testing.T, set storage.SeriesSet) map[string][]testPoint {
	t.Helper()
	out := make(map[string][]testPoint)
	for set.Next() {
		out[set.At().Labels().String()] = orderPoints(t, set.At())
	}
	require.NoError(t, set.Err())
	return out
}

// selectRange reads q's series of the test metric from mint to maxt, as a query of team A does
func selectRange(t *testing.T, ctx context.Context, q *Queryable, mint, maxt int64) storage.SeriesSet {
	t.Helper()
	qr, err := q.Querier(mint, maxt)
	require.NoError(t, err)
	t.Cleanup(func() { _ = qr.Close() })
	return qr.Select(ctx, true, &storage.SelectHints{Start: mint, End: maxt}, testMetricMatcher)
}

// orderFailure is the error a member's answer fails with when its sample of kind at t follows one of its kind at last
func orderFailure(kind string, t, last int64) string {
	return errRemoteRead.Error() + ": " + errSampleOrder.Error() + ": a " + kind + " sample at " +
		strconv.FormatInt(t, 10) + " follows one at " + strconv.FormatInt(last, 10)
}

// unknownSchema returns h with a schema no histogram has, which fails a histogram decoded from it
func unknownSchema(h prompb.Histogram) prompb.Histogram {
	h.Schema = 100
	return h
}

// wireSeries returns a ReadResponse of one series of pod a whose sample and histogram messages are in the order given
func wireSeries(t *testing.T, fields ...any) []byte {
	t.Helper()
	var msg []byte
	for _, l := range testLabels("a") {
		b, err := l.Marshal()
		require.NoError(t, err)
		msg = appendBytesField(msg, seriesLabels, b)
	}
	for _, f := range fields {
		switch f := f.(type) {
		case prompb.Sample:
			b, err := f.Marshal()
			require.NoError(t, err)
			msg = appendBytesField(msg, timeSeriesSamples, b)
		case prompb.Histogram:
			b, err := f.Marshal()
			require.NoError(t, err)
			msg = appendBytesField(msg, timeSeriesHistograms, b)
		}
	}
	return appendBytesField(nil, remoteread.ReadResponseResults, appendBytesField(nil, remoteread.QueryResultTimeseries, msg))
}

func TestDecoderSampleOrder(t *testing.T) {
	const a, b, c, d = decodeTestMint + 10, decodeTestMint + 20, decodeTestMint + 30, decodeTestMint + 40
	const before, after = decodeTestMint - 50, decodeTestMaxt + 50
	const negativeMint, negativeMaxt = -1000, 1000
	h := func(at int64, sum float64) testPoint { return testPoint{t: at, f: sum, h: true} }
	tests := []struct {
		name   string
		series *prompb.TimeSeries
		// message is the response read when series is nil
		message []byte
		// mint and maxt are the range read, the decoder tests' when zero
		mint, maxt int64
		// want is the series' samples, or nil when the response fails as out of order, with err
		want []testPoint
		err  string
		// fetched is how many samples the budget counts: every one sent in range, as a chunk's header counts them
		fetched int64
	}{
		{
			name: "floats in order", series: &prompb.TimeSeries{Samples: orderFloats(before, a, b, after)},
			want: []testPoint{{t: a, f: 2}, {t: b, f: 3}}, fetched: 2,
		},
		{
			name: "floats sharing a time keep the first", series: &prompb.TimeSeries{Samples: orderFloats(a, a, b)},
			want: []testPoint{{t: a, f: 1}, {t: b, f: 3}}, fetched: 3,
		},
		{
			name: "histograms sharing a time keep the first", series: &prompb.TimeSeries{Histograms: orderHistograms(a, a, b)},
			want: []testPoint{h(a, 1), h(b, 3)}, fetched: 3,
		},
		{
			name:   "kinds interleave",
			series: &prompb.TimeSeries{Samples: orderFloats(a, c), Histograms: orderHistograms(b, d)},
			want:   []testPoint{{t: a, f: 1}, h(b, 1), {t: c, f: 2}, h(d, 2)}, fetched: 4,
		},
		{
			name:   "a float and a histogram at one time keep the float",
			series: &prompb.TimeSeries{Samples: orderFloats(b), Histograms: orderHistograms(a, b, c)},
			want:   []testPoint{h(a, 1), {t: b, f: 1}, h(c, 3)}, fetched: 4,
		},
		{
			// a histogram dropped at a time already kept is never decoded, so its unknown schema fails nothing
			name: "a histogram at a float's time is never decoded",
			series: &prompb.TimeSeries{
				Samples:    orderFloats(a),
				Histograms: []prompb.Histogram{unknownSchema(orderHistograms(a)[0]), orderHistograms(a, b)[1]},
			},
			want: []testPoint{{t: a, f: 1}, h(b, 2)}, fetched: 3,
		},
		{
			name: "a histogram at an earlier histogram's time is never decoded",
			series: &prompb.TimeSeries{Histograms: []prompb.Histogram{
				orderHistograms(a)[0], unknownSchema(orderHistograms(a, a)[1]), orderHistograms(a, a, b)[2],
			}},
			want: []testPoint{h(a, 1), h(b, 3)}, fetched: 3,
		},
		{
			// each kind is in order, though a histogram on the wire precedes an earlier float
			name: "kinds interleaved on the wire, a histogram first",
			message: wireSeries(t, orderHistograms(b)[0], orderFloats(a)[0], prompb.Sample{Timestamp: b, Value: 2},
				prompb.FromIntHistogram(c, testHistogram())),
			want: []testPoint{{t: a, f: 1}, {t: b, f: 2}, h(c, testHistogram().Sum)}, fetched: 4,
		},
		{
			name:   "times before 1970",
			series: &prompb.TimeSeries{Samples: orderFloats(-20, -10, 5), Histograms: orderHistograms(-15, -5)},
			mint:   negativeMint, maxt: negativeMaxt,
			want:    []testPoint{{t: -20, f: 1}, h(-15, 1), {t: -10, f: 2}, h(-5, 2), {t: 5, f: 3}},
			fetched: 5,
		},
		{
			name: "floats going back", series: &prompb.TimeSeries{Samples: orderFloats(a, c, b)},
			err: orderFailure(orderFloat, b, c),
		},
		{
			name: "floats going back a millisecond", series: &prompb.TimeSeries{Samples: orderFloats(a, a-1)},
			err: orderFailure(orderFloat, a-1, a),
		},
		{
			name:   "histograms going back",
			series: &prompb.TimeSeries{Samples: orderFloats(a, b), Histograms: orderHistograms(b, a)},
			err:    orderFailure(orderHistogram, a, b),
		},
		{
			name: "histograms going back a millisecond", series: &prompb.TimeSeries{Histograms: orderHistograms(a, a-1)},
			err: orderFailure(orderHistogram, a-1, a),
		},
		{
			name: "floats going back outside the range", series: &prompb.TimeSeries{Samples: orderFloats(after+10, after)},
			err: orderFailure(orderFloat, after, after+10),
		},
		{
			name: "a float past maxt, then one in range", series: &prompb.TimeSeries{Samples: orderFloats(after, b)},
			err: orderFailure(orderFloat, b, after),
		},
		{
			name:   "a histogram past maxt, then one in range",
			series: &prompb.TimeSeries{Samples: orderFloats(a), Histograms: orderHistograms(after, b)},
			err:    orderFailure(orderHistogram, b, after),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := test.message
			if test.series != nil {
				test.series.Labels = testLabels("a")
				data = samplesResponse(t, test.series)
			}
			mint, maxt := cmp.Or(test.mint, decodeTestMint), cmp.Or(test.maxt, decodeTestMaxt)
			budget := &Budget{}
			dec := newDecoder(budget, mint, maxt, errRemoteRead)
			err := dec.readResponse(data)
			fetchedSeries, fetchedSamples := budget.Fetched()
			if test.want == nil {
				require.ErrorIs(t, err, errRemoteRead, "the member's answer is malformed")
				require.ErrorIs(t, err, errSampleOrder)
				require.EqualError(t, err, test.err)
				require.Empty(t, dec.out)
				require.Zero(t, fetchedSeries+fetchedSamples, "nothing of the series is counted")
				return
			}
			require.NoError(t, err)
			require.Len(t, dec.out, 1)
			require.Equal(t, test.want, orderPoints(t, dec.out[0]), "one sample is kept at each time, in time order")
			require.Equal(t, test.want, orderPoints(t, bound(dec.out, mint, maxt)[0]))
			require.Equal(t, int64(1), fetchedSeries)
			require.Equal(t, test.fetched, fetchedSamples)
		})
	}
}

func TestDecoderRefusesMisorderedSamplesBeforeCountingThem(t *testing.T) {
	const a, b, c = decodeTestMint + 10, decodeTestMint + 20, decodeTestMint + 30
	// more samples than are counted at once, all at one time and so in order, but for the last, going back
	many := make([]int64, countQuantum+10)
	for i := range many {
		many[i] = b
	}
	many[len(many)-1] = a
	tests := map[string]struct {
		series *prompb.TimeSeries
		budget *Budget
	}{
		// a sample limit the series exceeds would fail it as over the limit, were it counted first
		"floats past a sample limit": {
			series: &prompb.TimeSeries{Samples: orderFloats(a, c, b)}, budget: &Budget{MaxSamples: 1},
		},
		"histograms past a sample limit": {
			series: &prompb.TimeSeries{Samples: orderFloats(a), Histograms: orderHistograms(a, c, b)},
			budget: &Budget{MaxSamples: 1},
		},
		"past a count added at once": {series: &prompb.TimeSeries{Samples: orderFloats(many...)}, budget: &Budget{}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			test.series.Labels = testLabels("a")
			dec := newDecoder(test.budget, decodeTestMint, decodeTestMaxt, errRemoteRead)
			err := dec.readResponse(samplesResponse(t, test.series))
			require.ErrorIs(t, err, errSampleOrder)
			require.NotErrorIs(t, err, ErrFetchLimit, "the member's answer is malformed, not over a limit")
			series, samples := test.budget.Fetched()
			require.Zero(t, series)
			require.Zero(t, samples, "nothing of the series is added to the budget")
		})
	}
}

func TestRemoteReadRejectsMisorderedSamples(t *testing.T) {
	source := newTestRemoteRead(t, func(w http.ResponseWriter, _ *http.Request) {
		writeSamples(t, w,
			&prompb.TimeSeries{Labels: testLabels("a"), Samples: orderFloats(1, 2)},
			&prompb.TimeSeries{Labels: testLabels("b"), Samples: orderFloats(20, 5)},
		)
	})
	series, _, err := source.Select(testContext(t, nil), 0, 10, nil)
	require.ErrorIs(t, err, errRemoteRead)
	require.ErrorIs(t, err, errSampleOrder)
	require.Nil(t, series, "a well-ordered series of the same answer isn't kept either")
}

func TestRawCacheKeepsNothingOfMisorderedSamples(t *testing.T) {
	const mint, maxt = limitTestMint, limitTestMaxt
	// two samples in range precede one past it, and then one going back into it, which seeking past maxt would skip
	misordered := &prompb.TimeSeries{Labels: testLabels("a"), Samples: orderFloats(orderTestStart,
		orderTestStart+rawTestStep, maxt+rawTestStep, orderTestStart+2*rawTestStep)}
	fixed := &prompb.TimeSeries{Labels: testLabels("a"), Samples: orderFloats(orderTestStart, orderTestStart+rawTestStep)}
	want := map[string][]testPoint{
		labels.FromStrings(labels.MetricName, testMetric, testPod, "a").String(): {
			{t: orderTestStart, f: 1}, {t: orderTestStart + rawTestStep, f: 2},
		},
	}
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			var bad atomic.Bool
			bad.Store(true)
			source := newTestRemoteRead(t, func(w http.ResponseWriter, r *http.Request) {
				_ = decodeTestRequest(t, r)
				requests.Add(1)
				if bad.Load() {
					writeSamples(t, w, misordered)
					return
				}
				writeSamples(t, w, fixed)
			})
			source.ID = rawTestIdentity
			rc := newRawTestCache(newCache(), RawCacheOptions{})
			// each read's allowance is less than the samples in range, which counting them would exceed
			var budgets []*Budget
			tight := func() context.Context {
				budgets = append(budgets, &Budget{MaxSamples: 1})
				return WithBudget(teamRequest(rawTestTeamA), budgets[len(budgets)-1])
			}
			_, _, err := rc.selectAt(t, tight(), source, mint, maxt)
			require.ErrorIs(t, err, errRemoteRead)
			require.ErrorIs(t, err, errSampleOrder)
			require.NotErrorIs(t, err, ErrFetchLimit)

			var failures []error
			failed := func(member string, err error) {
				require.Equal(t, rawTestMember, member)
				failures = append(failures, err)
			}
			members := []Member{{Name: rawTestMember, Source: source}}
			for _, partial := range []bool{true, false} {
				direct := NewQueryable(members, nil, merge.TSMDeduplicationPenalty, partial, failed)
				cached := NewQueryable(members, nil, merge.TSMDeduplicationPenalty, partial, failed).WithRawCache(rc.RawCache)
				// read directly or through the cache, the member fails alike
				for _, q := range []*Queryable{direct, cached} {
					set := selectRange(t, tight(), q, mint, maxt)
					require.False(t, set.Next(), "no series of the member is kept")
					if partial {
						require.NoError(t, set.Err())
						require.Len(t, set.Warnings(), 1)
						require.EqualError(t, set.Warnings().AsErrors()[0], ErrMemberUnavailable.Error()+": "+rawTestMember)
					} else {
						require.ErrorIs(t, set.Err(), ErrMemberUnavailable)
					}
					rc.Wait()
				}
			}
			require.Len(t, failures, 4)
			for _, err := range failures {
				require.ErrorIs(t, err, errSampleOrder, "the member failed, rather than a fetch limit ending the query")
				require.NotErrorIs(t, err, ErrFetchLimit)
			}
			require.Len(t, budgets, 5)
			for _, b := range budgets {
				series, samples := b.Fetched()
				require.Zero(t, series+samples, "nothing of the misordered series is counted")
			}
			require.Equal(t, int32(5), requests.Load(), "every read reached the member, as no failed read was kept")

			// once the member answers in order, the next read reaches it, and what it keeps answers the one after
			bad.Store(false)
			cached := NewQueryable(members, nil, merge.TSMDeduplicationPenalty, false, nil).WithRawCache(rc.RawCache)
			require.Equal(t, want, setPoints(t, selectRange(t, teamRequest(rawTestTeamA), cached, mint, maxt)))
			rc.Wait()
			require.Equal(t, int32(6), requests.Load())
			require.Equal(t, want, setPoints(t, selectRange(t, teamRequest(rawTestTeamA), cached, mint, maxt)))
			require.Equal(t, int32(6), requests.Load())
		})
	}
}

func TestRawCacheAgreesOnSampleOrder(t *testing.T) {
	const mint, maxt = limitTestMint, limitTestMaxt
	at := func(i int64) int64 { return orderTestStart + i*rawTestStep }
	pod := func(name string) string {
		return labels.FromStrings(labels.MetricName, testMetric, testPod, name).String()
	}
	answers := map[string]struct {
		answer func(t *testing.T, w http.ResponseWriter)
		want   map[string][]testPoint
	}{
		// samples sharing a time keep the first, a float before a histogram
		"samples": {
			answer: func(t *testing.T, w http.ResponseWriter) {
				writeSamples(t, w, &prompb.TimeSeries{
					Labels:  testLabels("a"),
					Samples: orderFloats(at(0), at(2), at(2), at(4)), Histograms: orderHistograms(at(1), at(2), at(3), at(3)),
				})
			},
			want: map[string][]testPoint{pod("a"): {
				{t: at(0), f: 1}, {t: at(1), f: 1, h: true}, {t: at(2), f: 2}, {t: at(3), f: 3, h: true}, {t: at(4), f: 4},
			}},
		},
		// a series continues across frames in chunks that overlap, as the protocol allows, which are merged, not refused
		"streamed": {
			answer: func(t *testing.T, w http.ResponseWriter) {
				var body bytes.Buffer
				writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
					xorChunk(t, testPoint{t: at(0), f: 1}, testPoint{t: at(2), f: 3}),
				}})
				writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
					xorChunk(t, testPoint{t: at(1), f: 2}, testPoint{t: at(2), f: 3}, testPoint{t: at(3), f: 4}),
				}}, &prompb.ChunkedSeries{Labels: testLabels("b"), Chunks: []prompb.Chunk{
					xorChunk(t, testPoint{t: at(0), f: 1}),
				}})
				w.Header().Set(headers.NameContentType, limitTestStreamed)
				_, _ = w.Write(body.Bytes())
			},
			want: map[string][]testPoint{
				pod("a"): {{t: at(0), f: 1}, {t: at(1), f: 2}, {t: at(2), f: 3}, {t: at(3), f: 4}},
				pod("b"): {{t: at(0), f: 1}},
			},
		},
		// a series sent twice keeps the first copy's sample at a time both hold, whichever its kind, as replicas do
		"copies": {
			answer: func(t *testing.T, w http.ResponseWriter) {
				writeSamples(t, w,
					&prompb.TimeSeries{Labels: testLabels("a"), Samples: []prompb.Sample{
						{Timestamp: at(0), Value: 1}, {Timestamp: at(1), Value: 2}, {Timestamp: at(4), Value: 5},
					}, Histograms: orderHistograms(at(2))},
					&prompb.TimeSeries{Labels: testLabels("b"), Samples: orderFloats(at(0))},
					&prompb.TimeSeries{Labels: testLabels("a"), Samples: []prompb.Sample{
						{Timestamp: at(1), Value: 20}, {Timestamp: at(2), Value: 30}, {Timestamp: at(3), Value: 40},
					}, Histograms: orderHistograms(at(4))},
				)
			},
			want: map[string][]testPoint{
				pod("a"): {{t: at(0), f: 1}, {t: at(1), f: 2}, {t: at(2), f: 1, h: true}, {t: at(3), f: 40}, {t: at(4), f: 5}},
				pod("b"): {{t: at(0), f: 1}},
			},
		},
		// a series streamed again after another series is a copy of it, not its continuation
		"streamed copies": {
			answer: func(t *testing.T, w http.ResponseWriter) {
				var body bytes.Buffer
				writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
					xorChunk(t, testPoint{t: at(0), f: 1}, testPoint{t: at(1), f: 2}), histogramChunk(t, at(2)),
					xorChunk(t, testPoint{t: at(4), f: 5}),
				}}, &prompb.ChunkedSeries{Labels: testLabels("b"), Chunks: []prompb.Chunk{
					xorChunk(t, testPoint{t: at(0), f: 1}),
				}})
				writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
					xorChunk(t, testPoint{t: at(1), f: 20}, testPoint{t: at(2), f: 30}, testPoint{t: at(3), f: 40}),
					histogramChunk(t, at(4)),
				}})
				w.Header().Set(headers.NameContentType, limitTestStreamed)
				_, _ = w.Write(body.Bytes())
			},
			want: map[string][]testPoint{
				pod("a"): {
					{t: at(0), f: 1},
					{t: at(1), f: 2},
					{t: at(2), f: testHistogram().Sum, h: true},
					{t: at(3), f: 40},
					{t: at(4), f: 5},
				},
				pod("b"): {{t: at(0), f: 1}},
			},
		},
	}
	for answerName, answer := range answers {
		for cacheName, newCache := range rawTestCaches(t) {
			t.Run(answerName+"/"+cacheName, func(t *testing.T) {
				var requests atomic.Int32
				source := newTestRemoteRead(t, func(w http.ResponseWriter, r *http.Request) {
					_ = decodeTestRequest(t, r)
					requests.Add(1)
					answer.answer(t, w)
				})
				source.ID = rawTestIdentity
				rc := newRawTestCache(newCache(), RawCacheOptions{})
				members := []Member{{Name: rawTestMember, Source: source}}
				direct := NewQueryable(members, nil, merge.TSMDeduplicationPenalty, false, nil)
				cached := NewQueryable(members, nil, merge.TSMDeduplicationPenalty, false, nil).WithRawCache(rc.RawCache)
				ctx := teamRequest(rawTestTeamA)
				require.Equal(t, answer.want, setPoints(t, selectRange(t, ctx, direct, mint, maxt)))
				require.Equal(t, answer.want, setPoints(t, selectRange(t, ctx, cached, mint, maxt)))
				rc.Wait()
				require.Equal(t, int32(2), requests.Load())
				require.Equal(t, answer.want, setPoints(t, selectRange(t, ctx, cached, mint, maxt)), "as cached")
				require.Equal(t, int32(2), requests.Load())
			})
		}
	}
}
